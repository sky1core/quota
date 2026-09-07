package agentoverlay

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeEmptyEventRemovesReplacedHooksAndIsIdempotent(t *testing.T) {
	for _, initial := range []string{"empty", "old object", "old string", "unrelated", "null"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			path := filepath.Join(dir, "spec.json")
			if err := os.WriteFile(path, []byte(`{"version":1,"claude":{"hooks":{"SessionStart":[]},"replaces":["old-hook"]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			spec, err := LoadSpec(path)
			if err != nil {
				t.Fatal(err)
			}
			groups := []any{}
			if initial != "empty" && initial != "null" {
				var old any = map[string]any{"type": "invalid", "command": "old-hook"}
				if initial == "old string" {
					old = "old-hook"
				}
				group := map[string]any{"hooks": []any{old}}
				if initial == "unrelated" {
					group["matcher"] = "^compact$"
					group["hooks"] = []any{old, map[string]any{"type": "command", "command": "user-hook"}}
				}
				groups = append(groups, group)
			}
			if initial == "null" {
				groups = nil
			}
			otherEvent := []any{claudeHookGroup(HookEntry{Command: "old-hook"})}
			writeSettings(t, ClaudeSettingsPath(), map[string]any{
				"keep": "value", "disableAllHooks": true, "allowManagedHooksOnly": true,
				"hooks": map[string]any{"SessionStart": groups, "OtherEvent": otherEvent},
			})
			before := PlanClaude(spec)
			doc := DoctorClaude(spec)
			if initial == "null" {
				if doc.State != StateError {
					t.Fatalf("null event: %+v", doc)
				}
			} else if initial == "empty" {
				if doc.State != StateInstalled {
					t.Fatalf("empty event: %+v", doc)
				}
			} else if doc.State != StateDegraded || len(before.Entries) != 1 || before.Entries[0].Command != "old-hook" || before.Entries[0].Status != StatusStale || !reflect.DeepEqual(before.Entries, doc.Missing) {
				t.Fatalf("removal pending: plan=%+v doctor=%+v", before, doc)
			}
			if _, err := ApplyClaude(spec); err != nil {
				t.Fatal(err)
			}
			root := readSettings(t, ClaudeSettingsPath())
			hooks := root["hooks"].(map[string]any)
			if _, ok := hooks["SessionStart"].([]any); !ok {
				t.Fatalf("event is not an array: %v", hooks["SessionStart"])
			}
			wantGroups := []any{}
			if initial == "unrelated" {
				wantGroups = append(wantGroups, map[string]any{"matcher": "^compact$", "hooks": []any{map[string]any{"type": "command", "command": "user-hook"}}})
			}
			if !reflect.DeepEqual(hooks["SessionStart"], wantGroups) || !reflect.DeepEqual(hooks["OtherEvent"], otherEvent) || root["keep"] != "value" || root["disableAllHooks"] != true || root["allowManagedHooksOnly"] != true {
				t.Fatalf("unexpected saved settings: %v", root)
			}
			if doc := DoctorClaude(spec); doc.State != StateInstalled {
				t.Fatalf("after removal: %+v", doc)
			}
			content, err := os.ReadFile(ClaudeSettingsPath())
			if err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1600000000, 0)
			if err := os.Chtimes(ClaudeSettingsPath(), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			filesBefore, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyClaude(spec); err != nil {
				t.Fatalf("repeat apply: %v", err)
			}
			filesAfter, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(ClaudeSettingsPath())
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(ClaudeSettingsPath())
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != string(second) || !info.ModTime().Equal(stamp) || len(filesBefore) != len(filesAfter) {
				t.Fatal("repeat apply changed settings or created a backup")
			}
		})
	}
}

func TestClaudeEmptyEventRejectsMalformedSettings(t *testing.T) {
	for _, input := range []string{`{"hooks":null}`, `{"hooks":{"SessionStart":{}}}`, `{"hooks":{"SessionStart":false}}`} {
		t.Run(input, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			if err := os.WriteFile(ClaudeSettingsPath(), []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := &Spec{Version: 1, Claude: &ClaudeSpec{Hooks: map[string][]HookEntry{"SessionStart": {}}}}
			if doc := DoctorClaude(spec); doc.State != StateError {
				t.Fatalf("malformed event: %+v", doc)
			}
			if _, err := ApplyClaude(spec); err == nil {
				t.Fatal("apply accepted malformed settings")
			}
			got, err := os.ReadFile(ClaudeSettingsPath())
			if err != nil || string(got) != input {
				t.Fatalf("settings changed: %s %v", got, err)
			}
		})
	}
}

func TestCodexSettingsOnlyIgnoresUnrequestedHookBlockers(t *testing.T) {
	for _, hookSpec := range []string{"", `,"hooks":{"SessionStart":[]}`} {
		for _, config := range []string{"", "allow_managed_hooks_only=true\n", "[features]\nhooks=false\n", "[features]\ncodex_hooks=false\n", "[features]\nhooks=true\ncodex_hooks=false\n", "features=false\n"} {
			t.Run(hookSpec+config, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("CODEX_HOME", dir)
				path := filepath.Join(dir, "spec.json")
				if err := os.WriteFile(path, []byte(`{"version":1,"codex":{"settings":{"project_doc_max_bytes":32768}`+hookSpec+`}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				spec, err := LoadSpec(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range []string{"32768", "8192"} {
					content := "project_doc_max_bytes=" + value + "\n" + config
					if err := os.WriteFile(CodexConfigPath(), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
					plan, doc := PlanCodex(spec), DoctorCodex(spec)
					wantState := StateInstalled
					if value != "32768" {
						wantState = StateDegraded
					}
					if doc.State != wantState || len(plan.Reasons) != 0 || len(plan.Entries) != 0 || (value != "32768" && !strings.Contains(doc.Reason, "project_doc_max_bytes")) {
						t.Fatalf("plan=%+v doctor=%+v", plan, doc)
					}
					got, err := os.ReadFile(CodexConfigPath())
					if err != nil || string(got) != content {
						t.Fatalf("config changed: %s %v", got, err)
					}
				}
			})
		}
	}
}

func TestCodexRawHookSettingsStillReportBlockers(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"codex":{"settings":{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"example-hook"}]}]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	snippet, err := codexSnippet(spec.Codex.Settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{"", "allow_managed_hooks_only=true\n", "[features]\nhooks=false\n"} {
		if err := os.WriteFile(CodexConfigPath(), []byte(config+snippet), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, doc := PlanCodex(spec), DoctorCodex(spec)
		if len(plan.Settings) != 1 || plan.Settings[0].Status != StatusPresent {
			t.Fatalf("raw hooks differ: %+v", plan)
		}
		if config == "" {
			if doc.State != StateInstalled {
				t.Fatalf("no blocker: %+v", doc)
			}
		} else if doc.State != StateDegraded || len(plan.Reasons) == 0 || !strings.Contains(doc.Reason, plan.Reasons[0]) {
			t.Fatalf("blocker ignored: plan=%+v doctor=%+v", plan, doc)
		}
	}
}
