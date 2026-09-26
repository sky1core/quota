package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestWeeklyOnlyAdmissionThroughQuotaParsers(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			left  int
			floor float64
			allow bool
		}{
			{0, 5, false}, {4, 5, false}, {5, 5, true},
			{24, 5, true}, {90, 5, true}, {39, 40, false}, {40, 40, true},
		} {
			t.Run(fmt.Sprintf("%s/left%d/floor%g", provider, tc.left, tc.floor), func(t *testing.T) {
				raw := fmt.Sprintf("Current week (all models): %d%% used", 100-tc.left)
				if provider == "codex" {
					raw = fmt.Sprintf(`{"rateLimits":{"primary":{"windowDurationMins":10080,"usedPercent":%d},"secondary":null}}`, 100-tc.left)
				}
				checkAdmissionThroughParser(t, provider, raw, tc.floor, tc.allow, "")
			})
		}
	}
}

func TestIncompleteQuotaCannotBecomeWeeklyOnly(t *testing.T) {
	for _, short := range []string{
		`{}`, `{"windowDurationMins":300}`, `{"windowDurationMins":300,"usedPercent":null}`,
		`{"usedPercent":0}`, `{"windowDurationMins":null,"usedPercent":0}`,
		`{"windowDurationMins":0,"usedPercent":0}`, `{"windowDurationMins":-300,"usedPercent":0}`,
		`{"windowDurationMins":300,"usedPercent":-1}`, `{"windowDurationMins":300,"usedPercent":101}`,
	} {
		t.Run("codex/"+short, func(t *testing.T) {
			raw := fmt.Sprintf(`{"rateLimits":{"primary":%s,"secondary":{"windowDurationMins":10080,"usedPercent":10}}}`, short)
			checkAdmissionThroughParser(t, "codex", raw, 5, false, "quota window")
		})
	}
	for _, short := range []string{
		"Current session: unavailable", "Current session: -1% used", "Current session: 101% used",
		"Current session: 18446744073709551616% used", "Current session: 20%",
		"Current session 90% used", "Current session", "Current\tsession: unavailable",
	} {
		t.Run("claude/"+short, func(t *testing.T) {
			checkAdmissionThroughParser(t, "claude", short+"\nCurrent week (all models): 10% used", 5, false, "quota window")
		})
	}
}

func TestIncompleteWeeklyCannotBeIgnoredBesideHealthyShortWindow(t *testing.T) {
	checkAdmissionThroughParser(t, "codex", `{"rateLimits":{"primary":{"windowDurationMins":300,"usedPercent":0},"secondary":{"windowDurationMins":10080}}}`, 5, false, "quota window")
	checkAdmissionThroughParser(t, "claude", "Current session: 0% used\nCurrent week (all models): unavailable", 5, false, "quota window")
}

func TestMalformedDuplicateCannotBeHiddenByValidWindow(t *testing.T) {
	checkAdmissionThroughParser(t, "codex", `{"rateLimits":{"primary":{"windowDurationMins":10080,"usedPercent":0},"secondary":{"windowDurationMins":10080}}}`, 5, false, "quota window")
	checkAdmissionThroughParser(t, "claude", "Current week (all models): 0% used\nCurrent week (all models): unavailable", 5, false, "quota window")
}

func TestUnreadableOptionalModelRowPreservesAggregateAdmission(t *testing.T) {
	checkAdmissionThroughParser(t, "claude", "Current week (all models): 10% used\nCurrent week (Fable): unavailable", 5, true, "")
}

func TestCodexWeeklyOnlyResponsePositions(t *testing.T) {
	for _, snapshot := range []string{
		`{"primary":{"windowDurationMins":10080,"usedPercent":0}}`,
		`{"primary":null,"secondary":{"windowDurationMins":10080,"usedPercent":0}}`,
	} {
		for _, field := range []string{"rateLimits", "rateLimitsByLimitId"} {
			t.Run(field+snapshot, func(t *testing.T) {
				body := snapshot
				if field == "rateLimitsByLimitId" {
					body = `{"codex":` + snapshot + `}`
				}
				checkAdmissionThroughParser(t, "codex", fmt.Sprintf(`{%q:%s}`, field, body), 5, true, "")
			})
		}
	}
}

func TestQuotaWithoutUsableWindowsIsNotAdmitted(t *testing.T) {
	checkAdmissionThroughParser(t, "codex", `{"rateLimits":{"primary":null,"secondary":null,"planType":"pro"}}`, 5, false, "")
}

func TestSelectAgentRanksWeeklyOnlyAndSkipsIncompleteCandidate(t *testing.T) {
	for _, tc := range []struct {
		short    string
		selected string
	}{
		{"null", "codex"},
		{`{"windowDurationMins":300}`, "claude"},
		{`{"windowDurationMins":300,"usedPercent":76}`, "claude"},
	} {
		t.Run(tc.short, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CODEX_HOME", "")
			until := time.Now().Add(time.Hour)
			quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current week (all models): 50% used", until)
			quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), fmt.Sprintf(`{"rateLimits":{"primary":{"windowDurationMins":10080,"usedPercent":10},"secondary":%s}}`, tc.short), until)
			result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll})
			if err != nil || result.Selected == nil || result.Selected.Key != tc.selected {
				t.Fatalf("result = %+v, error = %v, want %s", result, err, tc.selected)
			}
		})
	}
}

func checkAdmissionThroughParser(t *testing.T, provider, raw string, floor float64, allow bool, reason string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	quotacache.Put(quotaTestCacheKey(t, provider, filepath.Join(home, "."+provider)), raw, time.Now().Add(time.Hour))
	cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
		provider: {MinLeftPct: &floor},
	}}}
	var err error
	if provider == "claude" {
		_, err = selectClaudeAccount(context.Background(), cfg, nil)
	} else {
		_, err = selectCodexAccount(context.Background(), cfg)
	}
	if (err == nil) != allow {
		t.Errorf("exec-prompt selection error = %v, want allowed = %v", err, allow)
	}
	result, err := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: provider})
	if (err == nil) != allow || (result.Selected != nil) != allow {
		t.Errorf("select-agent result = %+v, error = %v, want allowed = %v", result, err, allow)
	}
	if reason != "" {
		if len(result.Candidates) != 1 || !strings.Contains(result.Candidates[0].Reason, reason) {
			t.Errorf("candidates = %+v, want diagnostic containing %q", result.Candidates, reason)
		}
	}
}
