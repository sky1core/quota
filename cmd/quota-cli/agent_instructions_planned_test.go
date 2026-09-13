package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionsSetupRepairsPlannedClaudeSettings(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("real Codex CLI is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, linked := filepath.Join(root, "repo"), filepath.Join(root, "linked")
	bin, home := filepath.Join(root, "bin"), filepath.Join(root, "home")
	for _, path := range []string{repo, bin, home, filepath.Join(home, ".codex"), filepath.Join(repo, ".claude")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"git": git, "sh": "/bin/sh", "codex": codex} {
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	runGit("init", "-q")
	runGit("config", "user.name", "Instructions Test")
	runGit("config", "user.email", "instructions@example.invalid")
	write(filepath.Join(repo, "AGENTS.md"), "shared instruction\n")
	write(filepath.Join(repo, "AGENTS.local.md"), "local instruction\n")
	runGit("add", "AGENTS.md")
	runGit("commit", "-q", "-m", "seed")
	runGit("worktree", "add", "-q", "--detach", linked, "HEAD")
	write(filepath.Join(repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/.claude/settings.json\n")
	source := filepath.Join(repo, ".claude", "settings.json")
	copy := filepath.Join(linked, ".claude", "settings.json")
	write(source, "{invalid")
	call := func(args ...string) instructionsReport {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runAgentInstructions(append(args, "--json"), nil, &stdout, &stderr)
		var report instructionsReport
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || code > 1 {
			t.Fatalf("%v: exit=%d, err=%v, out=%s, stderr=%s", args, code, err, &stdout, &stderr)
		}
		return report
	}
	if report := call("setup", repo, "--agent=codex", "--local-file=.claude/settings.json"); report.Error != "" {
		t.Fatal(report.Error)
	}
	if read(copy) != "{invalid" {
		t.Fatal("Codex setup did not copy the registered file")
	}
	write(source, "{}")
	if report := call("setup", repo, "--agent=claude", "--dry-run"); report.Error != "" {
		t.Fatalf("planned recovery rejected: %s", report.Error)
	}
	if read(copy) != "{invalid" {
		t.Fatal("dry-run changed the managed copy")
	}
	if report := call("setup", repo, "--agent=claude"); report.Error != "" {
		t.Fatalf("recovery rejected: %s", report.Error)
	}
	if read(copy) != "{}" || read(source) != "{}" {
		t.Fatal("managed settings were not repaired or the source changed")
	}
	write(filepath.Join(repo, "AGENTS.md"), "")
	write(filepath.Join(linked, "AGENTS.md"), "")
	write(filepath.Join(repo, "AGENTS.local.md"), "{}")
	if report := call("setup", repo, "--agent=codex"); report.Error != "" {
		t.Fatal(report.Error)
	}
	if err := os.Symlink("../AGENTS.override.md", filepath.Join(linked, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	hookBody, err := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart CLAUDE.md CLAUDE.local.md . claude-session`}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(repo, "AGENTS.local.md"), string(hookBody))
	before := map[string]string{}
	for _, path := range []string{filepath.Join(repo, ".git", "quota-instructions.json"), filepath.Join(linked, "AGENTS.override.md"), filepath.Join(linked, "AGENTS.local.md"), filepath.Join(home, ".claude", "settings.json")} {
		before[path] = read(path)
	}
	if report := call("setup", repo, "--agent=all"); !strings.Contains(report.Error, "conflicts with the fixed account installation") {
		t.Fatalf("all-runtime hook conflict was missed: %+v", report)
	}
	for path, body := range before {
		if read(path) != body {
			t.Fatalf("hook conflict changed %s", path)
		}
	}
	if report := call("setup", repo, "--agent=codex"); report.Error != "" {
		t.Fatal(report.Error)
	}
	write(filepath.Join(repo, "AGENTS.local.md"), "{}")
	if report := call("setup", repo, "--agent=all"); report.Error != "" {
		t.Fatalf("all-runtime hook repair read the old merged body: %s", report.Error)
	}
	write(copy, `{"userEdited":true}`)
	write(source, `{"sourceChanged":true}`)
	if report := call("setup", repo, "--agent=all"); !strings.Contains(report.Error, "user edits or unknown ownership") {
		t.Fatalf("user edit was not protected: %+v", report)
	}
	if read(copy) != `{"userEdited":true}` {
		t.Fatal("user settings edit was overwritten")
	}
}
