package overlayruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeCreatePreparesSharedBridge(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared rules\n")
	git(t, repo, "add", "AGENTS.md")
	seedCommit(t, repo)
	if code, out, errb := run(t, "", "setup", "--runtime=claude", repo); code != 0 {
		t.Fatalf("initial setup: exit=%d out=%q err=%q", code, out, errb)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"bridge-check"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: exit=%d out=%q err=%q", code, out, errb)
	}
	linked := strings.TrimSpace(out)
	code, out, errb = run(t, "", "check", "--runtime=claude", linked)
	if code != 0 {
		t.Fatalf("prepared bridge check: exit=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := run(t, "", "setup", "--runtime=claude", linked); code != 0 {
		t.Fatalf("worktree setup: exit=%d out=%q err=%q", code, out, errb)
	}
	if got := regressionRead(t, filepath.Join(linked, "CLAUDE.md")); got != "@AGENTS.md\n" {
		t.Fatalf("shared bridge = %q", got)
	}
}
