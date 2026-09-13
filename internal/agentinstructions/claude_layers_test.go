package agentinstructions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func claudeLayerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
func claudeLayerRepo(t *testing.T) (*Installation, string) {
	t.Helper()
	i := testInstallation(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(i.targets.ClaudeSettings))
	t.Setenv("CODEX_HOME", filepath.Dir(i.targets.CodexHooks))
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claudeLayerGit(t, repo, "init", "-q")
	return i, repo
}
func claudeLayerHook(command string) map[string]any {
	return map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
}
func TestClaudeRepositoryInstructionHookConflicts(t *testing.T) {
	for _, file := range []string{"settings.json", "settings.local.json"} {
		for _, kind := range []string{"same command", "legacy", "unknown executable path", "unsupported event"} {
			t.Run(file+"/"+kind, func(t *testing.T) {
				i, repo := claudeLayerRepo(t)
				if _, err := i.Apply(InstallPlan{Agents: []string{"claude"}}); err != nil {
					t.Fatal(err)
				}
				command := i.command("claude", "SessionStart")
				switch kind {
				case "legacy":
					command = `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart CLAUDE.md CLAUDE.local.md . claude-session`
				case "unknown executable path":
					command = "/example/quota-cli agent instructions _hook --agent=claude --event=SessionStart"
				}
				root := claudeLayerHook(command)
				if kind == "unsupported event" {
					hooks := root["hooks"].(map[string]any)
					hooks["SubagentStart"] = hooks["SessionStart"]
					delete(hooks, "SessionStart")
				}
				path := filepath.Join(repo, ".claude", file)
				installWriteJSON(t, path, root)
				before, _ := os.ReadFile(path)
				problems, err := i.InspectClaudeRepositoryHooks(context.Background(), repo)
				if err != nil {
					t.Fatal(err)
				}
				if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), path) {
					t.Fatalf("project instruction hook not diagnosed: %v", problems)
				}
				if kind == "same command" && !strings.Contains(strings.Join(problems, "\n"), "2 instruction entries") {
					t.Fatalf("merged duplicate count missing: %v", problems)
				}
				saved, _ := os.ReadFile(path)
				if string(saved) != string(before) {
					t.Fatal("inspection changed project settings")
				}
			})
		}
	}
}
func TestClaudeRepositoryHookPreflightAllowsAccountMigration(t *testing.T) {
	i, repo := claudeLayerRepo(t)
	installWriteJSON(t, i.targets.ClaudeSettings, claudeLayerHook(`sh "$HOME/.local/bin/agents-overlay-context" json SessionStart CLAUDE.md CLAUDE.local.md . claude-session`))
	problems, err := i.InspectClaudeRepositoryHooks(context.Background(), repo)
	if err != nil || len(problems) != 0 {
		t.Fatalf("account migration blocked by project preflight: %v %v", problems, err)
	}
}
func TestClaudeRepositoryHooksPreserveUnrelatedCommands(t *testing.T) {
	i, repo := claudeLayerRepo(t)
	for _, command := range []string{"echo agent instructions _hook", "/opt/unrelated/auditor agent instructions _hook", "echo 'agents-overlay-context'"} {
		path := filepath.Join(repo, ".claude", "settings.local.json")
		installWriteJSON(t, path, claudeLayerHook(command))
		problems, err := i.InspectClaudeRepositoryHooks(context.Background(), repo)
		if err != nil || len(problems) != 0 {
			t.Fatalf("unrelated command blocked: %s %v %v", command, problems, err)
		}
	}
}
func TestClaudeRepositoryHookLayersIncludeNestedAndWorktreeSettings(t *testing.T) {
	i, repo := claudeLayerRepo(t)
	installWrite(t, filepath.Join(repo, "AGENTS.md"), "shared instruction\n")
	claudeLayerGit(t, repo, "add", "AGENTS.md")
	for key, value := range map[string]string{"user.name": "Example", "user.email": "example@example.invalid"} {
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			claudeLayerGit(t, repo, "config", "--local", key, value)
		}
	}
	claudeLayerGit(t, repo, "commit", "-q", "-m", "seed")
	linked := filepath.Join(t.TempDir(), "linked")
	claudeLayerGit(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{nested, linked} {
		installWriteJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), claudeLayerHook(i.command("claude", "SessionStart")))
	}
	problems, err := i.InspectClaudeRepositoryHooks(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(problems, "\n")
	for _, dir := range []string{nested, linked} {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(joined, filepath.Join(resolved, ".claude", "settings.local.json")) {
			t.Fatalf("active layer missed: %s\n%s", dir, joined)
		}
	}
}
func TestClaudeRepositoryHookLayerRejectsMalformedAndFIFO(t *testing.T) {
	for _, kind := range []string{"empty", "array", "malformed hooks", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			i, repo := claudeLayerRepo(t)
			path := filepath.Join(repo, ".claude", "settings.json")
			switch kind {
			case "empty":
				installWrite(t, path, "")
			case "array":
				installWrite(t, path, "[]")
			case "malformed hooks":
				installWrite(t, path, `{"hooks":{"SessionStart":{}}}`)
			case "fifo":
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			problems, err := i.InspectClaudeRepositoryHooks(context.Background(), repo)
			if err == nil && len(problems) == 0 {
				t.Fatal("invalid project settings reported ready")
			}
		})
	}
}

func TestClaudeRepositoryHookAliasRemainsSeparateActiveLayer(t *testing.T) {
	i, repo := claudeLayerRepo(t)
	if _, err := i.Apply(InstallPlan{Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(i.targets.ClaudeSettings, project); err != nil {
		t.Fatal(err)
	}
	problems, err := i.InspectClaudeRepositoryHooks(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), project) {
		t.Fatalf("project alias concealed active duplicate hook: %v", problems)
	}
}
