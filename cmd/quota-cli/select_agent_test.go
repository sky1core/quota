package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestParseSelectAgentArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     selectAgentOptions
		wantErr  string
		wantHelp bool
	}{
		{name: "defaults", want: selectAgentOptions{agent: selectAgentAll}},
		{name: "json", args: []string{"--json"}, want: selectAgentOptions{agent: selectAgentAll, jsonOut: true}},
		{name: "claude model", args: []string{"--agent=claude", "--model", "Fable"}, want: selectAgentOptions{agent: selectAgentClaude, model: "fable"}},
		{name: "codex", args: []string{"--agent", "codex"}, want: selectAgentOptions{agent: selectAgentCodex}},
		{name: "bad agent", args: []string{"--agent=other"}, wantErr: "--agent must be all, claude, or codex"},
		{name: "model in all mode", args: []string{"--model=fable"}, wantErr: "--model is only valid with --agent=claude"},
		{name: "positional", args: []string{"prompt"}, wantErr: `unexpected argument: "prompt"`},
		{name: "help", args: []string{"-h"}, wantHelp: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSelectAgentArgs(tt.args, &strings.Builder{})
			if tt.wantHelp {
				if err != flag.ErrHelp {
					t.Fatalf("error = %v, want flag.ErrHelp", err)
				}
				return
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSelectAgentSelectsAcrossProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current session: 40% used - resets in 4h\nCurrent week (all models): 50% used - resets in 3d", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), "Current session: 40% used - resets in 4h\nCurrent week (all models): 40% used - resets in 3d", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), `{"rateLimits":{"primary":{"usedPercent":40,"windowDurationMins":300},"secondary":{"usedPercent":50,"windowDurationMins":10080}}}`, validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`, validUntil)

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}},
		CodexAccounts:  []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}},
	}
	result, err := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil {
		t.Fatal("selected is nil")
	}
	if result.Selected.Provider != selectAgentCodex || result.Selected.Key != "codex-2" {
		t.Fatalf("selected = %+v, want codex-2", result.Selected)
	}
	if got, want := result.Selected.Command, []string{"codex", "exec"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %v, want %v", got, want)
	}
	if got, want := result.Selected.SetEnv["CODEX_HOME"], quotaTestAccountDir(t, filepath.Join(home, ".codex-2")); got != want {
		t.Fatalf("CODEX_HOME = %q, want %q", got, want)
	}
	if len(result.Candidates) != 4 {
		t.Fatalf("candidates = %d, want 4", len(result.Candidates))
	}
}

func TestSelectAgentDefaultDoesNotUseClaudeModelRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current session: 10% used - resets in 4h\nCurrent week (all models): 90% used - resets in 1d\nCurrent week (Fable): 0% used - resets in 1d", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), `{"rateLimits":{"primary":{"usedPercent":40,"windowDurationMins":300},"secondary":{"usedPercent":40,"windowDurationMins":10080}}}`, validUntil)

	result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil || result.Selected.Key != "codex" {
		t.Fatalf("selected = %+v, want codex", result.Selected)
	}
	for _, candidate := range result.Candidates {
		if candidate.Key != "claude" {
			continue
		}
		for _, window := range candidate.Windows {
			if strings.Contains(strings.ToLower(window.Label), "fable") {
				t.Fatalf("default select-agent must not report model-specific rows: %+v", candidate.Windows)
			}
		}
	}
}

func TestSelectAgentAllModeComparesSharedFiveHours(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current session: 10% used - resets in 4h", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), `{"rateLimits":{"primary":{"usedPercent":70,"windowDurationMins":300},"secondary":{"usedPercent":70,"windowDurationMins":10080}}}`, validUntil)

	result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil || result.Selected.Key != "claude" {
		t.Fatalf("selected = %+v, want claude with 90%% left in shared 5h window", result.Selected)
	}
}

func TestSelectAgentClaudeModelUsesRequestedModelRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current session: 20% used - resets in 4h\nCurrent week (all models): 10% used - resets in 1d\nCurrent week (Fable): 80% used - resets in 1d", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude-2")), "Current session: 20% used - resets in 4h\nCurrent week (all models): 80% used - resets in 1d\nCurrent week (Fable): 10% used - resets in 1d", validUntil)

	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	result, err := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentClaude, model: "fable"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil || result.Selected.Key != "claude-2" {
		t.Fatalf("selected = %+v, want claude-2", result.Selected)
	}
}

func TestSelectAgentReturnsCandidatesWhenNoUsableAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude")), "Current session: 99% used - resets in 1m\nCurrent week (all models): 99% used - resets in 1m", validUntil)
	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), `{"rateLimits":{"primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":99,"windowDurationMins":10080}}}`, validUntil)

	result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err == nil {
		t.Fatal("expected no usable account error")
	}
	if result.Selected != nil {
		t.Fatalf("selected = %+v, want nil", result.Selected)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(result.Candidates))
	}
	for _, candidate := range result.Candidates {
		if candidate.Status != selectAgentStatusSkipped {
			t.Fatalf("candidate status = %q, want skipped", candidate.Status)
		}
		if candidate.Reason == "" {
			t.Fatalf("candidate reason is empty: %+v", candidate)
		}
	}
}

func TestSelectAgentConfigErrorKeepsEmptyCandidatesArray(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "bad", ConfigDir: "/tmp/claude"}}}

	result, err := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentClaude}, now)
	if err == nil {
		t.Fatal("expected config error")
	}
	if result.Candidates == nil {
		t.Fatal("candidates must be an empty array, not nil")
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %d, want 0", len(result.Candidates))
	}
}

func TestSelectAgentKeepsPartialProbeFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	validUntil := time.Now().Add(time.Hour)

	quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex")), `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":10,"windowDurationMins":10080}}}`, validUntil)

	result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil || result.Selected.Key != "codex" {
		t.Fatalf("selected = %+v, want codex", result.Selected)
	}
	var sawClaudeError bool
	for _, candidate := range result.Candidates {
		if candidate.Key == "claude" && candidate.Status == selectAgentStatusError {
			sawClaudeError = true
		}
	}
	if !sawClaudeError {
		t.Fatalf("candidates = %+v, want claude probe error preserved", result.Candidates)
	}
}

func TestRunSelectAgentJSONConfigLoadErrorKeepsJSONShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runSelectAgentWithIO(context.Background(), []string{"--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var result selectAgentResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout must be JSON: %v\n%s", err, stdout.String())
	}
	if result.Error == "" {
		t.Fatalf("JSON error is empty: %+v", result)
	}
	if result.Candidates == nil {
		t.Fatal("candidates must be an empty array, not nil")
	}
}

func TestSelectAgentErrorSummaryDropsProviderRawOutput(t *testing.T) {
	err := errors.New("could not find quota rows\n--- output ---\nRAW PROVIDER DETAIL\nSECOND RAW LINE")
	got := selectAgentErrorSummary(err)
	if strings.Contains(got, "\n") {
		t.Fatalf("error summary must be one line: %q", got)
	}
	for _, leaked := range []string{"RAW PROVIDER DETAIL", "SECOND RAW LINE"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("error summary leaked raw provider output %q: %q", leaked, got)
		}
	}
	if got != "could not find quota rows" {
		t.Fatalf("summary = %q", got)
	}
}

func TestSelectAgentReportsUnsetEnvForSelectedProviderOverrides(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "secret")
	claudeKeys := []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_FUTURE_SETTING", "MAX_THINKING_TOKENS"}
	codexKeys := []string{"CODEX_FUTURE_SETTING", "OPENAI_FUTURE_SETTING", "CODEX_SESSIONS_DIR"}
	for _, key := range append(claudeKeys, codexKeys...) {
		t.Setenv(key, "caller-value")
	}
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	t.Setenv("CODEX_CA_CERTIFICATE", "/cert.pem")

	claudeUnset := selectAgentClaudeUnsetEnv("/tmp/claude-2")
	if !stringSliceContains(claudeUnset, "ANTHROPIC_API_KEY") || !stringSliceContains(claudeUnset, "CLAUDECODE") {
		t.Fatalf("claude unset env = %v", claudeUnset)
	}
	if stringSliceContains(claudeUnset, "OPENAI_API_KEY") {
		t.Fatalf("claude unset env must not include Codex/OpenAI keys: %v", claudeUnset)
	}
	codexUnset := selectAgentCodexUnsetEnv("/tmp/codex-2")
	if !stringSliceContains(codexUnset, "OPENAI_API_KEY") {
		t.Fatalf("codex unset env = %v", codexUnset)
	}
	if stringSliceContains(codexUnset, "ANTHROPIC_API_KEY") {
		t.Fatalf("codex unset env must not include Claude/Anthropic keys: %v", codexUnset)
	}
	for _, key := range claudeKeys {
		if !stringSliceContains(claudeUnset, key) {
			t.Errorf("Claude recommendation did not remove %s", key)
		}
	}
	for _, key := range codexKeys {
		if !stringSliceContains(codexUnset, key) {
			t.Errorf("Codex recommendation did not remove %s", key)
		}
	}
	if stringSliceContains(claudeUnset, "HTTPS_PROXY") || stringSliceContains(codexUnset, "HTTPS_PROXY") ||
		stringSliceContains(codexUnset, "CODEX_CA_CERTIFICATE") {
		t.Fatal("network settings must not be recommended for removal")
	}
}

func TestFormatSelectAgentResultShowsSelectedRunPrefix(t *testing.T) {
	generated := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	result := selectAgentResult{
		Selected: &selectAgentCandidate{
			Provider: selectAgentClaude,
			Key:      "claude-2",
			Command:  []string{"claude", "-p"},
			SetEnv:   map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/claude-2"},
			UnsetEnv: []string{"ANTHROPIC_API_KEY"},
		},
		Candidates: []selectAgentCandidate{{
			Provider: selectAgentClaude,
			Key:      "claude-2",
			Status:   selectAgentStatusSelected,
			Windows:  []selectAgentWindow{{Label: "Week", LeftPct: 80, SurplusPct: 75, ResetsIn: "1d", ResetMins: 1440, SurplusPctPerHour: 3.125}},
		}},
		Generated: generated,
	}
	got := formatSelectAgentResult(result)
	for _, want := range []string{
		"Selected: claude-2 (claude)",
		"Run: claude -p",
		"Set env: CLAUDE_CONFIG_DIR=/tmp/claude-2",
		"Unset env: ANTHROPIC_API_KEY",
		"Week 80% left/1d (75% surplus, 3.1%/h)",
		"Generated: 2026-08-31T12:00:00Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func stringSliceContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestSelectAgentCommonQuotaPeriods(t *testing.T) {
	for _, tc := range []struct {
		name, claude, codex, want string
		floor                     float64
	}{
		{"both 5h", "Current session: 10% used", `{"rateLimits":{"primary":{"usedPercent":70,"windowDurationMins":300}}}`, "claude", 5},
		{"shared weekly", "Current session: 70% used\nCurrent week (all models): 10% used", `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":70,"windowDurationMins":10080}}}`, "claude", 5},
		{"no common duration", "Current session: 10% used", `{"rateLimits":{"primary":{"usedPercent":70,"windowDurationMins":600}}}`, "", 5},
		{"monthly is not weekly", "Current week (all models): 10% used", `{"rateLimits":{"primary":{"usedPercent":70,"windowDurationMins":43200}}}`, "", 5},
		{"ineligible does not constrain periods", "Current session: 76% used", `{"rateLimits":{"primary":{"usedPercent":70,"windowDurationMins":600}}}`, "codex", 5},
		{"25 percent included", "Current session: 75% used", `{"rateLimits":{"primary":{"usedPercent":76,"windowDurationMins":300}}}`, "claude", 5},
		{"configured floor retained", "Current session: 61% used", `{"rateLimits":{"primary":{"usedPercent":60,"windowDurationMins":300}}}`, "codex", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := autoPromptTestHome(t)
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), tc.claude)
			putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), tc.codex)
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
				"claude": {MinLeftPct: &tc.floor}, "codex": {MinLeftPct: &tc.floor},
			}}}
			got, err := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentAll}, time.Now())
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "no common quota window duration") || got.Selected != nil || len(got.Candidates) != 2 {
					t.Fatalf("incomparable: %+v %v", got, err)
				}
			} else if err != nil || got.Selected == nil || got.Selected.Key != tc.want {
				t.Fatalf("selected: %+v %v; want %s", got.Selected, err, tc.want)
			}
		})
	}
}
