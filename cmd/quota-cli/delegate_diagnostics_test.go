package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/config"
)

func TestQuotaProbeFailureRetainsWindowReasons(t *testing.T) {
	reason := `could not parse claude quota from /usage output: claude quota row "Session" has an unreadable percentage; claude quota row "Week" has an unreadable percentage; claude quota row "Fable" has an out-of-range percentage`
	result := quotaProbeResult{err: errors.New(reason + "\n--- output ---\nPRIVATE RAW OUTPUT")}
	text := quotaSelectionFailure("claude", "claude", "fable", result, 5)
	if !strings.Contains(text, reason) || !strings.Contains(text, "quota lookup failed:") || strings.Contains(text, "PRIVATE RAW OUTPUT") {
		t.Fatalf("diagnostics lost window reasons or retained raw output: %s", text)
	}
}

func TestClaudeWeeklyModelAdmissionThroughParsers(t *testing.T) {
	for _, model := range []string{"claude-fable-5-1", "fable", "claude-opus-4-8", "claude-sonnet-4-6"} {
		for _, tc := range []struct {
			name          string
			weekly, fable string
			allowGeneral  bool
			allowFable    bool
		}{
			{"fable exhausted", "10% used", "100% used", true, false},
			{"aggregate exhausted", "100% used", "10% used", false, false},
			{"both at floor", "95% used", "95% used", true, true},
			{"fable below floor", "10% used", "96% used", true, false},
			{"fable unreadable", "10% used", "unavailable", true, false},
			{"fable out of range", "10% used", "101% used", true, false},
			{"invalid duplicate fable", "10% used", "10% used\nCurrent week (Fable): unavailable", true, true},
			{"valid duplicate fable", "10% used", "unavailable\nCurrent week (Fable): 10% used", true, false},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				home := autoPromptTestHome(t)
				raw := fmt.Sprintf("Current session: 75%% used\nCurrent week (all models): %s\nCurrent week (Fable): %s", tc.weekly, tc.fable)
				putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), raw)
				putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(0, 0))
				allow := tc.allowGeneral
				if strings.Contains(model, "fable") {
					allow = tc.allowFable
				}
				_, err := selectClaudeAccount(context.Background(), config.Config{}, []string{"--model", model})
				if (err == nil) != allow {
					t.Errorf("direct selection error=%v, want allowed=%t", err, allow)
				}
				result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: "claude", model: model})
				if (err == nil) != allow || (result.Selected != nil) != allow {
					t.Errorf("select-agent error=%v selected=%v, want allowed=%t", err, result.Selected, allow)
				}
				opts := autoPromptTestOptions()
				opts.models[0].model = model
				catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
				_, err = selectAutoPromptAccount(context.Background(), config.Config{}, opts, catalogs)
				if (err == nil) != allow {
					t.Errorf("automatic selection error=%v, want allowed=%t", err, allow)
				}
				if !allow && err != nil && !strings.Contains(err.Error(), "Fable") {
					t.Errorf("missing model-window evidence: %v", err)
				}
			})
		}
	}
}

func TestClaudeWeeklyModelUsesConfiguredFloor(t *testing.T) {
	for _, tc := range []struct {
		weekly, fable int
		model         string
		allow         bool
	}{
		{40, 40, "fable", true}, {40, 39, "fable", false},
		{39, 40, "fable", false}, {40, 0, "opus", true},
	} {
		t.Run(fmt.Sprintf("%s/week%d/fable%d", tc.model, tc.weekly, tc.fable), func(t *testing.T) {
			home := autoPromptTestHome(t)
			raw := fmt.Sprintf("Current session: 0%% used\nCurrent week (all models): %d%% used\nCurrent week (Fable): %d%% used", 100-tc.weekly, 100-tc.fable)
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), raw)
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
				"claude": {MinLeftPct: floatPtr(40)},
			}}}
			_, err := selectClaudeAccount(context.Background(), cfg, []string{"--model", tc.model})
			if (err == nil) != tc.allow {
				t.Fatalf("selection error=%v, want allowed=%t", err, tc.allow)
			}
			if !tc.allow && !strings.Contains(err.Error(), "left=39%; required>=40%; insufficient") {
				t.Fatalf("missing configured-floor comparison: %v", err)
			}
		})
	}
}

func TestClaudeSelectionUsesModelOptionOutsideOtherOptionValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		allow bool
	}{
		{"prompt text", []string{"--model", "opus", "--append-system-prompt", "--model=sonnet", "--", "test"}, false},
		{"file path", []string{"--model=opus", "--system-prompt-file", "--model=sonnet", "test"}, false},
		{"short name", []string{"--model=opus", "-n", "--model=sonnet", "test"}, false},
		{"available model", []string{"--model", "sonnet", "--append-system-prompt", "--model=opus", "test"}, true},
		{"subsequent model", []string{"--model=opus", "--append-system-prompt", "--model=opus", "--model=sonnet", "test"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := autoPromptTestHome(t)
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Opus): 100% used\nCurrent week (Sonnet): 10% used")
			_, err := selectClaudeAccount(context.Background(), config.Config{}, tc.args)
			if (err == nil) != tc.allow {
				t.Fatalf("selection error=%v, want allowed=%t", err, tc.allow)
			}
			if !tc.allow && (!strings.Contains(err.Error(), "model=opus") || !strings.Contains(err.Error(), "Opus [extra_1]: left=0%; required>=5%; insufficient")) {
				t.Fatalf("missing requested model quota evidence: %v", err)
			}
		})
	}
}

func TestExecPromptQuotaFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, raw string
		floor                      float64
		want                       []string
		absent                     string
	}{
		{"five hour floor", "claude", "claude-fable-5-1", "Current session: 80% used - resets in 4h\nCurrent week (all models): 10% used\nCurrent week (Fable): 20% used", 5,
			[]string{"model=claude-fable-5-1", "Session [session]: left=20%; required>=25%", "max(minLeftPct=5%, 5-hour minimum=25%)", "insufficient; resets in 4h", "Week [weekly_all]: left=90%; required>=5%; pass", "Fable [extra_1]: left=80%; required>=5%; pass"}, ""},
		{"custom floor", "claude", "fable", "Current session: 70% used\nCurrent week (all models): 10% used\nCurrent week (Fable): 20% used", 40,
			[]string{"minLeftPct=40%", "Session [session]: left=30%; required>=40%", "insufficient"}, ""},
		{"fable only exhausted", "claude", "fable", "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Fable): 100% used", 5,
			[]string{"Week [weekly_all]: left=90%; required>=5%; pass", "Fable [extra_1]: left=0%; required>=5%; insufficient"}, ""},
		{"general ignores fable", "claude", "claude-opus-4-8", "Current session: 10% used\nCurrent week (all models): 100% used\nCurrent week (Fable): 100% used", 5,
			[]string{"Week [weekly_all]: left=0%; required>=5%; insufficient", "Fable [extra_1]: left=0%; not applicable to requested model"}, "Fable [extra_1]: left=0%; required"},
		{"unreadable aggregate", "claude", "fable", "Current session: unavailable\nCurrent week (all models): 10% used", 5,
			[]string{"incomplete quota window data", "unreadable percentage", "Week [weekly_all]: left=90%"}, "insufficient"},
		{"unreadable fable", "claude", "fable", "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Fable): unavailable", 5,
			[]string{"Fable", "unreadable percentage"}, "insufficient"},
		{"model window only", "claude", "fable", "Current week (Fable): 10% used", 5,
			[]string{"Fable [extra_1]: left=90%; required>=5%; pass", "rejected: missing aggregate quota window (session or weekly_all)"}, "insufficient"},
		{"codex weekly", "codex", "", autoPromptCodexQuota(2, 30), 5,
			[]string{"codex: minLeftPct=5%", "left=2%; required>=5%; insufficient", "left=30%; required>=25%"}, ""},
		{"probe unavailable", "codex", "", "", 5,
			[]string{"codex: minLeftPct=5%", "quota lookup failed:"}, "insufficient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := autoPromptTestHome(t)
			if tc.raw != "" {
				putAutoPromptQuota(t, tc.provider, filepath.Join(home, "."+tc.provider), tc.raw)
			}
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
				tc.provider: {MinLeftPct: &tc.floor},
			}}}
			if err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			args := []string{"--agent=" + tc.provider}
			if tc.model != "" {
				args = append(args, "--model", tc.model)
			}
			args = append(args, "private prompt must not appear in diagnostics")
			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestExecPromptCacheHelper$")
			cmd.Env = append(os.Environ(), "QUOTA_CACHE_HELPER_ARGS="+string(encoded))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || stdout.Len() != 0 {
				t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			text := stderr.String()
			for _, want := range append(tc.want, "quota checks (cache max age 1m15s):") {
				if !strings.Contains(text, want) {
					t.Errorf("stderr missing %q:\n%s", want, text)
				}
			}
			for _, absent := range []string{tc.absent, args[len(args)-1]} {
				if absent != "" && strings.Contains(text, absent) {
					t.Errorf("stderr unexpectedly contains %q:\n%s", absent, text)
				}
			}
		})
	}
}

func TestQuotaFailureListsEveryAccount(t *testing.T) {
	home := autoPromptTestHome(t)
	putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current session: 80% used\nCurrent week (all models): 10% used")
	putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude-2"), "Current week (all models): 96% used\nCurrent week (Fable): 10% used")
	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}}}
	_, err := selectClaudeAccount(context.Background(), cfg, []string{"--model", "fable"})
	if err == nil {
		t.Fatal("expected quota rejection")
	}
	for _, want := range []string{"claude: minLeftPct=5%", "claude-2: minLeftPct=5%", "left=20%; required>=25%", "left=4%; required>=5%"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q: %v", want, err)
		}
	}
}
