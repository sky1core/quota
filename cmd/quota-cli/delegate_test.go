package main

import (
	"bytes"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestClaudeScorePrefersWeeklyQuotaResettingSooner(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	slower, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(6*24*time.Hour), 0),
		testWindow("session", "Session", 100, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("first account should be usable")
	}
	sooner, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 25, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("second account should be usable")
	}
	if compareAccountScoresForTest(sooner, slower) <= 0 {
		t.Fatal("the same weekly quota resetting in one day must rank above six days")
	}
}

func TestClaudeScoreWeeklyOutranksSession(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	weeklyRich, _ := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(3*24*time.Hour), 0),
		testWindow("session", "Session", 25, now.Add(4*time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	sessionRich, _ := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 60, now.Add(3*24*time.Hour), 0),
		testWindow("session", "Session", 100, now.Add(4*time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if compareAccountScoresForTest(weeklyRich, sessionRich) <= 0 {
		t.Fatal("weekly quota must decide before session quota")
	}
}

func TestScoreFallsBackToRemainingQuotaWhenResetIsUnknown(t *testing.T) {
	known := scoredWindow{present: true, available: 50, resetKnown: true, availablePerMin: 0.5}
	unknown := scoredWindow{present: true, available: 50}
	if got := compareScoredWindow(known, unknown); got != 0 {
		t.Fatalf("equal remaining quota with one unknown reset must tie, got %d", got)
	}
}

func TestClaudeScoreDoesNotCompareSessionAgainstWeeklySlot(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	withWeekly, _ := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 10, now.Add(6*24*time.Hour), 0),
		testWindow("session", "Session", 25, now.Add(4*time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	withoutWeekly, _ := scoreClaudeQuota(testQuota(
		testWindow("session", "Session", 100, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if compareAccountScoresForTest(withWeekly, withoutWeekly) <= 0 {
		t.Fatal("a session window must not be compared in the weekly slot")
	}
}

func TestClaudeScoreUsesRequestedModelWindow(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	first := testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("extra_1", "Fable", 80, now.Add(6*24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(4*time.Hour), 0),
	)
	second := testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(6*24*time.Hour), 0),
		testWindow("extra_1", "Fable", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(4*time.Hour), 0),
	)
	firstGeneric, _ := scoreClaudeQuota(first, "", false, defaultExecPromptMinLeftPct, now)
	secondGeneric, _ := scoreClaudeQuota(second, "", false, defaultExecPromptMinLeftPct, now)
	if compareAccountScoresForTest(firstGeneric, secondGeneric) <= 0 {
		t.Fatal("aggregate weekly comparison should prefer the first account")
	}
	firstFable, _ := scoreClaudeQuota(first, "claude-fable-5", true, defaultExecPromptMinLeftPct, now)
	secondFable, _ := scoreClaudeQuota(second, "claude-fable-5", true, defaultExecPromptMinLeftPct, now)
	if compareAccountScoresForTest(secondFable, firstFable) <= 0 {
		t.Fatal("Fable comparison should prefer the second account")
	}
}

func TestClaudeScoreFallsBackWhenRequestedModelWindowIsAbsent(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	withoutRequestedModelRow, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	), "claude-opus-4", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("an account without a requested model quota row should fall back to aggregate Claude quota")
	}
	laterReset, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(6*24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	), "claude-opus-4", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("second account should be usable")
	}
	if compareAccountScoresForTest(withoutRequestedModelRow, laterReset) <= 0 {
		t.Fatal("fallback comparison should still use aggregate weekly quota")
	}
}

func TestClaudeScoreRequiresAggregateWindowEvenWithModelWindow(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("extra_1", "Fable", 90, now.Add(24*time.Hour), 0),
	), "fable", true, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("a model-only Claude report should not be usable without aggregate quota")
	}
}

func TestShouldCompareClaudeModelWindowRequiresEveryUsableAccount(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	withFable := testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("extra_1", "Fable", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	)
	withoutFable := testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	)
	invalidFable := testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		map[string]any{"key": "extra_1", "label": "Fable"},
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	)
	unusableWithoutFable := testQuota(
		testWindow("weekly_all", "Week", 1, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 1, now.Add(time.Hour), 0),
	)
	if shouldCompareClaudeModelWindow([]quotaProbeResult{{quota: withFable}, {quota: withoutFable}}, "claude-fable-5", defaultPromptFloors(2), now) {
		t.Fatal("model comparison should be disabled when a usable account lacks the requested model window")
	}
	if shouldCompareClaudeModelWindow([]quotaProbeResult{{quota: withFable}, {quota: invalidFable}}, "claude-fable-5", defaultPromptFloors(2), now) {
		t.Fatal("model comparison should require a scoreable requested model window")
	}
	if !shouldCompareClaudeModelWindow([]quotaProbeResult{{quota: withFable}, {quota: unusableWithoutFable}}, "claude-fable-5", defaultPromptFloors(2), now) {
		t.Fatal("unusable accounts without model windows should not disable model comparison")
	}
	if !shouldCompareClaudeModelWindow([]quotaProbeResult{{quota: withFable}, {err: os.ErrNotExist}}, "claude-fable-5", defaultPromptFloors(2), now) {
		t.Fatal("probe failures should not disable model comparison")
	}
	if !shouldCompareClaudeModelWindow([]quotaProbeResult{{quota: withFable}, {quota: withFable}}, "claude-fable-5", defaultPromptFloors(2), now) {
		t.Fatal("model comparison should be enabled when every usable account has the requested model window")
	}
}

func TestClaudeScoreComparesAggregateWhenModelComparisonDisabled(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	withFable, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 20, now.Add(6*24*time.Hour), 0),
		testWindow("extra_1", "Fable", 50, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	), "fable", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("account with a Fable row above the prompt floor should be usable")
	}
	withoutFable, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 80, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 80, now.Add(time.Hour), 0),
	), "fable", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("account without a Fable row should fall back to aggregate Claude quota")
	}
	if compareAccountScoresForTest(withoutFable, withFable) <= 0 {
		t.Fatal("a present model row must not be compared against aggregate weekly quota")
	}
}

func TestCodexScoreLongestWindowOutranksShortest(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	longRich, _ := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 25, now.Add(4*time.Hour), 300),
		testWindow("weekly", "7d", 80, now.Add(3*24*time.Hour), 10080),
	), true, defaultExecPromptMinLeftPct, now)
	shortRich, _ := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 100, now.Add(4*time.Hour), 300),
		testWindow("weekly", "7d", 60, now.Add(3*24*time.Hour), 10080),
	), true, defaultExecPromptMinLeftPct, now)
	if compareAccountScoresForTest(longRich, shortRich) <= 0 {
		t.Fatal("longest Codex window must decide before the shortest window")
	}
}

func TestScoresRejectPromptWindowsBelowFloor(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 1, now.Add(time.Minute), 0),
	), "", false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("Claude must not route to an account with a 1% applicable window")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 1, now.Add(time.Minute), 300),
		testWindow("weekly", "7d", 90, now.Add(24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("Codex must not route to an account with a 1% applicable window")
	}
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", 90, now.Add(24*time.Hour), 0),
		testWindow("extra_1", "Fable", 1, now.Add(time.Minute), 0),
		testWindow("session", "Session", 90, now.Add(time.Hour), 0),
	), "fable", false, defaultExecPromptMinLeftPct, now); ok {
		t.Fatal("Claude must not route to an account with a 1% requested model window")
	}
}

func TestScoresAllowPromptWindowsAtFloor(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", defaultExecPromptMinLeftPct, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 25, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("Claude should allow an account whose weekly window sits at the delegated prompt floor")
	}
	if _, ok := scoreCodexQuota(testQuota(
		testWindow("5h", "5h", 25, now.Add(time.Hour), 300),
		testWindow("weekly", "7d", defaultExecPromptMinLeftPct, now.Add(24*time.Hour), 10080),
	), false, defaultExecPromptMinLeftPct, now); !ok {
		t.Fatal("Codex should allow an account whose weekly window sits at the delegated prompt floor")
	}
}

func TestScoreAtPromptFloorPrefersSoonerReset(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	sooner, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", defaultExecPromptMinLeftPct, now.Add(24*time.Hour), 0),
		testWindow("session", "Session", 100, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("sooner account should be usable at the prompt floor")
	}
	later, ok := scoreClaudeQuota(testQuota(
		testWindow("weekly_all", "Week", defaultExecPromptMinLeftPct, now.Add(6*24*time.Hour), 0),
		testWindow("session", "Session", 100, now.Add(time.Hour), 0),
	), "", false, defaultExecPromptMinLeftPct, now)
	if !ok {
		t.Fatal("later account should be usable at the prompt floor")
	}
	if compareAccountScoresForTest(sooner, later) <= 0 {
		t.Fatal("equal prompt-floor surplus should prefer the account resetting sooner")
	}
}

func TestClaudeRequestedModel(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--model", "fable", "prompt"}, "fable"},
		{[]string{"-m=claude-opus-4", "prompt"}, "claude-opus-4"},
		{[]string{"--model=sonnet"}, "sonnet"},
		{[]string{"--model", "opus", "--append-system-prompt", "--model=sonnet", "--", "test"}, "opus"},
		{[]string{"--system-prompt", "--model", "sonnet"}, ""},
		{[]string{"--model=opus", "--append-system-prompt-file", "-m=sonnet"}, "opus"},
		{[]string{"--append-subagent-system-prompt", "--model=sonnet", "--model", "opus"}, "opus"},
		{[]string{"--model=opus", "--plan-mode-instructions", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--plugin-dir-no-mcp", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--settings=--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--settings", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--name", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "-n", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "-cn", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "-n--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--tools", "--model=sonnet"}, "opus"},
		{[]string{"--tools", "Read", "Edit", "--model", "opus"}, "opus"},
		{[]string{"--allowed-tools", "Read", "--model=opus"}, "opus"},
		{[]string{"--resume", "--model=opus"}, "opus"},
		{[]string{"--model=opus", "-rmsonnet"}, "opus"},
		{[]string{"--debug", "--model=opus"}, "opus"},
		{[]string{"--model=opus", "--append-system-prompt", "", "--model=sonnet"}, "sonnet"},
		{[]string{"--append-system-prompt", "--", "--model=opus"}, "opus"},
		{[]string{"--model=opus", "--", "--model=sonnet"}, "opus"},
		{[]string{"--model=opus", "--model", "sonnet"}, "sonnet"},
		{[]string{"--model=opus", "--model="}, ""},
		{[]string{"--model=opus", "--model", ""}, ""},
		{[]string{"-m", "opus"}, "opus"},
		{[]string{"-mopus"}, "opus"},
		{[]string{"-pmopus"}, "opus"},
		{[]string{"--", "--model", "fable"}, ""},
		{[]string{"prompt"}, ""},
	}
	for _, tt := range tests {
		original := append([]string(nil), tt.args...)
		if got := claudeRequestedModel(tt.args); got != tt.want {
			t.Errorf("claudeRequestedModel(%v) = %q, want %q", tt.args, got, tt.want)
		}
		if !reflect.DeepEqual(tt.args, original) {
			t.Errorf("model extraction mutated args: got %v, want %v", tt.args, original)
		}
	}
}

func TestFindRequestedModelWindowMatchesActualExtraLabel(t *testing.T) {
	windows := quotaWindows(testQuota(
		testWindow("session", "Session", 80, time.Time{}, 0),
		testWindow("weekly_all", "Week", 80, time.Time{}, 0),
		testWindow("extra_1", "Fable", 80, time.Time{}, 0),
		testWindow("extra_2", "Sonnet only", 80, time.Time{}, 0),
	))

	if got := findRequestedModelWindow(windows, "claude-fable-5"); got == nil || got["label"] != "Fable" {
		t.Fatalf("claude-fable-5 matched %v, want Fable", got)
	}
	if got := findRequestedModelWindow(windows, "claude-sonnet-4"); got == nil || got["label"] != "Sonnet only" {
		t.Fatalf("claude-sonnet-4 matched %v, want Sonnet only", got)
	}
	if got := findRequestedModelWindow(windows, "claude-opus-4"); got != nil {
		t.Fatalf("claude-opus-4 must not match an unreported model row, got %v", got)
	}
}

func TestFindRequestedModelWindowMatchesOpusPlanAliasWhenRowExists(t *testing.T) {
	windows := quotaWindows(testQuota(
		testWindow("session", "Session", 80, time.Time{}, 0),
		testWindow("weekly_all", "Week", 80, time.Time{}, 0),
		testWindow("extra_1", "Opus", 80, time.Time{}, 0),
	))

	if got := findRequestedModelWindow(windows, "opusplan"); got == nil || got["label"] != "Opus" {
		t.Fatalf("opusplan matched %v, want Opus", got)
	}
}

func TestDelegatedArgvPreservesForwardedArgs(t *testing.T) {
	forwarded := []string{"--json", "-m", "gpt-5", "prompt text"}
	got := delegatedArgv("/bin/codex", []string{"exec"}, forwarded)
	want := []string{"/bin/codex", "exec", "--json", "-m", "gpt-5", "prompt text"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(forwarded, []string{"--json", "-m", "gpt-5", "prompt text"}) {
		t.Fatalf("forwarded args mutated: %v", forwarded)
	}
}

func TestPromptEnvironmentsSelectOnlyRequestedAccount(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"CLAUDECODE=1",
		"CLAUDE_CONFIG_DIR=/old-claude",
		"ANTHROPIC_API_HOST=https://example.invalid",
		"ANTHROPIC_API_KEY=secret",
		"ANTHROPIC_AUTH_TOKEN=secret",
		"ANTHROPIC_BASE_URL=https://example.invalid",
		"CLAUDE_API_KEY=secret",
		"CLAUDE_CODE_API_BASE_URL=https://example.invalid",
		"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
		"CLAUDE_CODE_EFFORT_LEVEL=low",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN=secret",
		"CLAUDE_CODE_OAUTH_TOKEN=secret",
		"CLAUDE_CODE_SIMPLE=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"CODEX_HOME=/old-codex",
		"CODEX_ACCESS_TOKEN=secret",
		"CODEX_API_KEY=secret",
		"CODEX_SQLITE_HOME=/caller-state",
		"OPENAI_API_KEY=secret",
		"OPENAI_BASE_URL=https://example.invalid",
	}
	claudeEnv := envMap(claude.EnvForConfigDir(base, "/new-claude"))
	if claudeEnv["CLAUDE_CONFIG_DIR"] != "/new-claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", claudeEnv["CLAUDE_CONFIG_DIR"])
	}
	for _, key := range []string{
		"CLAUDECODE", "ANTHROPIC_API_HOST", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"CLAUDE_API_KEY", "CLAUDE_CODE_API_BASE_URL", "CLAUDE_CODE_DISABLE_CLAUDE_MDS", "CLAUDE_CODE_EFFORT_LEVEL",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_USE_VERTEX",
	} {
		if _, ok := claudeEnv[key]; ok {
			t.Fatalf("%s must be removed", key)
		}
	}
	if claudeEnv["CODEX_HOME"] != "/old-codex" {
		t.Fatal("Claude selection must not alter CODEX_HOME")
	}

	codexEnv := envMap(codex.EnvForHome(base, "/new-codex"))
	if codexEnv["CODEX_HOME"] != "/new-codex" {
		t.Fatalf("CODEX_HOME = %q", codexEnv["CODEX_HOME"])
	}
	for _, key := range []string{"CODEX_ACCESS_TOKEN", "CODEX_API_KEY", "CODEX_SQLITE_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		if _, ok := codexEnv[key]; ok {
			t.Fatalf("%s must be removed", key)
		}
	}
	if codexEnv["CLAUDE_CONFIG_DIR"] != "/old-claude" {
		t.Fatal("Codex selection must not alter CLAUDE_CONFIG_DIR")
	}
}

func TestDefaultPromptEnvironmentsDropInheritedAccount(t *testing.T) {
	base := []string{"CLAUDE_CONFIG_DIR=/inherited-claude", "CODEX_HOME=/inherited-codex"}
	if _, ok := envMap(claude.EnvForConfigDir(base, ""))["CLAUDE_CONFIG_DIR"]; ok {
		t.Fatal("empty Claude target preserved inherited config dir")
	}
	if _, ok := envMap(codex.EnvForHome(base, ""))["CODEX_HOME"]; ok {
		t.Fatal("empty Codex target preserved inherited home")
	}
}

func TestExecDelegatedPreservesProcessContract(t *testing.T) {
	cmd := osexec.Command(os.Args[0], "-test.run=^TestExecDelegatedHelper$")
	cmd.Env = append(os.Environ(), "QUOTA_DELEGATE_HELPER=1", "DELEGATE_MARKER=marker")
	cmd.Stdin = strings.NewReader("input-line\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitErr, ok := err.(*osexec.ExitError)
	if !ok || exitErr.ExitCode() != 23 {
		t.Fatalf("exit = %v, want 23", err)
	}
	if got, want := stdout.String(), "out:input-line:forwarded-value\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "err:marker\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestExecDelegatedHelper(t *testing.T) {
	if os.Getenv("QUOTA_DELEGATE_HELPER") != "1" {
		return
	}
	const script = `read line; printf 'out:%s:%s\n' "$line" "$1"; printf 'err:%s\n' "$DELEGATE_MARKER" >&2; exit 23`
	if err := execDelegated("/bin/sh", []string{"-c", script, "delegated-test"}, []string{"forwarded-value"}, os.Environ()); err != nil {
		t.Fatal(err)
	}
}

func TestSelectClaudeAccountUsesSharedCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 10% used - resets in 4h\nCurrent week (all models): 80% used - resets in 6d"
	extraRaw := "Current session: 10% used - resets in 4h\nCurrent week (all models): 20% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), extraRaw, validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	selected, err := selectClaudeAccount(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "claude-2" {
		t.Fatalf("selected account = %q, want claude-2", selected.Key)
	}
}

func TestSelectClaudeAccountScoresSurplusOverConfiguredFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 10% used - resets in 4h\nCurrent week (all models): 10% used - resets in 1d"
	extraRaw := "Current session: 50% used - resets in 4h\nCurrent week (all models): 50% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), extraRaw, validUntil)

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}},
		ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
			"claude":   {MinLeftPct: floatPtr(80)},
			"claude-2": {MinLeftPct: floatPtr(5)},
		}},
	}
	selected, err := selectClaudeAccount(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "claude-2" {
		t.Fatalf("selected account = %q, want claude-2", selected.Key)
	}
}

func TestSelectClaudeAccountRejectsWindowBelowConfiguredFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	raw := "Current session: 70% used - resets in 4h\nCurrent week (all models): 20% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), raw, validUntil)

	cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
		"claude": {MinLeftPct: floatPtr(40)},
	}}}
	if _, err := selectClaudeAccount(cfg, nil, time.Now()); err == nil {
		t.Fatal("selection should fail when a configured floor is above an applicable window")
	}
}

func TestSelectClaudeAccountSkipsBelowPromptFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 10% used - resets in 4h\nCurrent week (all models): 99% used - resets in 1m"
	extraRaw := "Current session: 10% used - resets in 4h\nCurrent week (all models): 50% used - resets in 6d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), extraRaw, validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	selected, err := selectClaudeAccount(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "claude-2" {
		t.Fatalf("selected account = %q, want claude-2", selected.Key)
	}
}

func TestSelectClaudeAccountFailsWhenAllAccountsBelowPromptFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 99% used - resets in 1m\nCurrent week (all models): 99% used - resets in 1m"
	extraRaw := "Current session: 98% used - resets in 2m\nCurrent week (all models): 98% used - resets in 2m"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), extraRaw, validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	if _, err := selectClaudeAccount(cfg, nil, time.Now()); err == nil {
		t.Fatal("selection should fail when every Claude account is below the prompt floor")
	}
}

func TestSelectClaudeAccountRejectsInvalidExecPromptSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]config.ExecPromptAccountSettings
	}{
		{"unknown claude account", map[string]config.ExecPromptAccountSettings{
			"claude-3": {MinLeftPct: floatPtr(5)},
		}},
		{"bad key", map[string]config.ExecPromptAccountSettings{
			"cluade": {MinLeftPct: floatPtr(5)},
		}},
		{"floor below range", map[string]config.ExecPromptAccountSettings{
			"claude": {MinLeftPct: floatPtr(-1)},
		}},
		{"floor above range", map[string]config.ExecPromptAccountSettings{
			"claude": {MinLeftPct: floatPtr(101)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: tt.settings}}
			if _, err := selectClaudeAccount(cfg, nil, time.Now()); err == nil {
				t.Fatal("expected invalid execPrompt.accountSettings to fail")
			}
		})
	}
}

func TestSelectClaudeAccountFailsOnInvalidConfiguredAccount(t *testing.T) {
	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "work", ConfigDir: "~/.claude-work"}}}

	_, err := selectClaudeAccount(cfg, nil, time.Now())
	if err == nil {
		t.Fatal("invalid Claude account config should fail before delegation")
	}
	if !strings.Contains(err.Error(), "invalid Claude account config") {
		t.Fatalf("error = %q", err)
	}
}

func TestSelectClaudeAccountModelFailureMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	raw := "Current session: 99% used - resets in 1m\nCurrent week (all models): 99% used - resets in 1m\nCurrent week (Fable): 99% used - resets in 1m"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), raw, validUntil)

	_, err := selectClaudeAccount(config.Config{}, []string{"--model", "fable"}, time.Now())
	if err == nil {
		t.Fatal("selection should fail when requested model quota is not usable")
	}
	if !strings.Contains(err.Error(), "no account has usable Claude quota for --model fable") {
		t.Fatalf("error = %q", err)
	}
}

func TestSelectClaudeAccountUsesModelWindowWhenEveryUsableAccountHasIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 20% used - resets in 4h\nCurrent week (all models): 10% used - resets in 1d\nCurrent week (Fable): 80% used - resets in 1d"
	extraRaw := "Current session: 20% used - resets in 4h\nCurrent week (all models): 80% used - resets in 1d\nCurrent week (Fable): 10% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), extraRaw, validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	selected, err := selectClaudeAccount(cfg, []string{"--model", "fable"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "claude-2" {
		t.Fatalf("selected account = %q, want claude-2", selected.Key)
	}
}

func TestSelectClaudeAccountModelWindowMixIsOrderIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 20% used - resets in 4h\nCurrent week (all models): 90% used - resets in 6d\nCurrent week (Fable): 10% used - resets in 1d"
	bestRaw := "Current session: 20% used - resets in 4h\nCurrent week (all models): 10% used - resets in 1d\nCurrent week (Fable): 20% used - resets in 1d"
	noModelRaw := "Current session: 20% used - resets in 4h\nCurrent week (all models): 50% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), bestRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-3")), noModelRaw, validUntil)

	firstOrder := config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
		{Key: "claude-3", ConfigDir: "~/.claude-3"},
	}}
	secondOrder := config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Key: "claude-3", ConfigDir: "~/.claude-3"},
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
	}}
	for _, cfg := range []config.Config{firstOrder, secondOrder} {
		selected, err := selectClaudeAccount(cfg, []string{"--model", "fable"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if selected.Key != "claude-2" {
			t.Fatalf("selected account = %q, want claude-2", selected.Key)
		}
	}
}

func TestSelectClaudeAccountResetUnknownMixIsOrderIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	defaultRaw := "Current session: 20% used\nCurrent week (all models): 20% used - resets in 6d"
	unknownRaw := "Current session: 20% used\nCurrent week (all models): 20% used"
	soonerRaw := "Current session: 20% used\nCurrent week (all models): 20% used - resets in 1d"
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), unknownRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-3")), soonerRaw, validUntil)

	firstOrder := config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
		{Key: "claude-3", ConfigDir: "~/.claude-3"},
	}}
	secondOrder := config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Key: "claude-3", ConfigDir: "~/.claude-3"},
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
	}}
	for _, cfg := range []config.Config{firstOrder, secondOrder} {
		selected, err := selectClaudeAccount(cfg, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if selected.Key != "claude" {
			t.Fatalf("selected account = %q, want claude", selected.Key)
		}
	}
}

func TestSelectCodexAccountUsesSharedCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")

	defaultRaw := `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":80,"windowDurationMins":10080}}}`
	extraRaw := `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080}}}`
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), extraRaw, validUntil)

	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}}
	selected, err := selectCodexAccount(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "codex-2" {
		t.Fatalf("selected account = %q, want codex-2", selected.Key)
	}
}

func TestSelectCodexAccountScoresSurplusOverConfiguredFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")

	defaultRaw := `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`
	extraRaw := `{"rateLimits":{"primary":{"usedPercent":50,"windowDurationMins":300},"secondary":{"usedPercent":50,"windowDurationMins":10080}}}`
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), extraRaw, validUntil)

	cfg := config.Config{
		CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}},
		ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
			"codex":   {MinLeftPct: floatPtr(80)},
			"codex-2": {MinLeftPct: floatPtr(5)},
		}},
	}
	selected, err := selectCodexAccount(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "codex-2" {
		t.Fatalf("selected account = %q, want codex-2", selected.Key)
	}
}

func TestSelectCodexAccountRejectsInvalidExecPromptSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]config.ExecPromptAccountSettings
	}{
		{"unknown codex account", map[string]config.ExecPromptAccountSettings{
			"codex-3": {MinLeftPct: floatPtr(5)},
		}},
		{"floor above range", map[string]config.ExecPromptAccountSettings{
			"codex": {MinLeftPct: floatPtr(101)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: tt.settings}}
			if _, err := selectCodexAccount(cfg, time.Now()); err == nil {
				t.Fatal("expected invalid execPrompt.accountSettings to fail")
			}
		})
	}
}

func TestShouldCompareCodexShortestWindowRequiresEveryUsableAccount(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	twoWindows := testQuota(
		testWindow("5h", "5h", 80, now.Add(4*time.Hour), 300),
		testWindow("weekly", "7d", 80, now.Add(6*24*time.Hour), 10080),
	)
	fiveHourOnly := testQuota(
		testWindow("5h", "5h", 80, now.Add(4*time.Hour), 300),
	)
	unusableFiveHourOnly := testQuota(
		testWindow("5h", "5h", 1, now.Add(4*time.Hour), 300),
	)

	if shouldCompareCodexShortestWindow([]quotaProbeResult{{quota: twoWindows}, {quota: fiveHourOnly}}, defaultPromptFloors(2), now) {
		t.Fatal("short window comparison should be disabled when a usable account lacks a distinct short window")
	}
	if !shouldCompareCodexShortestWindow([]quotaProbeResult{{quota: twoWindows}, {quota: twoWindows}}, defaultPromptFloors(2), now) {
		t.Fatal("short window comparison should be enabled when every usable account has one")
	}
	if !shouldCompareCodexShortestWindow([]quotaProbeResult{{quota: twoWindows}, {quota: unusableFiveHourOnly}}, defaultPromptFloors(2), now) {
		t.Fatal("unusable accounts without a distinct short window should not disable short window comparison")
	}
}

func TestSelectCodexAccountRanksWeeklyOnlyByWeeklyQuota(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")

	defaultRaw := `{"rateLimits":{"primary":{"usedPercent":50,"windowDurationMins":300},"secondary":{"usedPercent":50,"windowDurationMins":10080}}}`
	extraRaw := `{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":10080}}}`
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), extraRaw, validUntil)

	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}}
	selected, err := selectCodexAccount(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "codex-2" {
		t.Fatalf("selected account = %q, want codex-2 (higher weekly quota)", selected.Key)
	}
}

func TestSelectCodexAccountSkipsBelowPromptFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")

	defaultRaw := `{"rateLimits":{"primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`
	extraRaw := `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":50,"windowDurationMins":10080}}}`
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), extraRaw, validUntil)

	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}}
	selected, err := selectCodexAccount(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Key != "codex-2" {
		t.Fatalf("selected account = %q, want codex-2", selected.Key)
	}
}

func TestSelectCodexAccountFailsWhenAllAccountsBelowPromptFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")

	defaultRaw := `{"rateLimits":{"primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":99,"windowDurationMins":10080}}}`
	extraRaw := `{"rateLimits":{"primary":{"usedPercent":98,"windowDurationMins":300},"secondary":{"usedPercent":98,"windowDurationMins":10080}}}`
	validUntil := time.Now().Add(time.Hour)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), defaultRaw, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), extraRaw, validUntil)

	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}}
	if _, err := selectCodexAccount(cfg, time.Now()); err == nil {
		t.Fatal("selection should fail when every Codex account is below the prompt floor")
	}
}

func TestSelectCodexAccountFailsOnInvalidConfiguredAccount(t *testing.T) {
	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "work", Home: "~/.codex-work"}}}

	_, err := selectCodexAccount(cfg, time.Now())
	if err == nil {
		t.Fatal("invalid Codex account config should fail before delegation")
	}
	if !strings.Contains(err.Error(), "invalid Codex account config") {
		t.Fatalf("error = %q", err)
	}
}

func TestParseQuotaDuration(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"6d 2h": 6*24*time.Hour + 2*time.Hour,
		"4h 5m": 4*time.Hour + 5*time.Minute,
		"0m":    0,
	} {
		got, ok := parseQuotaDuration(value)
		if !ok || got != want {
			t.Errorf("parseQuotaDuration(%q) = %v/%v, want %v", value, got, ok, want)
		}
	}
	if _, ok := parseQuotaDuration("tomorrow"); ok {
		t.Fatal("invalid duration must be rejected")
	}
}

func TestNumericValueAcceptsIntAndFloat64(t *testing.T) {
	for _, value := range []any{5, float64(5)} {
		got, ok := numericValue(value)
		if !ok || got != 5 {
			t.Fatalf("numericValue(%T(%v)) = %v/%v, want 5/true", value, value, got, ok)
		}
	}
}

func compareAccountScoresForTest(a, b accountScore) int {
	scores := []accountScore{a, b}
	markRateComparable(scores, []bool{true, true})
	return compareAccountScore(scores[0], scores[1])
}

func testQuota(windows ...map[string]any) map[string]any {
	return map[string]any{"windows": windows}
}

func testWindow(key, label string, left float64, resetsAt time.Time, windowMins int) map[string]any {
	window := map[string]any{
		"key":      key,
		"label":    label,
		"left":     left,
		"resetsAt": resetsAt,
	}
	if windowMins > 0 {
		window["windowMins"] = windowMins
	}
	return window
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		key, val, ok := strings.Cut(item, "=")
		if ok {
			out[key] = val
		}
	}
	return out
}

func defaultPromptFloors(n int) []float64 {
	floors := make([]float64, n)
	for i := range floors {
		floors[i] = defaultExecPromptMinLeftPct
	}
	return floors
}

func floatPtr(v float64) *float64 {
	return &v
}

func TestResolvedDefaultAccountsUseQuotaDefaultEnvironment(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, inherited := range []bool{false, true} {
			t.Run(provider+"/"+map[bool]string{false: "unset", true: "inherited"}[inherited], func(t *testing.T) {
				home := autoPromptTestHome(t)
				envKey := "CLAUDE_CONFIG_DIR"
				if provider == "codex" {
					envKey = "CODEX_HOME"
				}
				base := []string{"PATH=/usr/bin"}
				if inherited {
					path := filepath.Join(home, "custom")
					t.Setenv(envKey, path)
					base = append(base, envKey+"="+path)
				}
				var got []string
				if provider == "claude" {
					accounts, errs := (config.Config{}).ResolveAccounts()
					if len(errs) != 0 || len(accounts) != 1 {
						t.Fatalf("resolution %v %v", accounts, errs)
					}
					got = claude.EnvForConfigDir(base, accounts[0].ConfigDir)
					if set := selectAgentClaudeSetEnv(accounts[0].ConfigDir); len(set) != 0 {
						t.Fatalf("default Claude recommendation must not set CLAUDE_CONFIG_DIR: %v", set)
					}
				} else {
					accounts, errs := (config.Config{}).ResolveCodexAccounts()
					if len(errs) != 0 || len(accounts) != 1 {
						t.Fatalf("resolution %v %v", accounts, errs)
					}
					got = codex.EnvForHome(base, accounts[0].Home)
					defaultDir := quotaTestAccountDir(t, filepath.Join(home, ".codex"))
					if set := selectAgentCodexSetEnv(accounts[0].Home); set["CODEX_HOME"] != defaultDir {
						t.Fatalf("default recommendation = %v", set)
					}
				}
				gotMap := envMap(got)
				defaultDir := quotaTestAccountDir(t, filepath.Join(home, "."+provider))
				if gotMap["PATH"] != "/usr/bin" {
					t.Fatalf("default account environment lost PATH: %v", got)
				}
				if provider == "claude" {
					if _, ok := gotMap[envKey]; ok {
						t.Fatalf("default Claude environment must not set %s: %v", envKey, got)
					}
				} else if gotMap[envKey] != defaultDir {
					t.Fatalf("default Codex environment = %v", got)
				}
			})
		}
	}
}
