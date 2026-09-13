package agentinstructions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func testInstallation(t *testing.T) *Installation {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	i, err := NewInstallation(filepath.Join(dir, "bin", "quota-cli"), InstallTargets{filepath.Join(dir, "claude", "settings.json"), filepath.Join(dir, "codex", "hooks.json"), filepath.Join(dir, "codex", "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func installWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func installWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	installWrite(t, path, string(b))
}
func TestInstallationLifecycle(t *testing.T) {
	i := testInstallation(t)
	unrelated := map[string]any{"type": "command", "command": "echo 'agent instructions _hook'", "timeout": 12}
	installWriteJSON(t, i.targets.ClaudeSettings, map[string]any{"theme": "custom", "hooks": map[string]any{"SessionStart": []any{map[string]any{"matcher": "startup", "hooks": []any{unrelated}}}}})
	plan, err := i.Plan([]string{"claude", "codex"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(i.targets.CodexHooks); !os.IsNotExist(err) {
		t.Fatal("plan created account files")
	}
	result, err := i.Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("applied: %+v", result)
	}
	statuses, err := i.Inspect(plan.Agents)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if !s.Configured {
			t.Fatalf("not configured: %+v", s)
		}
	}
	claude, err := agenthooks.ReadJSONObject(i.targets.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	if claude["theme"] != "custom" {
		t.Fatal("settings lost")
	}
	initial, _ := os.ReadFile(i.targets.ClaudeSettings)
	again, err := i.Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Applied) != 0 {
		t.Fatalf("not idempotent: %+v", again)
	}
	saved, _ := os.ReadFile(i.targets.ClaudeSettings)
	if string(initial) != string(saved) {
		t.Fatal("second setup rewrote settings")
	}
	uninstall, err := i.Plan(plan.Agents, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Apply(uninstall); err != nil {
		t.Fatal(err)
	}
	claude, err = agenthooks.ReadJSONObject(i.targets.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"theme": "custom", "hooks": map[string]any{"SessionStart": []any{map[string]any{"matcher": "startup", "hooks": []any{unrelated}}}}}
	if !installEqual(claude, want) {
		t.Fatalf("unrelated hooks changed: %#v", claude)
	}
	statuses, _ = i.Inspect(plan.Agents)
	for _, s := range statuses {
		if s.Agent == "claude" && s.Configured {
			t.Fatalf("uninstalled reported configured: %+v", s)
		}
	}
}
func TestInstallationPreflightsEveryTarget(t *testing.T) {
	i := testInstallation(t)
	installWrite(t, i.targets.CodexConfig, "[invalid")
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude", "codex"}}); err == nil {
		t.Fatal("invalid TOML accepted")
	}
	if _, err := os.Stat(i.targets.ClaudeSettings); !os.IsNotExist(err) {
		t.Fatal("Claude settings written before Codex preflight")
	}
}
func TestInstallationRejectsUnknownOwnership(t *testing.T) {
	i := testInstallation(t)
	for _, command := range []string{"/other/quota-cli agent instructions _hook --agent=codex --event=SessionStart", `env X=value sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`} {
		installWriteJSON(t, i.targets.CodexHooks, map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
		if _, err := i.Plan([]string{"codex"}, false); err == nil {
			t.Fatalf("unknown command accepted: %s", command)
		}
	}
}
func TestInstallationInspectRejectsLegacyCodexHooks(t *testing.T) {
	cases := []struct {
		name   string
		modify func(map[string]any)
	}{
		{"disabled", func(r map[string]any) { r["disableAllHooks"] = true }},
		{"matcher", func(r map[string]any) {
			r["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)["matcher"] = "startup"
		}},
		{"async", func(r map[string]any) {
			r["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["async"] = true
		}},
		{"limit", func(r map[string]any) {
			r["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["additionalContextLimit"] = 20
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			i := testInstallation(t)
			root := map[string]any{"hooks": map[string]any{"SessionStart": []any{i.desired("codex", "SessionStart")}}}
			tt.modify(root)
			installWriteJSON(t, i.targets.CodexHooks, root)
			s, err := i.Inspect([]string{"codex"})
			if err != nil {
				t.Fatal(err)
			}
			if s[0].Configured || len(s[0].Problems) == 0 {
				t.Fatalf("invalid installation accepted: %+v", s)
			}
		})
	}
}
func TestInstallationApplyRechecksChangedPlan(t *testing.T) {
	i := testInstallation(t)
	plan, err := i.Plan([]string{"claude"}, false)
	if err != nil {
		t.Fatal(err)
	}
	installWrite(t, i.targets.ClaudeSettings, `{"newSetting":"preserve"}`)
	if _, err := i.Apply(plan); err != nil {
		t.Fatal(err)
	}
	root, err := agenthooks.ReadJSONObject(i.targets.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	if root["newSetting"] != "preserve" {
		t.Fatal("stale plan erased later edit")
	}
}
func TestInstallationResolvesAccountSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "actual")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	i, err := NewInstallation("/example/quota-cli", InstallTargets{filepath.Join(link, "claude.json"), filepath.Join(link, "hooks.json"), filepath.Join(link, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("account symlink replaced")
	}
}
func TestCurrentInstallTargetsUsesNativeHomesWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "account-c"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "account-x"))
	targets, err := CurrentInstallTargets()
	if err != nil {
		t.Fatal(err)
	}
	if targets.ClaudeSettings != filepath.Join(dir, "account-c", "settings.json") || targets.CodexHooks != filepath.Join(dir, "account-x", "hooks.json") {
		t.Fatalf("wrong native targets: %+v", targets)
	}
	if os.Getenv("HOME") != dir || !strings.HasSuffix(os.Getenv("CODEX_HOME"), "account-x") {
		t.Fatal("environment changed")
	}
}

func TestInstallationPartialFailureReportsSavedFilesAndRetry(t *testing.T) {
	i := testInstallation(t)
	installWriteJSON(t, i.targets.CodexHooks, map[string]any{"hooks": map[string]any{"SessionStart": []any{i.desired("codex", "SessionStart")}}})
	if err := os.MkdirAll(i.targets.CodexHooks+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := i.Apply(InstallPlan{Agents: []string{"claude", "codex"}})
	if err == nil {
		t.Fatal("lock obstruction accepted")
	}
	if len(result.Applied) != 1 || result.Applied[0] != i.targets.ClaudeSettings || result.FailedPath != i.targets.CodexHooks {
		t.Fatalf("partial report inaccurate: %+v", result)
	}
	status, _ := i.Inspect([]string{"claude"})
	if !status[0].Configured {
		t.Fatalf("saved Claude missing: %+v", status)
	}
	if err := os.Remove(i.targets.CodexHooks + ".lock"); err != nil {
		t.Fatal(err)
	}
	result, err = i.Apply(InstallPlan{Agents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || result.Applied[0] != i.targets.CodexHooks {
		t.Fatalf("retry did not converge: %+v", result)
	}
}

func TestInstallationSelectedAgentIgnoresUnrelatedBrokenHome(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken")
	if err := os.Symlink(broken, broken); err != nil {
		t.Fatal(err)
	}
	targets := InstallTargets{filepath.Join(dir, "claude", "settings.json"), filepath.Join(broken, "hooks.json"), filepath.Join(broken, "config.toml")}
	i, err := NewInstallation("/example/quota-cli", targets)
	if err != nil {
		t.Fatalf("constructor accessed unselected account: %v", err)
	}
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude"}}); err != nil {
		t.Fatalf("unselected Codex home blocked Claude: %v", err)
	}
	statuses, err := i.Inspect([]string{"claude"})
	if err != nil || !statuses[0].Configured {
		t.Fatalf("Claude inspection failed: %+v %v", statuses, err)
	}
	if _, err := i.Plan([]string{"claude", "codex"}, false); err == nil {
		t.Fatal("all accepted broken Codex home")
	}
}

func TestInstallationAllResolvesTargetsBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken")
	if err := os.Symlink(broken, broken); err != nil {
		t.Fatal(err)
	}
	targets := InstallTargets{filepath.Join(dir, "claude", "settings.json"), filepath.Join(broken, "hooks.json"), filepath.Join(broken, "config.toml")}
	i, err := NewInstallation("/example/quota-cli", targets)
	if err != nil {
		t.Fatalf("constructor accessed account: %v", err)
	}
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude", "codex"}}); err == nil {
		t.Fatal("all accepted broken Codex home")
	}
	if _, err := os.Stat(targets.ClaudeSettings); !os.IsNotExist(err) {
		t.Fatal("all wrote Claude before Codex target validation")
	}
}

func TestInstallationSelectedTargetsRejectAliasesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared.json")
	alias := filepath.Join(dir, "alias.json")
	installWrite(t, target, "{}")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	i, err := NewInstallation("/example/quota-cli", InstallTargets{target, alias, filepath.Join(dir, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Plan([]string{"claude"}, false); err != nil {
		t.Fatalf("unselected alias blocked Claude: %v", err)
	}
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude", "codex"}}); err == nil {
		t.Fatal("selected aliases accepted")
	}
	saved, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "{}" {
		t.Fatal("alias preflight wrote shared target")
	}
}

func TestInstallationPreservesUnrelatedProgramsWithInstructionArguments(t *testing.T) {
	commands := []string{"echo agent instructions _hook", "/opt/unrelated/auditor agent instructions _hook", "echo agent overlay hook", "/opt/unrelated/auditor agent overlay hook"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			i := testInstallation(t)
			unrelated := map[string]any{"type": "command", "command": command}
			original := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{unrelated}}}}}
			installWriteJSON(t, i.targets.CodexHooks, original)
			if _, err := i.Apply(InstallPlan{Agents: []string{"codex"}}); err != nil {
				t.Fatalf("unrelated program blocked installation: %v", err)
			}
			status, err := i.Inspect([]string{"codex"})
			if err != nil || !status[0].Configured {
				t.Fatalf("unrelated program blocked inspection: %+v %v", status, err)
			}
			if _, err := i.Apply(InstallPlan{Agents: []string{"codex"}, Uninstall: true}); err != nil {
				t.Fatal(err)
			}
			saved, err := agenthooks.ReadJSONObject(i.targets.CodexHooks)
			if err != nil {
				t.Fatal(err)
			}
			if !installEqual(saved, original) {
				t.Fatalf("unrelated hook changed: %#v", saved)
			}
		})
	}
}
func TestSuspiciousInstructionCommandRecognizesKnownExecutable(t *testing.T) {
	command := "/example/custom-binary agent instructions _hook --agent=codex --event=SessionStart"
	if suspiciousInstructionCommand(command) {
		t.Fatal("unknown executable inferred as quota")
	}
	if !suspiciousInstructionCommand(command, "/example/custom-binary") {
		t.Fatal("explicit known executable missed")
	}
	if !suspiciousInstructionCommand("/example/quota-cli agent instructions _hook --agent=codex --event=SessionStart") {
		t.Fatal("quota executable missed")
	}
}

func TestCodexNativeInspectionPreservesJSONNumbers(t *testing.T) {
	i := testInstallation(t)
	const body = `{"unrelated":{"large":9007199254740993,"precise":1.00000000000000001}}`
	installWrite(t, i.targets.CodexHooks, body)
	for n := 0; n < 2; n++ {
		plan, err := i.Plan([]string{"codex"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := i.Apply(plan); err != nil {
			t.Fatal(err)
		}
		statuses, err := i.Inspect([]string{"codex"})
		if err != nil || len(statuses) != 1 || !statuses[0].Configured {
			t.Fatalf("unrelated JSON numbers diagnosed as instruction hooks: %+v %v", statuses, err)
		}
		saved, err := os.ReadFile(i.targets.CodexHooks)
		if err != nil || string(saved) != body {
			t.Fatalf("unrelated numeric values changed: %s %v", saved, err)
		}
	}
}
