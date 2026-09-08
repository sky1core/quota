package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestFiveHourAdmissionRejectsInvalidRemaining(t *testing.T) {
	for _, left := range []any{nil, "50", math.NaN(), math.Inf(1), math.Inf(-1), -1.0, 101.0} {
		t.Run(fmt.Sprint(left), func(t *testing.T) {
			now := time.Now()
			window := map[string]any{"key": "session", "windowMins": 300, "left": left}
			quota := testQuota(window)
			if _, ok := scoreClaudeQuota(quota, "", false, 0, now); ok {
				t.Fatal("Claude admitted invalid session remaining")
			}
			if _, ok := scoreCodexQuota(quota, false, 0, now); ok {
				t.Fatal("Codex admitted invalid five-hour remaining")
			}
		})
	}
}

func TestCodexAdmissionUsesActualDuration(t *testing.T) {
	for _, tc := range []struct {
		duration any
		left     int
		allow    bool
	}{
		{nil, 25, false}, {"300", 25, false}, {300.9, 25, false},
		{0, 25, false}, {-300, 25, false}, {math.NaN(), 25, false}, {math.Inf(1), 25, false},
		{300, 24, false}, {300, 25, true},
		{10080, 24, true}, {43200, 24, true}, {600, 24, true},
	} {
		t.Run(fmt.Sprintf("%v/left%d", tc.duration, tc.left), func(t *testing.T) {
			quota := testQuota(map[string]any{"key": "5h", "label": "5h", "windowMins": tc.duration, "left": tc.left})
			_, ok := scoreCodexQuota(quota, false, 5, time.Now())
			if ok != tc.allow {
				t.Fatalf("duration %v left %d: usable = %v, want %v", tc.duration, tc.left, ok, tc.allow)
			}
		})
	}
}

func TestCodexAdmissionRequiresReportedUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		allow bool
	}{
		{"missing", "", false},
		{"null", `,"usedPercent":null`, false},
		{"zero", `,"usedPercent":0`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			t.Setenv("CODEX_HOME", "")
			raw := fmt.Sprintf(`{"rateLimits":{"primary":{"windowDurationMins":300%s},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`, tc.field)
			quotacache.Put("codex:"+filepath.Join(home, ".codex"), raw, time.Now().Add(time.Hour))
			_, err := selectCodexAccount(config.Config{}, time.Now())
			if (err == nil) != tc.allow {
				t.Fatalf("exec-prompt selection error = %v, want allowed = %v", err, tc.allow)
			}
			result, err := buildSelectAgentResult(config.Config{}, selectAgentOptions{agent: selectAgentCodex}, time.Now())
			if (err == nil) != tc.allow || (result.Selected != nil) != tc.allow {
				t.Fatalf("select-agent result = %+v, error = %v, want allowed = %v", result, err, tc.allow)
			}
		})
	}
}

func TestClaudeSessionAdmissionFloorBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	healthyWeekly := testWindow("weekly_all", "Week", 90, now.Add(3*24*time.Hour), 0)
	for _, tc := range []struct {
		sessionLeft float64
		usable      bool
	}{
		{5, false},
		{6, false},
		{24.99, false},
		{25, true},
		{25.01, true},
	} {
		quota := testQuota(healthyWeekly, testWindow("session", "Session", tc.sessionLeft, now.Add(time.Hour), 0))
		_, ok := scoreClaudeQuota(quota, "", false, defaultExecPromptMinLeftPct, now)
		if ok != tc.usable {
			t.Fatalf("session left %v%%: usable = %v, want %v", tc.sessionLeft, ok, tc.usable)
		}
	}
}

func TestCodexFiveHourAdmissionFloorBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	healthyWeekly := testWindow("weekly", "7d", 90, now.Add(3*24*time.Hour), 10080)
	for _, tc := range []struct {
		fiveHourLeft float64
		usable       bool
	}{
		{5, false},
		{6, false},
		{24.99, false},
		{25, true},
		{25.01, true},
	} {
		quota := testQuota(testWindow("5h", "5h", tc.fiveHourLeft, now.Add(time.Hour), 300), healthyWeekly)
		_, ok := scoreCodexQuota(quota, false, defaultExecPromptMinLeftPct, now)
		if ok != tc.usable {
			t.Fatalf("5h left %v%%: usable = %v, want %v", tc.fiveHourLeft, ok, tc.usable)
		}
	}
}

func TestAdmissionExcludesRichWeeklyWhenShortWindowBelowFloor(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(3*24*time.Hour), 0),
		testWindow("session", "Session", 20, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("a rich weekly window must not admit an account whose session is below the 25% floor")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 20, now.Add(time.Hour), 300),
		testWindow("weekly", "7d", 90, now.Add(3*24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("a rich weekly window must not admit an account whose 5h window is below the 25% floor")
	}
}

func TestAdmissionKeepsWeeklyAtMinLeftPctFloor(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", defaultExecPromptMinLeftPct, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 25, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("weekly at the 5% minLeftPct floor with a healthy session must stay usable")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 25, now.Add(time.Hour), 300),
		testWindow("weekly", "7d", defaultExecPromptMinLeftPct, now.Add(24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("weekly at the 5% minLeftPct floor with a healthy 5h window must stay usable")
	}
}

func TestAdmissionStrongerMinLeftPctStillApplies(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 30, now.Add(time.Hour), 0),
	), "", false, 40, now); ok {
		t.Fatal("a session above the 25% floor but below the configured 40% floor must be excluded")
	}
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 45, now.Add(time.Hour), 0),
	), "", false, 40, now); !ok {
		t.Fatal("a session above the configured 40% floor must stay usable")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 30, now.Add(time.Hour), 300),
		testWindow("weekly", "7d", 90, now.Add(24*time.Hour), 10080),
	), false, 40, now); ok {
		t.Fatal("a 5h window above the 25% floor but below the configured 40% floor must be excluded")
	}
}

func TestCodexAdmissionAllowsAbsentButNotUnreadableFiveHourWindow(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("weekly", "7d", 90, now.Add(3*24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("a healthy weekly-only Codex report must be usable")
	}
	if _, ok := scoreCodexQuota(testQuota(
		map[string]any{"key": "5h", "label": "5h", "windowMins": 300, "resetsAt": now.Add(time.Hour)},
		testWindow("weekly", "7d", 90, now.Add(3*24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("a Codex 5h window without a numeric left must be excluded")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 90, now.Add(time.Hour), 300),
	), false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("a 5h-only Codex report above the 25% floor must stay usable")
	}
}

func TestClaudeAdmissionAllowsAbsentButNotUnreadableSessionWindow(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(3*24*time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("a healthy weekly-only Claude report must be usable")
	}
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(3*24*time.Hour), 0),
		map[string]any{"key": "session", "label": "Session", "resetsAt": now.Add(time.Hour)},
	), "", false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("a Claude session window without a numeric left must be excluded")
	}
}

func TestAdmissionPreservesRankingAmongPassingCandidates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put("claude:"+filepath.Join(home, ".claude"), "Current session: 40% used - resets in 4h\nCurrent week (all models): 80% used - resets in 1d", validUntil)
	quotacache.Put("claude:"+filepath.Join(home, ".claude-2"), "Current session: 40% used - resets in 4h\nCurrent week (all models): 20% used - resets in 1d", validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	selected, err := selectClaudeAccount(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "claude-2" {
		t.Fatalf("selected account = %q, want claude-2 (richer weekly among admitted candidates)", selected.Key)
	}
}

func TestSelectAgentSkipReasonReportsSessionAdmissionFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put("claude:"+filepath.Join(home, ".claude"), "Current session: 80% used - resets in 4h\nCurrent week (all models): 10% used - resets in 3d", validUntil)
	quotacache.Put("codex:"+filepath.Join(home, ".codex"), `{"rateLimits":{"primary":{"usedPercent":80,"windowDurationMins":300},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`, validUntil)

	result, err := buildSelectAgentResult(config.Config{}, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err == nil {
		t.Fatal("expected no usable account when every session/5h window is below the 25% floor")
	}
	text := formatSelectAgentResult(result)
	var jsonBuf strings.Builder
	if err := writeSelectAgentJSON(&jsonBuf, result); err != nil {
		t.Fatal(err)
	}
	jsonOut := jsonBuf.String()

	for _, candidate := range result.Candidates {
		if candidate.Status != selectAgentStatusSkipped {
			t.Fatalf("candidate %s status = %q, want skipped", candidate.Key, candidate.Status)
		}
		if !strings.Contains(candidate.Reason, "25") {
			t.Fatalf("candidate %s reason = %q, want it to name the 25%% floor", candidate.Key, candidate.Reason)
		}
		if !strings.Contains(text, candidate.Reason) {
			t.Fatalf("text output must render the skip reason for %s:\n%s", candidate.Key, text)
		}
		if !strings.Contains(jsonOut, candidate.Reason) {
			t.Fatalf("JSON output must carry the skip reason for %s:\n%s", candidate.Key, jsonOut)
		}
	}
}
