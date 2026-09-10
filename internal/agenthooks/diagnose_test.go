package agenthooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRawFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONRoot(t *testing.T, path string, root map[string]any) {
	t.Helper()
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	writeRawFile(t, path, string(b))
}

func hasReason(reasons []string, substr string) bool {
	for _, r := range reasons {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func managedBashHooks(runtime, binary, policyDir string, entry map[string]any) map[string]any {
	entry["type"] = "command"
	entry["command"] = HookCommand(runtime, binary, policyDir)
	return map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{entry}}},
	}
}

func TestDetectClaudeMalformedSettingsSetsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeRawFile(t, ClaudeSettingsPath(), `{ this is not json`)
	plan := Detect("claude", "/bin/quota-cli", "")
	if plan.Error == "" {
		t.Fatal("malformed settings.json must surface as error")
	}
	if plan.Present {
		t.Fatal("malformed settings.json must not report present")
	}
}

func TestDetectCodexMalformedHooksSetsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	writeRawFile(t, CodexHooksPath(), `{ nope`)
	plan := Detect("codex", "/bin/quota-cli", "")
	if plan.Error == "" {
		t.Fatal("malformed hooks.json must surface as error")
	}
}

func TestDetectCodexMalformedConfigSetsErrorWithPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	if _, err := Apply("codex", "/bin/quota-cli", ""); err != nil {
		t.Fatal(err)
	}
	writeRawFile(t, codexConfigPath(), "features = [ oops")
	plan := Detect("codex", "/bin/quota-cli", "")
	if !plan.Present {
		t.Fatal("installed hooks.json entry should still be present")
	}
	if plan.Error == "" {
		t.Fatal("malformed config.toml must surface as error")
	}
}

func TestDetectClaudeDisableSettingsBlock(t *testing.T) {
	cases := []struct {
		key string
	}{
		{"disableAllHooks"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			root := map[string]any{
				tc.key:  true,
				"hooks": managedBashHooks("claude", "/bin/quota-cli", "", map[string]any{}),
			}
			writeJSONRoot(t, ClaudeSettingsPath(), root)
			plan := Detect("claude", "/bin/quota-cli", "")
			if !plan.Present {
				t.Fatalf("managed entry should be present: %+v", plan)
			}
			if !hasReason(plan.Reasons, tc.key) {
				t.Fatalf("reasons=%v want mention of %s", plan.Reasons, tc.key)
			}
		})
	}
}

func TestDetectClaudeManagedVariants(t *testing.T) {
	cases := []struct {
		name       string
		extra      map[string]any
		wantReason bool
	}{
		{"asyncRewake true", map[string]any{"asyncRewake": true}, true},
		{"asyncRewake false", map[string]any{"asyncRewake": false}, false},
		{"shell powershell", map[string]any{"shell": "powershell"}, true},
		{"shell bash", map[string]any{"shell": "bash"}, false},
		{"async true", map[string]any{"async": true}, true},
		{"async false is default", map[string]any{"async": false}, false},
		{"if condition", map[string]any{"if": "tool == 'Bash'"}, true},
		{"if empty unsupported", map[string]any{"if": ""}, true},
		{"args present", map[string]any{"args": []any{"--extra"}}, true},
		{"args empty changes execution", map[string]any{"args": []any{}}, true},
		{"async null invalid", map[string]any{"async": nil}, true},
		{"async zero invalid", map[string]any{"async": 0}, true},
		{"generated statusMessage", map[string]any{"statusMessage": hookStatusMessage}, false},
		{"unrelated field", map[string]any{"description": "note"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			root := map[string]any{"hooks": managedBashHooks("claude", "/bin/quota-cli", "", tc.extra)}
			writeJSONRoot(t, ClaudeSettingsPath(), root)
			plan := Detect("claude", "/bin/quota-cli", "")
			if !plan.Present {
				t.Fatalf("managed entry should be present: %+v", plan)
			}
			if got := len(plan.Reasons) > 0; got != tc.wantReason {
				t.Fatalf("reasons=%v want reason=%v", plan.Reasons, tc.wantReason)
			}
		})
	}
}

func TestDetectClaudeIgnoresUnrelatedEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	managed := map[string]any{"type": "command", "command": HookCommand("claude", "/bin/quota-cli", "")}
	unrelated := map[string]any{"type": "command", "command": "echo hi", "async": true, "if": "always"}
	root := map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{unrelated, managed}}},
	}}
	writeJSONRoot(t, ClaudeSettingsPath(), root)
	plan := Detect("claude", "/bin/quota-cli", "")
	if !plan.Present {
		t.Fatalf("managed entry should be present: %+v", plan)
	}
	if len(plan.Reasons) > 0 {
		t.Fatalf("unrelated entry fields must not be flagged: %v", plan.Reasons)
	}
}

func TestDetectCodexConfigBlockers(t *testing.T) {
	cases := []struct {
		name       string
		toml       string
		wantReason bool
	}{
		{"features.hooks false", "[features]\nhooks = false\n", true},
		{"features.codex_hooks false legacy", "[features]\ncodex_hooks = false\n", true},
		{"canonical hooks enabled wins", "[features]\nhooks = true\ncodex_hooks = false\n", false},
		{"inactive alias still requires boolean", "[features]\nhooks = true\ncodex_hooks = \"bad\"\n", true},
		{"canonical hooks disabled wins", "[features]\nhooks = false\ncodex_hooks = true\n", true},
		{"features.hooks type invalid", "[features]\nhooks = \"yes\"\n", true},
		{"managed-only key ignored in user config", "allow_managed_hooks_only = true\n", false},
		{"features.hooks true", "[features]\nhooks = true\n", false},
		{"unrelated setting", "model = \"example\"\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", "")
			if _, err := Apply("codex", "/bin/quota-cli", ""); err != nil {
				t.Fatal(err)
			}
			writeRawFile(t, codexConfigPath(), tc.toml)
			plan := Detect("codex", "/bin/quota-cli", "")
			if plan.Error != "" {
				t.Fatalf("unexpected error for %q: %s", tc.toml, plan.Error)
			}
			if got := len(plan.Reasons) > 0; got != tc.wantReason {
				t.Fatalf("toml=%q reasons=%v want reason=%v", tc.toml, plan.Reasons, tc.wantReason)
			}
		})
	}
}

func TestApplyThenDetectCleanInstall(t *testing.T) {
	for _, runtime := range []string{"claude", "codex"} {
		t.Run(runtime, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CODEX_HOME", "")
			policyDir := filepath.Join(home, "policies")
			plan, err := Apply(runtime, "/bin/quota-cli", policyDir)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Present || len(plan.Reasons) > 0 || plan.Error != "" {
				t.Fatalf("apply plan = %+v", plan)
			}
			d := Detect(runtime, "/bin/quota-cli", policyDir)
			if !d.Present {
				t.Fatalf("detect should preserve present after apply: %+v", d)
			}
			if len(d.Reasons) > 0 || d.Error != "" {
				t.Fatalf("detect plan = %+v", d)
			}
		})
	}
}

func TestApplyClaudeReportsBlockerPreservesUserSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeJSONRoot(t, ClaudeSettingsPath(), map[string]any{"disableAllHooks": true})
	plan, err := Apply("claude", "/bin/quota-cli", "")
	if err == nil {
		t.Fatal("expected diagnostic failure due to blocker")
	}
	if !strings.Contains(err.Error(), "saved") {
		t.Fatalf("error should report the save: %v", err)
	}
	if !plan.Present {
		t.Fatalf("hook should be saved and present: %+v", plan)
	}
	if !hasReason(plan.Reasons, "disableAllHooks") {
		t.Fatalf("reasons=%v want disableAllHooks", plan.Reasons)
	}
	saved, err := ReadJSONObject(ClaudeSettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved["disableAllHooks"] != true {
		t.Fatalf("apply changed the user's disableAllHooks setting: %+v", saved["disableAllHooks"])
	}
}

func TestDetectExaminesEveryManagedEntry(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	command := HookCommand("claude", "/bin/quota-cli", "")
	root := map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "Bash", "hooks": []any{
			map[string]any{"type": "command", "command": command},
			map[string]any{"type": "command", "command": command, "args": []any{}},
		}},
	}}}
	writeJSONRoot(t, ClaudeSettingsPath(), root)
	plan := Detect("claude", "/bin/quota-cli", "")
	if !plan.Present || !hasReason(plan.Reasons, "args") {
		t.Fatalf("later managed entry was not diagnosed: %+v", plan)
	}
}

func TestDetectIgnoresManagedOnlyKeyInClaudeUserSettings(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	writeJSONRoot(t, ClaudeSettingsPath(), map[string]any{"allowManagedHooksOnly": true})
	plan, err := Apply("claude", "/bin/quota-cli", "")
	if err != nil || !plan.Present || len(plan.Reasons) != 0 {
		t.Fatalf("user setting must not block installation: %+v err=%v", plan, err)
	}
}
