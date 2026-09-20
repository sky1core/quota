package agentinstructions

import (
	"context"
	"encoding/json"
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

func TestInstallationLifecycleInstallsPrepareEntries(t *testing.T) {
	i := testInstallation(t)
	unrelated := map[string]any{"type": "command", "command": "echo 'agent instructions _prepare'", "timeout": 12}
	legacyHook := func(agent, event string) string {
		return agenthooks.ShellQuote([]string{i.executable, "agent", "instructions", "_hook", "--agent=" + agent, "--event=" + event})
	}
	legacyClaude := map[string]any{"type": "command", "command": legacyHook("claude", "SessionStart")}
	legacyCodex := map[string]any{"type": "command", "command": legacyHook("codex", "SessionStart"), "additionalContextLimit": 0}
	legacySubagent := map[string]any{"type": "command", "command": legacyHook("codex", "SubagentStart"), "additionalContextLimit": 0}
	overlayLegacy := map[string]any{"type": "command", "command": `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`}
	overlayCommandLegacy := map[string]any{"type": "command", "command": agenthooks.ShellQuote([]string{i.executable, "agent", "overlay", "hook", "--runtime=codex", "--event=SessionStart"})}
	claudeRoot := map[string]any{"model": "opus", "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{unrelated, legacyClaude}}}, "PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{unrelated}}}}}
	codexRoot := map[string]any{"description": "keep", "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{legacyCodex, overlayLegacy, overlayCommandLegacy, unrelated}}}, "SubagentStart": []any{map[string]any{"hooks": []any{legacySubagent}}}, "state": map[string]any{"abc": map[string]any{"enabled": true, "trusted_hash": "x"}}}}
	for path, root := range map[string]map[string]any{i.targets.ClaudeSettings: claudeRoot, i.targets.CodexHooks: codexRoot} {
		b, _ := json.Marshal(root)
		installWrite(t, path, string(b))
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
	for _, event := range []string{"SessionStart", "WorktreeCreate", "WorktreeRemove"} {
		commands := installCommands(t, claude, event)
		want := i.command("claude", event)
		count := 0
		for _, c := range commands {
			if c == want {
				count++
			}
			if strings.Contains(c, "_hook") {
				t.Fatalf("legacy _hook entry survived in %s: %q", event, c)
			}
		}
		if count != 1 {
			t.Fatalf("%s: managed entries = %d in %v", event, count, commands)
		}
		if !strings.Contains(want, "_prepare '--agent=claude' '--event="+event+"' --claude-config-dir ") {
			t.Fatalf("unexpected command %q", want)
		}
	}
	if commands := installCommands(t, claude, "SessionStart"); len(commands) != 2 || commands[0] != unrelated["command"] {
		t.Fatalf("unrelated SessionStart hook not preserved: %v", commands)
	}
	if commands := installCommands(t, claude, "PreToolUse"); len(commands) != 1 {
		t.Fatalf("unrelated PreToolUse hook changed: %v", commands)
	}
	codex := installReadJSON(t, i.targets.CodexHooks)
	if codex["description"] != "keep" {
		t.Fatal("unrelated Codex settings lost")
	}
	if _, ok := codex["hooks"].(map[string]any)["state"]; !ok {
		t.Fatal("Codex trust state lost")
	}
	if _, ok := codex["hooks"].(map[string]any)["SubagentStart"]; ok {
		t.Fatal("legacy SubagentStart injection hook survived")
	}
	commands := installCommands(t, codex, "SessionStart")
	if len(commands) != 2 || commands[0] != unrelated["command"] || commands[1] != i.command("codex", "SessionStart") {
		t.Fatalf("Codex SessionStart hooks = %v", commands)
	}
	groups, _ := installArray(codex["hooks"].(map[string]any)["SessionStart"])
	managedGroup, _ := groups[1].(map[string]any)
	managedHooks, _ := installArray(managedGroup["hooks"])
	managedHook, _ := managedHooks[0].(map[string]any)
	if managedHook["additionalContextLimit"] != json.Number("0") {
		t.Fatalf("Codex instruction hook must pass complete first-session context: %#v", managedHook)
	}
	for _, c := range commands {
		if strings.Contains(c, "_hook") || strings.Contains(c, "agents-overlay-context") || strings.Contains(c, "agent overlay hook") {
			t.Fatalf("legacy Codex hook survived: %q", c)
		}
	}
	statuses, err := i.Inspect([]string{"claude", "codex"})
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
	if commands := installCommands(t, claude, "SessionStart"); len(commands) != 1 || commands[0] != unrelated["command"] {
		t.Fatalf("uninstall changed unrelated hooks: %v", commands)
	}
	if _, ok := claude["hooks"].(map[string]any)["WorktreeCreate"]; ok {
		t.Fatal("uninstall left WorktreeCreate")
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

func applyGlobalIgnore(t *testing.T, path string, uninstall bool) GlobalIgnorePlan {
	t.Helper()
	plan, err := PlanGlobalIgnore(path, uninstall)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestGlobalIgnoreManagesOnlyItsMarkedBlock(t *testing.T) {
	testInstallation(t)
	ctx := context.Background()
	path, err := GlobalIgnorePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore"); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	installWrite(t, path, "# user rules\n*.log\nCLAUDE.local.md\nCLAUDE.local.md.bak")
	plan := applyGlobalIgnore(t, path, false)
	if !plan.Changed || strings.Join(plan.Add, ",") != strings.Join(GlobalIgnoreLines, ",") {
		t.Fatalf("plan = %+v", plan)
	}
	want := "# user rules\n*.log\nCLAUDE.local.md\nCLAUDE.local.md.bak\n" + GlobalIgnoreMarker + "\n" + strings.Join(GlobalIgnoreLines, "\n") + "\n"
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("ignore file = %q", got)
	}
	if plan = applyGlobalIgnore(t, path, false); plan.Changed {
		t.Fatalf("second apply changed the file: %+v", plan)
	}
	if plan = applyGlobalIgnore(t, path, true); !plan.Changed || strings.Join(plan.Remove, ",") != strings.Join(GlobalIgnoreLines, ",") {
		t.Fatalf("removal plan = %+v", plan)
	}
	if got, _ := os.ReadFile(path); string(got) != "# user rules\n*.log\nCLAUDE.local.md\nCLAUDE.local.md.bak\n" {
		t.Fatalf("ignore file after removal = %q", got)
	}
	if plan = applyGlobalIgnore(t, path, true); plan.Changed {
		t.Fatalf("second removal changed the file: %+v", plan)
	}
}

func TestGlobalIgnoreExtendsExistingBlockAndKeepsUserLinesAfterIt(t *testing.T) {
	testInstallation(t)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore")
	installWrite(t, path, GlobalIgnoreMarker+"\nCLAUDE.local.md\n*.tmp\n")
	plan := applyGlobalIgnore(t, path, false)
	if strings.Join(plan.Add, ",") != strings.Join(GlobalIgnoreLines, ",") || len(plan.Remove) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if got, _ := os.ReadFile(path); string(got) != GlobalIgnoreMarker+"\nCLAUDE.local.md\n"+strings.Join(GlobalIgnoreLines, "\n")+"\n*.tmp\n" {
		t.Fatalf("ignore file = %q", got)
	}
	plan = applyGlobalIgnore(t, path, true)
	if strings.Join(plan.Remove, ",") != "CLAUDE.local.md,"+strings.Join(GlobalIgnoreLines, ",") {
		t.Fatalf("removal plan = %+v", plan)
	}
	if got, _ := os.ReadFile(path); string(got) != "*.tmp\n" {
		t.Fatalf("ignore file after removal = %q", got)
	}
}

func TestGlobalIgnoreApplyFailsWhenFileChangedSincePlan(t *testing.T) {
	testInstallation(t)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore")
	installWrite(t, path, "*.log\n")
	plan, err := PlanGlobalIgnore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	installWrite(t, path, "*.log\n*.tmp\n")
	if err := plan.Apply(); err == nil || !strings.Contains(err.Error(), "changed since it was planned") {
		t.Fatalf("apply error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "*.log\n*.tmp\n" {
		t.Fatalf("concurrent edit was overwritten: %q", got)
	}
}

func TestGlobalIgnorePathHonorsExcludesFile(t *testing.T) {
	testInstallation(t)
	custom := filepath.Join(os.Getenv("HOME"), "custom-ignore")
	installWrite(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[core]\n\texcludesFile = ~/custom-ignore\n")
	path, err := GlobalIgnorePath(context.Background())
	if err != nil || path != custom {
		t.Fatalf("path = %s, err = %v", path, err)
	}
	if plan := applyGlobalIgnore(t, path, false); !plan.Changed {
		t.Fatalf("plan = %+v", plan)
	}
	got, _ := os.ReadFile(custom)
	if string(got) != GlobalIgnoreMarker+"\n"+strings.Join(GlobalIgnoreLines, "\n")+"\n" {
		t.Fatalf("new ignore file = %q", got)
	}
}

func TestGlobalIgnorePathHonorsIncludedExcludesFile(t *testing.T) {
	testInstallation(t)
	custom := filepath.Join(os.Getenv("HOME"), "included-ignore")
	include := filepath.Join(os.Getenv("HOME"), "included.gitconfig")
	installWrite(t, include, "[core]\n\texcludesFile = ~/included-ignore\n")
	installWrite(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[include]\n\tpath = "+include+"\n")
	path, err := GlobalIgnorePath(context.Background())
	if err != nil || path != custom {
		t.Fatalf("path = %s, err = %v", path, err)
	}
}

func TestGlobalIgnorePathFallsBackToSystemExcludesFile(t *testing.T) {
	testInstallation(t)
	home := os.Getenv("HOME")
	system := filepath.Join(home, "system.gitconfig")
	global := filepath.Join(home, "global.gitconfig")
	custom := filepath.Join(home, "system-ignore")
	installWrite(t, global, "")
	installWrite(t, system, "[core]\n\texcludesFile = "+custom+"\n")
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	path, err := GlobalIgnorePath(context.Background())
	if err != nil || path != custom {
		t.Fatalf("path = %s, err = %v", path, err)
	}
}

func TestGlobalIgnorePathRespectsNoSystemConfig(t *testing.T) {
	testInstallation(t)
	home := os.Getenv("HOME")
	system := filepath.Join(home, "system.gitconfig")
	global := filepath.Join(home, "global.gitconfig")
	installWrite(t, global, "")
	installWrite(t, system, "[core]\n\texcludesFile = "+filepath.Join(home, "system-ignore")+"\n")
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "true")
	path, err := GlobalIgnorePath(context.Background())
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore")
	if err != nil || path != want {
		t.Fatalf("path = %s, want %s, err = %v", path, want, err)
	}
	for _, value := range []string{"1", "2", "-1", "01", "1k", "True", "Yes", "ON"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", value)
			path, err := GlobalIgnorePath(context.Background())
			if err != nil || path != want {
				t.Fatalf("path = %s, want %s, err = %v", path, want, err)
			}
		})
	}
}

func TestGlobalIgnorePathHonorsFalseNoSystemConfig(t *testing.T) {
	testInstallation(t)
	home := os.Getenv("HOME")
	system := filepath.Join(home, "system.gitconfig")
	global := filepath.Join(home, "global.gitconfig")
	custom := filepath.Join(home, "system-ignore")
	installWrite(t, global, "")
	installWrite(t, system, "[core]\n\texcludesFile = "+custom+"\n")
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "false")
	path, err := GlobalIgnorePath(context.Background())
	if err != nil || path != custom {
		t.Fatalf("path = %s, want %s, err = %v", path, custom, err)
	}
	for _, value := range []string{"0", "00", "+0", "0k", "False", "No", "OFF"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", value)
			path, err := GlobalIgnorePath(context.Background())
			if err != nil || path != custom {
				t.Fatalf("path = %s, want %s, err = %v", path, custom, err)
			}
		})
	}
}

func TestGlobalIgnorePathRejectsInvalidNoSystemConfig(t *testing.T) {
	testInstallation(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "definitely")
	if _, err := GlobalIgnorePath(context.Background()); err == nil || !strings.Contains(err.Error(), "GIT_CONFIG_NOSYSTEM") {
		t.Fatalf("invalid no-system value accepted: %v", err)
	}
}
