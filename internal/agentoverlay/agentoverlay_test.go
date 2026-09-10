package agentoverlay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const overlayHook = "/opt/overlay/hook"

func claudeOnlySpec(t *testing.T) *Spec {
	t.Helper()
	return &Spec{
		Version: SpecVersion,
		Claude: &ClaudeSpec{
			Hooks: map[string][]HookEntry{
				"SessionStart": {{Command: overlayHook + " session"}},
			},
		},
	}
}

func writeSettings(t *testing.T, path string, root map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	root, err := readSettingsRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readSettingsRaw(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	return root, nil
}

// commandsForEvent flattens every hook command string installed under one event.
func commandsForEvent(root map[string]any, event string) []string {
	var out []string
	hooksObj, _ := root["hooks"].(map[string]any)
	for _, group := range hookGroups(hooksObj[event]) {
		groupMap, _ := group.(map[string]any)
		hooks, _ := groupMap["hooks"].([]any)
		for _, hook := range hooks {
			if cmd, ok := hookAnyFormCommand(hook); ok {
				out = append(out, cmd)
			}
		}
	}
	return out
}

func TestValidateRejectsUnknownVersion(t *testing.T) {
	spec := Spec{Version: 2}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("Validate() = %v, want unknown version error", err)
	}
}

func TestValidateRejectsEmptyCommand(t *testing.T) {
	spec := Spec{Version: 1, Claude: &ClaudeSpec{Hooks: map[string][]HookEntry{
		"SessionStart": {{Command: "  "}},
	}}}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("Validate() = %v, want empty command error", err)
	}
}

func TestApplyClaudePreservesUnrelatedHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	writeSettings(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "/other/tool keep"},
				}},
			},
		},
	})

	if _, err := ApplyClaude(claudeOnlySpec(t)); err != nil {
		t.Fatal(err)
	}
	cmds := commandsForEvent(readSettings(t, path), "SessionStart")
	if !contains(cmds, "/other/tool keep") {
		t.Fatalf("unrelated hook was not preserved: %v", cmds)
	}
	if !contains(cmds, overlayHook+" session") {
		t.Fatalf("spec hook was not installed: %v", cmds)
	}
}

// P1-1: managed is exact-string only. A hook that merely shares argv[0]
// (python3) with the spec command must survive apply.
func TestApplyClaudePreservesUnrelatedSameBinaryHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	audit := "python3 /opt/unrelated/audit.py --watch"
	writeSettings(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": audit},
				}},
			},
		},
	})

	spec := &Spec{Version: SpecVersion, Claude: &ClaudeSpec{Hooks: map[string][]HookEntry{
		"SessionStart": {{Command: "python3 /opt/overlay/hook.py session"}},
	}}}
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	cmds := commandsForEvent(readSettings(t, path), "SessionStart")
	if !contains(cmds, audit) {
		t.Fatalf("unrelated same-binary hook was removed: %v", cmds)
	}
	if !contains(cmds, "python3 /opt/overlay/hook.py session") {
		t.Fatalf("spec hook was not installed: %v", cmds)
	}
}

// P1-2/replaces: only commands listed in claude.replaces are swapped; a similar
// command not listed is preserved.
func TestApplyClaudeReplacesListedCommandOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	old := "python3 /opt/overlay/hook.py session --old"
	similar := "python3 /opt/overlay/hook.py session --older"
	writeSettings(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": old},
					map[string]any{"type": "command", "command": similar},
				}},
			},
		},
	})

	spec := &Spec{Version: SpecVersion, Claude: &ClaudeSpec{
		Hooks:    map[string][]HookEntry{"SessionStart": {{Command: "python3 /opt/overlay/hook.py session"}}},
		Replaces: []string{old},
	}}
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	cmds := commandsForEvent(readSettings(t, path), "SessionStart")
	if contains(cmds, old) {
		t.Fatalf("replaces-listed command was not removed: %v", cmds)
	}
	if !contains(cmds, similar) {
		t.Fatalf("similar command not in replaces was removed: %v", cmds)
	}
	if !contains(cmds, "python3 /opt/overlay/hook.py session") {
		t.Fatalf("fresh spec hook was not installed: %v", cmds)
	}
}

func TestApplyClaudeHandlesStringHookEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	writeSettings(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					overlayHook + " session",
					"/other/tool keep",
				}},
			},
		},
	})

	if _, err := ApplyClaude(claudeOnlySpec(t)); err != nil {
		t.Fatal(err)
	}
	cmds := commandsForEvent(readSettings(t, path), "SessionStart")
	if !contains(cmds, "/other/tool keep") {
		t.Fatalf("unrelated string hook was not preserved: %v", cmds)
	}
	n := 0
	for _, cmd := range cmds {
		if cmd == overlayHook+" session" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("spec command count = %d, want 1 (string entry must be managed): %v", n, cmds)
	}
}

func TestApplyClaudeNormalizesMalformedSameCommandEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	writeSettings(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "invalid", "command": overlayHook + " session"},
				}},
			},
		},
	})
	if _, err := ApplyClaude(claudeOnlySpec(t)); err != nil {
		t.Fatal(err)
	}
	var entries []any
	root := readSettings(t, path)
	for _, group := range hookGroups(root["hooks"].(map[string]any)["SessionStart"]) {
		entries = append(entries, group.(map[string]any)["hooks"].([]any)...)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want the malformed variant replaced by one entry", entries)
	}
	if e := entries[0].(map[string]any); e["type"] != "command" {
		t.Fatalf("entry = %v, want type command", e)
	}
}

func TestDoctorClaudeMatcherScopedEntryIsStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	writeSettings(t, ClaudeSettingsPath(), map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"matcher": "resume", "hooks": []any{
					map[string]any{"type": "command", "command": overlayHook + " session"},
				}},
			},
		},
	})
	doc := DoctorClaude(claudeOnlySpec(t))
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded (matcher-scoped entry must not count)", doc.State)
	}
	if len(doc.Missing) != 1 || doc.Missing[0].Status != StatusStale {
		t.Fatalf("missing = %+v, want one stale entry", doc.Missing)
	}
}

func TestDoctorClaudeEmptyMatcherCountsUnrestricted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	writeSettings(t, ClaudeSettingsPath(), map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"matcher": "", "hooks": []any{
					map[string]any{"type": "command", "command": overlayHook + " session"},
				}},
			},
		},
	})
	if got := DoctorClaude(claudeOnlySpec(t)).State; got != StateInstalled {
		t.Fatalf("state = %q, want installed (empty matcher matches everything)", got)
	}
}

func TestDoctorClaudeStringEntryIsStaleNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	writeSettings(t, ClaudeSettingsPath(), map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{overlayHook + " session"}},
			},
		},
	})
	doc := DoctorClaude(claudeOnlySpec(t))
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded (string entry must not count as installed)", doc.State)
	}
	if len(doc.Missing) != 1 || doc.Missing[0].Status != StatusStale {
		t.Fatalf("missing = %+v, want one stale entry", doc.Missing)
	}
}

func TestLoadSpecRejectsTrailingContent(t *testing.T) {
	dir := t.TempDir()
	for i, trailing := range []string{"\n" + `{"claude":{"hook":{}}}`, "]", "}"} {
		path := filepath.Join(dir, fmt.Sprintf("spec-%d.json", i))
		if err := os.WriteFile(path, []byte(`{"version":1}`+trailing), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSpec(path); err == nil || !strings.Contains(err.Error(), "trailing content") {
			t.Fatalf("LoadSpec(%q trailing) = %v, want trailing content error", trailing, err)
		}
	}
}

func TestApplyClaudeIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	spec := claudeOnlySpec(t)
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	cmds := commandsForEvent(readSettings(t, path), "SessionStart")
	if len(cmds) != 1 {
		t.Fatalf("apply is not idempotent, got: %v", cmds)
	}
}

func TestDoctorClaudeInstalledAfterApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := claudeOnlySpec(t)
	if got := DoctorClaude(spec).State; got != StateDegraded {
		t.Fatalf("before apply state = %q, want degraded", got)
	}
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	if got := DoctorClaude(spec).State; got != StateInstalled {
		t.Fatalf("after apply state = %q, want installed (doctor never reports enforced)", got)
	}
}

// P1-2: disableAllHooks:true keeps the runtime degraded even with entries present.
func TestDoctorClaudeDisableAllHooksDegraded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := claudeOnlySpec(t)
	if _, err := ApplyClaude(spec); err != nil {
		t.Fatal(err)
	}
	path := ClaudeSettingsPath()
	root := readSettings(t, path)
	root["disableAllHooks"] = true
	writeSettings(t, path, root)

	doc := DoctorClaude(spec)
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded", doc.State)
	}
	if !strings.Contains(doc.Reason, "disableAllHooks") {
		t.Fatalf("reason = %q, want disableAllHooks cause", doc.Reason)
	}
}

func TestClaudeIgnoresManagedOnlyKeyButDisableAllHooksBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	path := ClaudeSettingsPath()
	spec := claudeOnlySpec(t)

	writeSettings(t, path, map[string]any{"allowManagedHooksOnly": true, "keep": "value"})
	if plan := PlanClaude(spec); containsSubstr(plan.Reasons, "allowManagedHooksOnly") {
		t.Fatalf("managed-only key flagged before apply: %+v", plan.Reasons)
	}
	if plan, err := ApplyClaude(spec); err != nil || len(plan.Reasons) != 0 {
		t.Fatalf("apply blocked by managed-only key: plan=%+v err=%v", plan, err)
	}
	root := readSettings(t, path)
	if root["allowManagedHooksOnly"] != true || root["keep"] != "value" {
		t.Fatalf("apply did not preserve managed-only key or unrelated setting: %+v", root)
	}
	if got := DoctorClaude(spec).State; got != StateInstalled {
		t.Fatalf("state = %q, want installed with managed-only key present", got)
	}

	root["disableAllHooks"] = true
	writeSettings(t, path, root)
	doc := DoctorClaude(spec)
	if doc.State != StateDegraded || !strings.Contains(doc.Reason, "disableAllHooks") {
		t.Fatalf("disableAllHooks must block: %+v", doc)
	}
	if strings.Contains(doc.Reason, "allowManagedHooksOnly") {
		t.Fatalf("managed-only key must not be a blocker: %q", doc.Reason)
	}
}

func TestDoctorClaudeUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spec := &Spec{Version: SpecVersion}
	if got := DoctorClaude(spec).State; got != StateUnconfigured {
		t.Fatalf("state = %q, want unconfigured", got)
	}
}

func codexSpec() *Spec {
	zero := 0
	return &Spec{
		Version: SpecVersion,
		Codex: &CodexSpec{
			Settings: map[string]any{"project_doc_max_bytes": float64(32768)},
			Hooks: map[string][]HookEntry{
				"SessionStart": {{Command: overlayHook + " codex-session", AdditionalContextLimit: &zero}},
			},
		},
	}
}

func TestDoctorCodexDetectsMismatchAndMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Wrong settings value, no hooks at all.
	if err := os.WriteFile(path, []byte("project_doc_max_bytes = 100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := DoctorCodex(codexSpec())
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded", doc.State)
	}
	if len(doc.Settings) != 1 || doc.Settings[0].Status != StatusMismatch {
		t.Fatalf("settings = %+v, want one mismatch", doc.Settings)
	}
	if len(doc.Missing) != 1 || doc.Missing[0].Status != StatusMissing {
		t.Fatalf("missing = %+v, want one missing hook", doc.Missing)
	}
	// P2-5: the missing hook goes into the add snippet; the mismatched setting
	// value must NOT be in the add snippet (adding it duplicates the TOML key) —
	// it is surfaced as a replace instruction instead.
	if !strings.Contains(doc.Snippet, "[[hooks.SessionStart]]") ||
		!strings.Contains(doc.Snippet, overlayHook+" codex-session") {
		t.Fatalf("snippet missing required hook lines:\n%s", doc.Snippet)
	}
	if strings.Contains(doc.Snippet, "project_doc_max_bytes") {
		t.Fatalf("mismatched setting key must not appear in add snippet:\n%s", doc.Snippet)
	}
	if !containsSubstr(doc.Replacements, "replace the value of project_doc_max_bytes using") {
		t.Fatalf("replacements = %v, want value-replace instruction", doc.Replacements)
	}
}

// P1-2: a Codex hook whose type is not "command" is not recognized as present.
func TestDoctorCodexRejectsNonCommandHookType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "project_doc_max_bytes = 32768\n\n[[hooks.SessionStart]]\n\n[[hooks.SessionStart.hooks]]\ntype = \"invalid\"\ncommand = \"" + overlayHook + " codex-session\"\nadditionalContextLimit = 0\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := DoctorCodex(codexSpec())
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded for non-command hook type", doc.State)
	}
	if len(doc.Missing) != 1 || doc.Missing[0].Status != StatusMismatch {
		t.Fatalf("missing = %+v, want the hook reported not present", doc.Missing)
	}
}

func TestDoctorCodexDetectsContextLimitMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "project_doc_max_bytes = 32768\n\n[[hooks.SessionStart]]\n\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"" + overlayHook + " codex-session\"\nadditionalContextLimit = 999\n"
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := DoctorCodex(codexSpec())
	if doc.State != StateDegraded {
		t.Fatalf("state = %q, want degraded on additionalContextLimit mismatch", doc.State)
	}
	if len(doc.Missing) != 1 || doc.Missing[0].Status != StatusMismatch {
		t.Fatalf("missing = %+v, want one mismatch hook", doc.Missing)
	}
	// A mismatched hook is a replace instruction, not an add snippet (settings are
	// already present, so nothing to add).
	if doc.Snippet != "" {
		t.Fatalf("mismatched hook must not produce an add snippet:\n%s", doc.Snippet)
	}
	if !containsSubstr(doc.Replacements, "additionalContextLimit = 0") {
		t.Fatalf("replacements = %v, want hook replace instruction", doc.Replacements)
	}
}

func TestDoctorCodexEnforced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "project_doc_max_bytes = 32768\n\n[[hooks.SessionStart]]\n\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"" + overlayHook + " codex-session\"\nadditionalContextLimit = 0\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DoctorCodex(codexSpec()).State; got != StateInstalled {
		t.Fatalf("state = %q, want installed (doctor never reports enforced)", got)
	}
}

func TestDoctorCodexParseErrorState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is = = not valid toml ]["), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := DoctorCodex(codexSpec())
	if doc.State != StateError || doc.Error == "" {
		t.Fatalf("doctor = %+v, want error state", doc)
	}
}

func TestInitTemplateValidatesAndRoundTrips(t *testing.T) {
	tmpl := InitTemplate()
	if err := tmpl.Validate(); err != nil {
		t.Fatalf("template invalid: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-overlay.json")
	if _, err := SaveSpec(path, tmpl, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Codex == nil || loaded.Codex.Hooks["SessionStart"][0].AdditionalContextLimit == nil {
		t.Fatalf("template did not round-trip additionalContextLimit: %+v", loaded.Codex)
	}
}

func TestLoadSpecMissingFileErrors(t *testing.T) {
	_, err := LoadSpec(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("LoadSpec() = %v, want not-found error", err)
	}
}

func TestSettingsValueEqualNumberTypes(t *testing.T) {
	if !settingsValueEqual(float64(32768), int64(32768)) {
		t.Fatal("float64 spec should equal int64 toml value")
	}
	if settingsValueEqual(float64(32768), int64(100)) {
		t.Fatal("different numbers must not be equal")
	}
	if !settingsValueEqual([]any{}, []any{}) {
		t.Fatal("empty arrays should be equal")
	}
}

// P2-3: an unknown/typo field is a hard load error, not silently ignored.
func TestLoadSpecRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-overlay.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"claude":{"hook":{"SessionStart":[{"command":"/opt/overlay/hook session"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSpec(path)
	if err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("LoadSpec() = %v, want unknown-field error mentioning \"hook\"", err)
	}
}

// The old global verify.command shape is gone; it now reads as an unknown field.
func TestLoadSpecRejectsLegacyVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-overlay.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"verify":{"command":["/opt/overlay/hook","verify"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("LoadSpec() = %v, want unknown-field error for legacy verify.command", err)
	}
}

func TestLoadSpecAcceptsPerRuntimeVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-overlay.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"verify":{"claude":{"command":["/usr/bin/true"]},"codex":{"command":["/usr/bin/true"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Verify == nil || spec.Verify.Claude == nil || spec.Verify.Codex == nil {
		t.Fatalf("per-runtime verify did not decode: %+v", spec.Verify)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
