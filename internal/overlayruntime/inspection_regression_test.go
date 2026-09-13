package overlayruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRemovalPreservesHiddenTrackedChanges(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		operation := "remove"
		if rollback {
			operation = "rollback"
		}
		for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
			t.Run(operation+"/"+flag, func(t *testing.T) {
				repo, target := removalWorktree(t)
				git(t, target, "update-index", flag, "--", "AGENTS.md")
				path := filepath.Join(target, "AGENTS.md")
				const body = "# unpublished user edits\n"
				write(t, path, body)
				if got := git(t, target, "status", "--porcelain"); got != "" {
					t.Fatalf("Git did not hide the tracked change: %q", got)
				}
				registration := git(t, repo, "worktree", "list", "--porcelain")
				code, out, errb := removalAttempt(t, repo, target, rollback)
				if code != 1 || !strings.Contains(out+errb, "index flags") {
					t.Fatalf("hidden change accepted: exit=%d out=%q err=%q", code, out, errb)
				}
				if got := regressionRead(t, path); got != body {
					t.Fatalf("hidden change lost: %q", got)
				}
				if got := git(t, repo, "worktree", "list", "--porcelain"); got != registration {
					t.Fatalf("worktree registration changed: %q", got)
				}
				git(t, repo, "rev-parse", "--verify", "refs/heads/agents-overlay/remove-test")
			})
		}
	}
}

func TestRuntimeReadsSymlinkedCodexConfig(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	target := filepath.Join(t.TempDir(), "codex.toml")
	const content = "project_doc_max_bytes = 32768\n"
	write(t, target, content)
	config := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if err := os.Symlink(target, config); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setup", "check"} {
		code, out, errb := run(t, "", op, "--runtime=codex", repo)
		if code != 0 {
			t.Fatalf("%s rejected config link: exit=%d out=%q err=%q", op, code, out, errb)
		}
		if got := regressionRead(t, target); got != content {
			t.Fatalf("config changed: %q", got)
		}
		if _, err := os.Readlink(config); err != nil {
			t.Fatalf("config symlink replaced: %v", err)
		}
	}
}

func TestRuntimeChecksClaudeNativeDisableEnvironment(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
	if code, out, errb := run(t, "", "setup", "--runtime=claude", repo); code != 0 {
		t.Fatalf("setup: exit=%d out=%q err=%q", code, out, errb)
	}
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
	for _, runtime := range []string{"claude", "all", "codex"} {
		for _, op := range []string{"check", "setup"} {
			code, out, errb := run(t, "", op, "--runtime="+runtime, repo)
			if runtime == "codex" {
				if code != 0 {
					t.Fatalf("Claude environment blocked Codex %s: exit=%d out=%q err=%q", op, code, out, errb)
				}
			} else if code != 1 || !strings.Contains(out+errb, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1") {
				t.Fatalf("disabled Claude %s accepted: exit=%d out=%q err=%q", op, code, out, errb)
			}
		}
	}
}

func TestRuntimeRejectsClaudeSettingsWithoutObjectRoot(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	for _, content := range []string{"null", "[]", "false", "1"} {
		write(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), content)
		code, out, errb := run(t, "", "check", "--runtime=claude", repo)
		if code != 1 || !strings.Contains(out+errb, "must contain a JSON object") {
			t.Fatalf("accepted %s: exit=%d out=%q err=%q", content, code, out, errb)
		}
	}
}

func TestRuntimeReadsSymlinkedClaudeSettings(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	target := filepath.Join(t.TempDir(), "claude.json")
	const content = "{\"claudeMdExcludes\":[]}\n"
	write(t, target, content)
	config := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	if err := os.Symlink(target, config); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setup", "check"} {
		code, out, errb := run(t, "", op, "--runtime=claude", repo)
		if code != 0 {
			t.Fatalf("%s rejected config link: exit=%d out=%q err=%q", op, code, out, errb)
		}
		if got := regressionRead(t, target); got != content {
			t.Fatalf("config changed: %q", got)
		}
		if _, err := os.Readlink(config); err != nil {
			t.Fatalf("config symlink replaced: %v", err)
		}
	}
}
