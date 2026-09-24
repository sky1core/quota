package agentinstructions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func testInstallation(t *testing.T) *Installation {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	targets, err := CurrentInstallTargets()
	if err != nil {
		t.Fatal(err)
	}
	i, err := NewInstallation("/example/quota-cli", targets)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestInstallationInspectsAndRepairsPromptGuard(t *testing.T) {
	for _, mutation := range []string{"missing", "duplicate", "async", "conditional"} {
		t.Run(mutation, func(t *testing.T) {
			i := testInstallation(t)
			plan := InstallPlan{Agents: []string{"claude"}}
			if _, err := i.Apply(plan); err != nil {
				t.Fatal(err)
			}
			root := installReadJSON(t, i.targets.ClaudeSettings)
			hooks := root["hooks"].(map[string]any)
			groups, _ := installArray(hooks["UserPromptSubmit"])
			group := groups[0].(map[string]any)
			entries, _ := installArray(group["hooks"])
			switch mutation {
			case "missing":
				delete(hooks, "UserPromptSubmit")
			case "duplicate":
				group["hooks"] = append(entries, entries[0])
			case "async":
				entries[0].(map[string]any)["async"] = true
			case "conditional":
				group["matcher"] = "conditional"
			}
			b, _ := json.Marshal(root)
			installWrite(t, i.targets.ClaudeSettings, string(b))
			statuses, err := i.Inspect([]string{"claude"})
			if err != nil || statuses[0].Configured || !strings.Contains(strings.Join(statuses[0].Problems, "\n"), "UserPromptSubmit") {
				t.Fatalf("guard defect missed: %+v %v", statuses, err)
			}
			if _, err := i.Apply(plan); err != nil {
				t.Fatal(err)
			}
			statuses, err = i.Inspect([]string{"claude"})
			if err != nil || !statuses[0].Configured {
				t.Fatalf("guard not repaired: %+v %v", statuses, err)
			}
		})
	}
}

func installWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	root, err := agenthooks.ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func installCommands(t *testing.T, root map[string]any, event string) []string {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := installArray(hooks[event])
	var out []string
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		entries, _ := installArray(group["hooks"])
		for _, entry := range entries {
			hook, _ := entry.(map[string]any)
			command, _ := hook["command"].(string)
			out = append(out, command)
		}
	}
	return out
}

func installHookEntries(t *testing.T, root map[string]any, event string) []map[string]any {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := installArray(hooks[event])
	var out []map[string]any
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		entries, _ := installArray(group["hooks"])
		for _, entry := range entries {
			hook, _ := entry.(map[string]any)
			out = append(out, hook)
		}
	}
	return out
}

func TestInstallationLifecycleReplacesLegacyHooksWithOneHook(t *testing.T) {
	i := testInstallation(t)
	unrelated := map[string]any{"type": "command", "command": "echo 'agent instructions _prepare'", "timeout": 12}
	quota := func(args ...string) string {
		return agenthooks.ShellQuote(append([]string{i.executable, "agent", "instructions"}, args...))
	}
	legacyClaude := []any{
		map[string]any{"type": "command", "command": quota("_hook", "--agent=claude", "--event=SessionStart")},
		map[string]any{"type": "command", "command": quota("_prepare", "--agent=claude", "--event=SessionStart", "--claude-config-dir", i.targets.ClaudeConfigDir)},
	}
	for part := 1; part <= 8; part++ {
		legacyClaude = append(legacyClaude, map[string]any{"type": "command", "command": quota("_prepare", "--agent=claude", "--event=SessionStart", fmt.Sprintf("--part=%d", part))})
	}
	legacyWorktree := map[string]any{"type": "command", "command": quota("_prepare", "--agent=claude", "--event=WorktreeCreate", "--claude-config-dir", i.targets.ClaudeConfigDir)}
	legacyWorktreeRemove := map[string]any{"type": "command", "command": quota("_hook", "--agent=claude", "--event=WorktreeRemove")}
	legacyCodex := []any{
		map[string]any{"type": "command", "command": quota("_hook", "--agent=codex", "--event=SessionStart"), "additionalContextLimit": 0},
		map[string]any{"type": "command", "command": quota("_prepare", "--agent=codex", "--event=SessionStart", "--codex-home", i.targets.CodexHome), "additionalContextLimit": 0},
		map[string]any{"type": "command", "command": `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`},
		map[string]any{"type": "command", "command": agenthooks.ShellQuote([]string{i.executable, "agent", "overlay", "hook", "--runtime=codex", "--event=SessionStart"})},
	}
	legacySubagent := map[string]any{"type": "command", "command": quota("_hook", "--agent=codex", "--event=SubagentStart"), "additionalContextLimit": 0}
	claudeRoot := map[string]any{"model": "opus", "hooks": map[string]any{
		"SessionStart":   []any{map[string]any{"hooks": append([]any{unrelated}, legacyClaude...)}},
		"WorktreeCreate": []any{map[string]any{"hooks": []any{legacyWorktree}}},
		"WorktreeRemove": []any{map[string]any{"hooks": []any{legacyWorktreeRemove}}},
		"PreToolUse":     []any{map[string]any{"matcher": "Bash", "hooks": []any{unrelated}}},
	}}
	codexRoot := map[string]any{"description": "keep", "hooks": map[string]any{
		"SessionStart":  []any{map[string]any{"hooks": append(legacyCodex, unrelated)}},
		"SubagentStart": []any{map[string]any{"hooks": []any{legacySubagent}}},
		"state":         map[string]any{"abc": map[string]any{"enabled": true, "trusted_hash": "x"}},
	}}
	for path, root := range map[string]map[string]any{i.targets.ClaudeSettings: claudeRoot, i.targets.CodexHooks: codexRoot} {
		b, _ := json.Marshal(root)
		installWrite(t, path, string(b))
	}
	statuses, err := i.Inspect([]string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.Configured || !strings.Contains(strings.Join(s.Problems, "\n"), "obsolete managed hook") {
			t.Fatalf("%s legacy hooks were not reported: %+v", s.Agent, s)
		}
	}
	plan, err := i.Plan([]string{"claude", "codex"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		if filepath.Base(change.Path) != "config.toml" && !change.Changed {
			t.Fatalf("expected change for %s", change.Path)
		}
	}
	result, err := i.Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 2 {
		t.Fatalf("applied = %v", result.Applied)
	}
	claude := installReadJSON(t, i.targets.ClaudeSettings)
	if claude["model"] != "opus" {
		t.Fatal("unrelated Claude settings lost")
	}
	for _, event := range []string{"WorktreeCreate", "WorktreeRemove"} {
		if _, ok := claude["hooks"].(map[string]any)[event]; ok {
			t.Fatalf("legacy %s hook survived", event)
		}
	}
	commands := installCommands(t, claude, "SessionStart")
	if len(commands) != 2 || commands[0] != unrelated["command"] {
		t.Fatalf("Claude SessionStart hooks = %v", commands)
	}
	if want := quota("_prepare", "--agent=claude", "--event=SessionStart"); commands[1] != want {
		t.Fatalf("Claude command = %q; want %q", commands[1], want)
	}
	if commands := installCommands(t, claude, "UserPromptSubmit"); len(commands) != 1 || commands[0] != quota("_prepare", "--agent=claude", "--event=UserPromptSubmit") {
		t.Fatalf("Claude prompt guard = %v", commands)
	}
	for _, c := range commands {
		if strings.Contains(c, "_hook") || strings.Contains(c, "--claude-config-dir") {
			t.Fatalf("legacy entry survived: %q", c)
		}
	}
	if commands := installCommands(t, claude, "PreToolUse"); len(commands) != 1 {
		t.Fatalf("unrelated PreToolUse hook changed: %v", commands)
	}
	codex := installReadJSON(t, i.targets.CodexHooks)
	if commands := installCommands(t, codex, "UserPromptSubmit"); len(commands) != 0 {
		t.Fatalf("unexpected Codex prompt guard = %v", commands)
	}
	if codex["description"] != "keep" {
		t.Fatal("unrelated Codex settings lost")
	}
	if _, ok := codex["hooks"].(map[string]any)["state"]; !ok {
		t.Fatal("Codex trust state lost")
	}
	if _, ok := codex["hooks"].(map[string]any)["SubagentStart"]; ok {
		t.Fatal("legacy SubagentStart hook survived")
	}
	entries := installHookEntries(t, codex, "SessionStart")
	if len(entries) != 2 || entries[0]["command"] != unrelated["command"] || entries[1]["command"] != i.command("codex", instructionEvent) || entries[1]["additionalContextLimit"] != json.Number("0") {
		t.Fatalf("Codex SessionStart hooks = %v", entries)
	}
	if c := entries[1]["command"].(string); strings.Contains(c, "--codex-home") || strings.Contains(c, "--part") {
		t.Fatalf("Codex command carries removed arguments: %q", c)
	}
	statuses, err = i.Inspect([]string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if !s.Configured {
			t.Fatalf("%s not configured after setup: %v", s.Agent, s.Problems)
		}
	}
	again, err := i.Plan([]string{"claude", "codex"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range again.Changes {
		if change.Changed {
			t.Fatalf("re-setup would rewrite %s", change.Path)
		}
	}
	backups, _ := filepath.Glob(i.targets.ClaudeSettings + ".bak.*")
	result, err = i.Apply(again)
	if err != nil || len(result.Applied) != 0 {
		t.Fatalf("re-apply wrote files: %v %v", result, err)
	}
	if after, _ := filepath.Glob(i.targets.ClaudeSettings + ".bak.*"); len(after) != len(backups) {
		t.Fatal("re-apply created a backup")
	}
	removal, err := i.Plan([]string{"claude", "codex"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Apply(removal); err != nil {
		t.Fatal(err)
	}
	claude = installReadJSON(t, i.targets.ClaudeSettings)
	if commands := installCommands(t, claude, "UserPromptSubmit"); len(commands) != 0 {
		t.Fatalf("uninstall left prompt guard: %v", commands)
	}
	if commands := installCommands(t, claude, "SessionStart"); len(commands) != 1 || commands[0] != unrelated["command"] {
		t.Fatalf("uninstall changed unrelated hooks: %v", commands)
	}
	codex = installReadJSON(t, i.targets.CodexHooks)
	if commands := installCommands(t, codex, "SessionStart"); len(commands) != 1 || commands[0] != unrelated["command"] {
		t.Fatalf("uninstall changed unrelated Codex hooks: %v", commands)
	}
	statuses, err = i.Inspect([]string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.Configured {
			t.Fatalf("%s still configured after uninstall", s.Agent)
		}
	}
}

func TestInspectReportsMissingDuplicateAndAlteredHooks(t *testing.T) {
	i := testInstallation(t)
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(i.targets.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(t *testing.T, change func(hooks []any) []any) []string {
		t.Helper()
		installWrite(t, i.targets.ClaudeSettings, string(installed))
		root := installReadJSON(t, i.targets.ClaudeSettings)
		groups, _ := installArray(root["hooks"].(map[string]any)["SessionStart"])
		group := groups[0].(map[string]any)
		entries, _ := installArray(group["hooks"])
		group["hooks"] = change(entries)
		b, _ := json.Marshal(root)
		installWrite(t, i.targets.ClaudeSettings, string(b))
		statuses, err := i.Inspect([]string{"claude"})
		if err != nil {
			t.Fatal(err)
		}
		if statuses[0].Configured {
			t.Fatal("problem was not reported")
		}
		return statuses[0].Problems
	}
	problems := mutate(t, func(hooks []any) []any { return hooks[:0] })
	if !strings.Contains(strings.Join(problems, "\n"), "expected one managed hook, found 0") {
		t.Fatalf("missing hook not reported: %v", problems)
	}
	problems = mutate(t, func(hooks []any) []any { return append(hooks, hooks[0]) })
	if !strings.Contains(strings.Join(problems, "\n"), "expected one managed hook, found 2") {
		t.Fatalf("duplicate hook not reported: %v", problems)
	}
	problems = mutate(t, func(hooks []any) []any {
		hooks[0].(map[string]any)["timeout"] = 30
		return hooks
	})
	if !strings.Contains(strings.Join(problems, "\n"), "differs from the fixed execution settings") {
		t.Fatalf("changed settings not reported: %v", problems)
	}
	root := installReadJSON(t, i.targets.ClaudeSettings)
	root["disableAllHooks"] = true
	b, _ := json.Marshal(root)
	installWrite(t, i.targets.ClaudeSettings, string(b))
	statuses, err := i.Inspect([]string{"claude"})
	if err != nil || statuses[0].Configured || !strings.Contains(strings.Join(statuses[0].Problems, "\n"), "disableAllHooks") {
		t.Fatalf("disableAllHooks not reported: %+v %v", statuses, err)
	}
}

func TestInstallationRejectsUnknownOwnershipBeforeWriting(t *testing.T) {
	i := testInstallation(t)
	for _, command := range []string{"/other/quota-cli agent instructions _prepare --agent=codex --event=SessionStart", "/other/quota-cli agent instructions _hook --agent=codex --event=SessionStart", "/other/quota-cli agent overlay hook --runtime=codex --event=SessionStart", `env X=value sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`, `f() { /example/quota-cli agent instructions _prepare --agent=codex --event=SessionStart; }`} {
		root := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
		b, _ := json.Marshal(root)
		installWrite(t, i.targets.CodexHooks, string(b))
		before, _ := os.ReadFile(i.targets.CodexHooks)
		if _, err := i.Plan([]string{"codex"}, false); err == nil {
			t.Fatalf("unknown ownership accepted: %s", command)
		}
		after, _ := os.ReadFile(i.targets.CodexHooks)
		if string(before) != string(after) {
			t.Fatal("rejected plan wrote the file")
		}
	}
}

func TestInstallationPreservesUnrelatedProgramsWithInstructionArguments(t *testing.T) {
	i := testInstallation(t)
	commands := []string{"echo agent instructions _prepare", "/opt/unrelated/auditor agent instructions _hook", "echo legacy instruction words"}
	var entries []any
	for _, c := range commands {
		entries = append(entries, map[string]any{"type": "command", "command": c})
	}
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": entries}}}})
	installWrite(t, i.targets.ClaudeSettings, string(b))
	plan, err := i.Plan([]string{"claude"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Apply(plan); err != nil {
		t.Fatal(err)
	}
	got := installCommands(t, installReadJSON(t, i.targets.ClaudeSettings), "SessionStart")
	if len(got) != len(commands)+1 {
		t.Fatalf("unrelated commands changed: %v", got)
	}
}

func TestInstallationRejectsWrappedInstructionConflicts(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, wrapper := range []string{"sudo -i", "sudo -s", "sudo --login", "sudo --shell", "env command sudo -iu nobody"} {
			t.Run(agent+"/"+wrapper, func(t *testing.T) {
				i := testInstallation(t)
				path := i.targets.CodexHooks
				if agent == "claude" {
					path = i.targets.ClaudeSettings
				}
				command := wrapper + " /other/quota-cli agent instructions _prepare --agent=" + agent + " --event=SessionStart"
				root := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
				body, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				installWrite(t, path, string(body))
				if _, err := i.Plan([]string{agent}, false); err == nil {
					t.Fatal("instruction conflict was accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(body) {
					t.Fatalf("rejected plan changed the source: %v", err)
				}
			})
		}
	}
}

func TestSuspiciousInstructionCommandRecognizesPrepareAndHook(t *testing.T) {
	for _, command := range []string{"/example/custom-binary agent instructions _prepare --agent=codex --event=SessionStart", "/example/quota-cli agent instructions _hook --agent=codex --event=SessionStart"} {
		if !suspiciousInstructionCommand(command, "/example/custom-binary") {
			t.Fatalf("not suspicious: %s", command)
		}
	}
	if suspiciousInstructionCommand("/example/custom-binary agent instructions _prepare --agent=codex --event=SessionStart") {
		t.Fatal("unknown executable treated as quota")
	}
}
