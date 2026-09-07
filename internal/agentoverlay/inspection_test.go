package agentoverlay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestGeneratedCodexConfigPassesInspection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	spec := codexSpec()
	spec.Codex.Settings["large_integer"] = json.Number("9007199254740993")
	spec.Codex.Settings["maximum_integer"] = json.Number("9223372036854775807")
	spec.Codex.Settings["nested"] = map[string]any{"fraction": json.Number("0.125"), "exponent": json.Number("9.007199254740993e15")}
	snippet, err := codexSnippet(spec.Codex.Settings, spec.Codex.Hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CodexConfigPath(), []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	if doc := DoctorCodex(spec); doc.State != StateInstalled {
		t.Fatalf("round-trip: %+v\n%s", doc, snippet)
	}
	var root map[string]any
	if err := toml.Unmarshal([]byte(snippet), &root); err != nil {
		t.Fatal(err)
	}
	if root["large_integer"] != int64(9007199254740993) || root["maximum_integer"] != int64(9223372036854775807) {
		t.Fatalf("integer precision lost: %v", root)
	}
}

func TestInspectionReportsManagedExecutionChanges(t *testing.T) {
	variants := []struct {
		name  string
		group bool
		key   string
		value any
	}{
		{"condition", false, "if", "Bash(git *)"}, {"exec form", false, "args", []any{}},
		{"unknown hook field", false, "futureBehavior", true}, {"async", false, "async", true},
		{"timeout", false, "timeout", 0}, {"scoped group", true, "matcher", "^compact$"},
		{"unknown group field", true, "futureCondition", true},
	}
	for _, runtime := range []string{"claude", "codex"} {
		for _, variant := range variants {
			t.Run(runtime+"/"+variant.name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("CLAUDE_CONFIG_DIR", home)
				t.Setenv("CODEX_HOME", home)
				e := HookEntry{Command: overlayHook + " session"}
				spec := &Spec{Version: 1}
				var root map[string]any
				if runtime == "claude" {
					spec.Claude = &ClaudeSpec{Hooks: map[string][]HookEntry{"SessionStart": {e}}}
					root = map[string]any{"hooks": map[string]any{"SessionStart": []any{claudeHookGroup(e)}}}
				} else {
					spec.Codex = &CodexSpec{Hooks: map[string][]HookEntry{"SessionStart": {e}}}
					root, _ = expectedCodexConfig(nil, spec.Codex.Hooks)
				}
				group := groupArray(root["hooks"].(map[string]any)["SessionStart"])[0].(map[string]any)
				if variant.group {
					group[variant.key] = variant.value
				} else {
					groupArray(group["hooks"])[0].(map[string]any)[variant.key] = variant.value
				}
				var plan RuntimePlan
				var doc RuntimeDoctor
				if runtime == "claude" {
					writeSettings(t, ClaudeSettingsPath(), root)
					plan = PlanClaude(spec)
					doc = DoctorClaude(spec)
				} else {
					var b strings.Builder
					if err := toml.NewEncoder(&b).Encode(root); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(CodexConfigPath(), []byte(b.String()), 0o600); err != nil {
						t.Fatal(err)
					}
					plan = PlanCodex(spec)
					doc = DoctorCodex(spec)
				}
				if doc.State != StateDegraded || len(doc.Missing) != 1 || len(plan.Entries) != 1 {
					t.Fatalf("plan=%+v doctor=%+v", plan, doc)
				}
				if !reflect.DeepEqual(plan.Entries, doc.Missing) || !strings.Contains(plan.Entries[0].Reason, variant.key) {
					t.Fatalf("inconsistent reason: %+v %+v", plan, doc)
				}
			})
		}
	}
}

func TestExplicitHookBlockersAreSharedByPlanAndDoctor(t *testing.T) {
	for _, runtime := range []string{"claude", "codex"} {
		t.Run(runtime, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", home)
			t.Setenv("CODEX_HOME", home)
			if runtime == "claude" {
				spec := claudeOnlySpec(t)
				writeSettings(t, ClaudeSettingsPath(), map[string]any{"disableAllHooks": true})
				plan, err := ApplyClaude(spec)
				if err == nil || len(plan.Reasons) == 0 {
					t.Fatalf("apply must report blocker: %+v %v", plan, err)
				}
				if readSettings(t, ClaudeSettingsPath())["disableAllHooks"] != true {
					t.Fatal("apply enabled hooks")
				}
				doc := DoctorClaude(spec)
				if doc.State != StateDegraded || !strings.Contains(doc.Reason, plan.Reasons[0]) {
					t.Fatalf("%+v", doc)
				}
			} else {
				spec := codexSpec()
				snippet, err := codexSnippet(spec.Codex.Settings, spec.Codex.Hooks)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(CodexConfigPath(), []byte("[features]\nhooks=false\n"+snippet), 0o600); err != nil {
					t.Fatal(err)
				}
				plan := PlanCodex(spec)
				doc := DoctorCodex(spec)
				if len(plan.Reasons) == 0 || doc.State != StateDegraded || !strings.Contains(doc.Reason, "features.hooks") {
					t.Fatalf("%+v %+v", plan, doc)
				}
			}
		})
	}
}

func TestSpecRejectsUnsupportedNumericAndRuntimeFields(t *testing.T) {
	inputs := []string{
		`{"version":1,"claude":{"hooks":{"SessionStart":[{"command":"hook","additionalContextLimit":0}]}}}`,
		`{"version":1,"codex":{"hooks":{"SessionStart":[{"command":"hook","additionalContextLimit":-1}]}}}`,
		`{"version":1,"codex":{"settings":{"n":9223372036854775808}}}`,
		`{"version":1,"codex":{"settings":{"n":-9223372036854775809}}}`,
		`{"version":1,"codex":{"settings":{"n":1e400}}}`,
		`{"version":1,"codex":{"settings":{"n":1e-4000}}}`,
		`{"version":1,"codex":{"settings":{"n":100000000000000000000}}}`,
		`{"version":1,"claude":{"hooks":{"SessionStart":[{"command":"A"},{"command":"B"}]},"replaces":["A"]}}`,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.json")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSpec(path); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestLoadSpecKeepsLargeIntegerPrecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"codex":{"settings":{"n":9007199254740993}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Codex.Settings["n"] != json.Number("9007199254740993") {
		t.Fatalf("%v", spec.Codex.Settings)
	}
}

func TestCodexNestedSettingReplacementRoundTrip(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	spec := &Spec{Version: 1, Codex: &CodexSpec{Settings: map[string]any{"custom": map[string]any{"limit": json.Number("9007199254740993")}}}}
	if err := os.WriteFile(CodexConfigPath(), []byte("[custom]\nlimit=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := DoctorCodex(spec)
	if doc.State != StateDegraded || doc.Snippet != "" || len(doc.Replacements) != 1 {
		t.Fatalf("%+v", doc)
	}
	_, replacement, ok := strings.Cut(doc.Replacements[0], "\n")
	if !ok {
		t.Fatal("replacement TOML missing")
	}
	if err := os.WriteFile(CodexConfigPath(), []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if doc := DoctorCodex(spec); doc.State != StateInstalled {
		t.Fatalf("replacement is not usable: %+v", doc)
	}
}

func TestCodexFeatureAliasConflictIsUnsupported(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	spec := codexSpec()
	snippet, err := codexSnippet(spec.Codex.Settings, spec.Codex.Hooks)
	if err != nil {
		t.Fatal(err)
	}
	for _, features := range []string{"hooks=true\ncodex_hooks=false", "codex_hooks=false"} {
		if err := os.WriteFile(CodexConfigPath(), []byte(snippet+"\n[features]\n"+features+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if doc := DoctorCodex(spec); doc.State != StateDegraded || !strings.Contains(doc.Reason, "features.codex_hooks") {
			t.Fatalf("%+v", doc)
		}
	}
}

func TestNormalizeForTOMLFollowsLiteralForm(t *testing.T) {
	cases := map[string]any{"1": int64(1), "-7": int64(-7), "1.0": int64(1), "0e0": int64(0), "9.007199254740993e15": int64(9007199254740993), "9223372036854775807": int64(9223372036854775807), "1e20": float64(1e20), "1e100": float64(1e100), "9.3e18": float64(9.3e18), "0.5": float64(0.5)}
	for literal, want := range cases {
		got, err := normalizeForTOML(json.Number(literal))
		if err != nil || got != want {
			t.Fatalf("%s: got %v (%T), err=%v; want %v (%T)", literal, got, got, err, want, want)
		}
	}
	for _, literal := range []string{"9223372036854775808", "-9223372036854775809", "100000000000000000000", "1e400", "1e-4000", "abc"} {
		if _, err := normalizeForTOML(json.Number(literal)); err == nil {
			t.Fatalf("%s accepted", literal)
		}
	}
}
