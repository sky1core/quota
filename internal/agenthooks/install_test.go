package agenthooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyClaudeHookPreservesOtherHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"quota-cli agent hooks eval --runtime=claude"},{"type":"command","command":"quota-cli agent hooks eval --runtime=claude || true"},{"type":"command","command":"echo keep-bash"}]},{"matcher":"Write","hooks":[{"type":"command","command":"echo keep-write"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(home, "policies")
	plan, err := Apply("claude", "/bin/quota-cli", policyDir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Present {
		t.Fatal("plan should be present after apply")
	}
	root, err := ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCommandString(root, "echo keep-bash") {
		t.Fatal("existing Bash hook was not preserved")
	}
	if !containsCommandString(root, "echo keep-write") {
		t.Fatal("existing Write hook was not preserved")
	}
	if containsCommandString(root, "quota-cli agent hooks eval --runtime=claude") {
		t.Fatal("old managed hook was not replaced")
	}
	if containsCommandString(root, "quota-cli agent hooks eval --runtime=claude || true") {
		t.Fatal("old swallowed managed hook was not replaced")
	}
	if !containsManagedHook(root, "claude", "/bin/quota-cli", policyDir) {
		t.Fatal("managed hook was not installed")
	}
	if !strings.Contains(plan.Command, "--policy-dir") || !strings.Contains(plan.Command, policyDir) {
		t.Fatalf("plan command = %q, want policy dir", plan.Command)
	}
	if matches, err := filepath.Glob(path + ".bak.*"); err != nil || len(matches) != 1 {
		t.Fatalf("backup matches = %v err=%v, want one", matches, err)
	}
}

func TestApplyClaudeHookPreservesUnrelatedArgvHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	unrelated := "logger -- quota-cli agent hooks eval marker"
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+unrelated+`"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(home, "policies")
	if _, err := Apply("claude", "/bin/quota-cli", policyDir); err != nil {
		t.Fatal(err)
	}
	root, err := ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCommandString(root, unrelated) {
		t.Fatalf("unrelated hook whose args merely contain the evaluator was removed: %+v", root)
	}
	if !containsManagedHook(root, "claude", "/bin/quota-cli", policyDir) {
		t.Fatal("managed hook was not installed")
	}
}

func TestApplyClaudeHookPreservesUnrelatedCommandPositionHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	unrelated := "echo agent hooks eval"
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+unrelated+`"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(home, "policies")
	if _, err := Apply("claude", "/bin/quota-cli", policyDir); err != nil {
		t.Fatal(err)
	}
	root, err := ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCommandString(root, unrelated) {
		t.Fatalf("unrelated hook without the managed --runtime argument was removed: %+v", root)
	}
	if !containsManagedHook(root, "claude", "/bin/quota-cli", policyDir) {
		t.Fatal("managed hook was not installed")
	}
}

func TestApplyReplacesOnlyEvaluatorExecutables(t *testing.T) {
	for _, runtime := range []string{"claude", "codex"} {
		t.Run(runtime, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CODEX_HOME", "")
			t.Chdir(home)
			path := ClaudeSettingsPath()
			if runtime == "codex" {
				path = CodexHooksPath()
			}
			preserved := []string{
				"echo agent hooks eval --runtime=" + runtime,
				"logger agent hooks eval --runtime=" + runtime,
				"/usr/bin/logger agent hooks eval --runtime=" + runtime,
				"env logger agent hooks eval --runtime=" + runtime,
				"sh -c 'echo agent hooks eval --runtime=" + runtime + "'",
				"logger -- quota-cli agent hooks eval --runtime=" + runtime,
				"other-cli agent hooks eval --runtime=" + runtime,
				"quota-cli agent hooks eval --runtime=invalid",
				`"$runner" agent hooks eval --runtime=` + runtime,
				`"/old/bin/$runner" agent hooks eval --runtime=` + runtime,
				`/old/bin/quota-cli agent hooks eval --runtime="$runtime"`,
				`exec -a "$0" /old/bin/quota-cli agent hooks eval --runtime="$runtime"`,
				`exec -a "$0" "$runner" agent hooks eval --runtime=` + runtime,
				`exec -a $0 /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`exec -a "$@" /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`exec -a "${names[@]}" /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`exec -a "${name:-$@}" /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`command exec -a $0 /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`/old/bin/quota-cli agent hooks eval --runtime=` + runtime + ` --policy-dir "$policies"`,
				`logger -- "exec -a \"$0\" /old/bin/quota-cli agent hooks eval --runtime=` + runtime + `"`,
				"unknown-wrapper /old/bin/quota-cli agent hooks eval --runtime=" + runtime,
				"exec -Z /old/bin/quota-cli agent hooks eval --runtime=" + runtime,
				"/old/bin/quota-cli agent hooks eval --runtime=" + runtime + " --runtime=" + runtime,
				"/old/bin/quota-cli agent hooks eval --runtime=" + runtime + " --unknown",
			}
			stale := []string{
				"quota-cli agent hooks eval --runtime=" + runtime,
				"/old/bin/quota-cli agent hooks eval --runtime=" + runtime,
				"./qc agent hooks eval --runtime=" + runtime + " --policy-dir /old/policies",
				"env ./qc agent hooks eval --runtime=" + runtime + " || true",
				"./qc agent hooks eval --runtime " + runtime,
				`exec -a "$0" /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`command exec -a "$0" /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
				`exec -a "$0" -- /old/bin/quota-cli agent hooks eval --runtime=` + runtime,
			}
			var hooks []any
			for _, command := range append(append([]string{}, preserved...), stale...) {
				hooks = append(hooks, map[string]any{"type": "command", "command": command})
			}
			writeTestHooks(t, path, map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": hooks}}})
			for i := 0; i < 2; i++ {
				plan, err := Apply(runtime, "./qc", filepath.Join(home, "policies"))
				if err != nil {
					t.Fatal(err)
				}
				root, err := ReadJSONObject(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, command := range preserved {
					if !containsCommandString(root, command) {
						t.Fatalf("unrelated hook removed: %s", command)
					}
				}
				for _, command := range stale {
					if containsCommandString(root, command) {
						t.Fatalf("stale evaluator hook retained: %s", command)
					}
				}
				groups := root["hooks"].(map[string]any)["PreToolUse"].([]any)
				n := 0
				for _, group := range groups {
					n += len(group.(map[string]any)["hooks"].([]any))
				}
				if n != len(preserved)+1 || !containsCommandString(root, plan.Command) {
					t.Fatalf("hook count=%d, want %d with evaluator present", n, len(preserved)+1)
				}
			}
		})
	}
}

func TestApplyClaudeHookRemovesStaleCustomBinaryHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	stale := "./qc agent hooks eval --runtime=claude --policy-dir /old/pd"
	writeTestHooks(t, path, map[string]any{
		"PreToolUse": []any{testHookGroup("Bash", stale)},
	})
	policyDir := filepath.Join(home, "policies")
	if _, err := Apply("claude", "./qc", policyDir); err != nil {
		t.Fatal(err)
	}
	root, err := ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if containsCommandString(root, stale) {
		t.Fatalf("stale managed hook with custom basename was not removed: %+v", root)
	}
	if n := countPreToolUseBashCommands(root, "agent hooks eval"); n != 1 {
		t.Fatalf("evaluator hook count = %d, want 1", n)
	}
}

func TestApplyClaudeHookPreservesManagedStatusMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	writeTestHooks(t, path, map[string]any{
		"PreToolUse": []any{map[string]any{
			"matcher": "Bash",
			"hooks": []any{map[string]any{
				"type":          "command",
				"command":       "logger ok",
				"statusMessage": "quota-cli agent hooks eval --runtime=claude",
			}},
		}},
	})
	policyDir := filepath.Join(home, "policies")
	if _, err := Apply("claude", "/bin/quota-cli", policyDir); err != nil {
		t.Fatal(err)
	}
	root, err := ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCommandString(root, "logger ok") {
		t.Fatalf("unrelated hook removed via non-command field: %+v", root)
	}
	if !containsManagedHook(root, "claude", "/bin/quota-cli", policyDir) {
		t.Fatal("managed hook was not installed")
	}
}

func countPreToolUseBashCommands(root map[string]any, substr string) int {
	hooks, _ := root["hooks"].(map[string]any)
	count := 0
	for _, group := range hookGroups(hooks["PreToolUse"]) {
		groupMap, ok := group.(map[string]any)
		if !ok || groupMap["matcher"] != "Bash" {
			continue
		}
		hs, _ := groupMap["hooks"].([]any)
		for _, hook := range hs {
			hookMap, ok := hook.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hookMap["command"].(string); ok && strings.Contains(cmd, substr) {
				count++
			}
		}
	}
	return count
}

func TestApplyCodexHookCreatesHooksJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plan, err := Apply("codex", "/bin/quota-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != filepath.Join(home, ".codex", "hooks.json") {
		t.Fatalf("path = %q", plan.Path)
	}
	root, err := ReadJSONObject(plan.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsManagedHook(root, "codex", "", "") {
		t.Fatal("managed hook was not installed")
	}
}

func TestDetectMatchesPolicyDirExactly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	policyDir := filepath.Join(home, "policies")
	if _, err := Apply("claude", "/bin/quota-cli", policyDir); err != nil {
		t.Fatal(err)
	}
	if !Detect("claude", "/bin/quota-cli", policyDir).Present {
		t.Fatal("hook with matching policy dir should be present")
	}
	if Detect("claude", filepath.Join(home, "other", "quota-cli"), policyDir).Present {
		t.Fatal("hook with a different explicit binary should not match")
	}
	if Detect("claude", "/bin/quota-cli", "").Present {
		t.Fatal("hook with a non-default policy dir should not match the default policy dir")
	}
	if Detect("claude", "/bin/quota-cli", filepath.Join(home, "other-policies")).Present {
		t.Fatal("hook with a different policy dir should not match")
	}
}

func TestDetectRequiresPreToolUseBashCommandHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	command := HookCommand("claude", "/bin/quota-cli", "")

	tests := []struct {
		name  string
		hooks map[string]any
		want  bool
	}{
		{
			name:  "wrong event",
			hooks: map[string]any{"PostToolUse": []any{testHookGroup("Bash", command)}},
		},
		{
			name:  "wrong matcher",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Write", command)}},
		},
		{
			name: "wrong hook type",
			hooks: map[string]any{"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks": []any{map[string]any{
					"type":    "notification",
					"command": command,
				}},
			}}},
		},
		{
			name:  "installed",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", command)}},
			want:  true,
		},
		{
			name:  "swallowed exit",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", command+" || true")}},
		},
		{
			name:  "not reached",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", "true || "+command)}},
		},
		{
			name:  "ignores hook event",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", command+" --command true")}},
		},
		{
			name:  "duplicate runtime",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", command+" --runtime=codex")}},
		},
		{
			name:  "negated exit",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", "! "+command)}},
		},
		{
			name:  "redirected stdin",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", command+" <<< '{\"tool_input\":{\"command\":\"true\"}}'")}},
		},
		{
			name:  "leading assignment",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", "PATH=/tmp "+command)}},
		},
		{
			name:  "env wrapper",
			hooks: map[string]any{"PreToolUse": []any{testHookGroup("Bash", "env PATH=/tmp "+command)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestHooks(t, path, tt.hooks)
			if got := Detect("claude", "/bin/quota-cli", "").Present; got != tt.want {
				t.Fatalf("present = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectRejectsDuplicatePolicyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	policyDir := filepath.Join(home, "policies")
	otherPolicyDir := filepath.Join(home, "other-policies")
	command := HookCommand("claude", "/bin/quota-cli", policyDir)
	writeTestHooks(t, path, map[string]any{
		"PreToolUse": []any{testHookGroup("Bash", command+" --policy-dir "+otherPolicyDir)},
	})

	if Detect("claude", "/bin/quota-cli", policyDir).Present {
		t.Fatal("hook with duplicate policy dir should not match")
	}
}

// P2-4: two writes in the same process (same wall-clock second) must produce two
// distinct backups, and the very first original content must survive in one of
// them rather than being overwritten by the second backup.
func TestUpdateJSONObjectWithBackupNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"v":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error { root["v"] = "first"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error { root["v"] = "second"; return nil }); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("backups = %v, want 2", matches)
	}
	foundOriginal := false
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "original") {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Fatalf("original content was overwritten; backups = %v", matches)
	}
}

func testHookGroup(matcher, command string) map[string]any {
	return map[string]any{
		"matcher": matcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
		}},
	}
}

func writeTestHooks(t *testing.T, path string, hooks map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
