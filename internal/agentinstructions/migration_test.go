package agentinstructions

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestManagedTOMLMigrationPreservesUnrelatedBytes(t *testing.T) {
	old := `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`
	cases := []struct{ name, body string }{
		{"tables", fmt.Sprintf("[[hooks.SessionStart]] # group note\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = '%s' # hook note\nadditionalContextLimit = 0\n", old)},
		{"mixed tables", fmt.Sprintf("[[hooks.SessionStart]]\nmatcher = 'startup'\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = '%s'\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = 'echo retained' # retain this\n", old)},
		{"inline root", fmt.Sprintf("hooks = { SessionStart = [{ hooks = [{ type = 'command', command = '%s' }] }], Other = [{hooks = [{type = 'command', command = 'echo retained'}]}] }\n", old)},
		{"inline root last", fmt.Sprintf("hooks = { Other = [{hooks = [{type = 'command', command = 'echo retained'}]}], SessionStart = [{ hooks = [{ type = 'command', command = '%s' }] }] }\n", old)},
		{"inline event", fmt.Sprintf("[hooks]\nSessionStart = [{ hooks = [{type = 'command', command = '%s'}, {type = 'command', command = 'echo retained'}]}]\n", old)},
		{"inline owned entries", fmt.Sprintf("[[hooks.SessionStart]]\nhooks = [\n # retain comment\n {type = 'command', command = '%s'},\n]\n", old)},
		{"inline entries", fmt.Sprintf("[[hooks.SessionStart]]\nhooks = [\n # retain comment\n {type = 'command', command = '%s'},\n {type = 'command', command = 'echo retained'},\n]\n", old)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			i := testInstallation(t)
			prefix := "# document note\nmodel = 'placeholder'\n"
			suffix := "\n[unrelated]\nvalue = '''text\n[[hooks.SessionStart]]\n# inside string\n''' # final note\n"
			before := prefix + tt.body + suffix
			installWrite(t, i.targets.CodexConfig, before)
			plan, err := i.Plan([]string{"codex"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := i.Apply(plan); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(i.targets.CodexConfig)
			if err != nil {
				t.Fatal(err)
			}
			after := string(saved)
			if !strings.HasPrefix(after, prefix) || !strings.HasSuffix(after, suffix) {
				t.Fatalf("unrelated bytes changed:\n%s", after)
			}
			if strings.Contains(after, old) {
				t.Fatal("old managed command retained")
			}
			if strings.Contains(before, "echo retained") && !strings.Contains(after, "echo retained") {
				t.Fatal("unrelated hook lost")
			}
			for _, comment := range []string{"# group note", "# hook note", "# retain comment"} {
				if strings.Contains(before, comment) && !strings.Contains(after, comment) {
					t.Fatalf("comment lost: %s", comment)
				}
			}
			second, err := i.Apply(plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Applied) != 0 {
				t.Fatalf("migration not idempotent: %+v", second)
			}
			status, _ := i.Inspect([]string{"codex"})
			if !status[0].Configured {
				t.Fatalf("migration not configured: %+v", status)
			}
		})
	}
}
func TestInspectionCombinesTOMLAndJSON(t *testing.T) {
	i := testInstallation(t)
	if _, err := i.Apply(InstallPlan{Agents: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	installWrite(t, i.targets.CodexConfig, fmt.Sprintf("[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = %q\n", i.command("codex", instructionEvent)))
	status, err := i.Inspect([]string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if status[0].Configured {
		t.Fatal("duplicate TOML hook reported configured")
	}
	installWrite(t, i.targets.CodexConfig, "[features]\nhooks = false\n")
	status, err = i.Inspect([]string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !status[0].Configured {
		t.Fatal("native instruction delivery requires disabled TOML hooks")
	}
}

func TestManagedTOMLMigrationPreservesLargeInteger(t *testing.T) {
	i := testInstallation(t)
	before := "unrelated_counter = 9007199254740993\n[limits]\nmaximum = 9223372036854775807\n" + fmt.Sprintf("[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = %q\n", i.command("codex", instructionEvent))
	installWrite(t, i.targets.CodexConfig, before)
	plan, err := i.Plan([]string{"codex"}, false)
	if err != nil {
		t.Fatalf("large integer blocked migration: %v", err)
	}
	if _, err := i.Apply(plan); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(i.targets.CodexConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(saved), "unrelated_counter = 9007199254740993\n[limits]\nmaximum = 9223372036854775807\n") {
		t.Fatalf("integer bytes changed: %s", saved)
	}
	status, err := i.Inspect([]string{"codex"})
	if err != nil || !status[0].Configured {
		t.Fatalf("migration not configured: %+v %v", status, err)
	}
}

func TestCodexHookTrustStateSurvivesInstallationAndMigration(t *testing.T) {
	for _, layout := range []string{"state only", "table hooks", "inline hooks"} {
		t.Run(layout, func(t *testing.T) {
			i := testInstallation(t)
			state := "[hooks.state.example_entry]\nenabled = false\ntrusted_hash = 'placeholder-trust' # preserve trust metadata\n"
			before := state
			switch layout {
			case "table hooks":
				before = fmt.Sprintf("[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = %q\n", i.command("codex", instructionEvent)) + state
			case "inline hooks":
				before = fmt.Sprintf("hooks = { state = { example_entry = { enabled = false, trusted_hash = 'placeholder-trust' } }, SessionStart = [{hooks = [{type = 'command', command = %q}]}] } # preserve trust metadata\n", i.command("codex", instructionEvent))
			}
			installWrite(t, i.targets.CodexConfig, before)
			original, err := parseInstallTOML([]byte(before))
			if err != nil {
				t.Fatal(err)
			}
			wantState := original["hooks"].(map[string]any)["state"]
			plan, err := i.Plan([]string{"codex"}, false)
			if err != nil {
				t.Fatalf("native trust state blocked plan: %v", err)
			}
			beforePlan, _ := os.ReadFile(i.targets.CodexConfig)
			if string(beforePlan) != before {
				t.Fatal("plan changed trust metadata")
			}
			for _, uninstall := range []bool{false, true} {
				plan.Uninstall = uninstall
				if _, err := i.Apply(plan); err != nil {
					t.Fatal(err)
				}
				saved, err := os.ReadFile(i.targets.CodexConfig)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := parseInstallTOML(saved)
				if err != nil {
					t.Fatal(err)
				}
				if !installEqual(actual["hooks"].(map[string]any)["state"], wantState) {
					t.Fatalf("native trust state changed: %#v", actual)
				}
				if layout == "state only" && string(saved) != before {
					t.Fatal("state-only config was rewritten")
				}
				if layout == "table hooks" && !strings.HasSuffix(string(saved), state) {
					t.Fatal("trust metadata bytes changed")
				}
				if !strings.Contains(string(saved), "# preserve trust metadata") {
					t.Fatal("trust metadata comment lost")
				}
				if !uninstall {
					status, err := i.Inspect([]string{"codex"})
					if err != nil || !status[0].Configured {
						t.Fatalf("trust metadata blocked static installation inspection: %+v %v", status, err)
					}
				}
			}
		})
	}
}
func TestCodexHookTrustStateRejectsInvalidTypesBeforeWriting(t *testing.T) {
	cases := []string{
		"[hooks]\nstate = []\n",
		"[hooks.state]\nexample_entry = true\n",
		"[hooks.state.example_entry]\nenabled = 'yes'\n",
		"[hooks.state.example_entry]\ntrusted_hash = 42\n",
	}
	for _, content := range cases {
		t.Run(content, func(t *testing.T) {
			i := testInstallation(t)
			installWrite(t, i.targets.CodexConfig, content)
			if _, err := i.Apply(InstallPlan{Agents: []string{"claude", "codex"}}); err == nil {
				t.Fatal("invalid native trust state accepted")
			}
			if _, err := os.Stat(i.targets.ClaudeSettings); !os.IsNotExist(err) {
				t.Fatal("invalid trust state caused partial account write")
			}
			saved, _ := os.ReadFile(i.targets.CodexConfig)
			if string(saved) != content {
				t.Fatal("invalid trust metadata changed")
			}
		})
	}
}
