package overlayruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeCreateRejectsRelativeBaseBeforeMutation(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	git(t, repo, "add", "AGENTS.md")
	seedCommit(t, repo)
	registration := git(t, repo, "worktree", "list", "--porcelain")
	refs := git(t, repo, "show-ref")
	caller := t.TempDir()
	t.Chdir(caller)
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", "worktrees")
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"relative-base"}`, "claude-worktree-create")
	if code != 1 || !strings.Contains(out+errb, "must be an absolute path") {
		t.Fatalf("relative base accepted: exit=%d out=%q err=%q", code, out, errb)
	}
	for _, dir := range []string{repo, caller} {
		if _, err := os.Lstat(filepath.Join(dir, "worktrees")); !os.IsNotExist(err) {
			t.Fatalf("relative base caused filesystem mutation in %s: %v", dir, err)
		}
	}
	if got := git(t, repo, "worktree", "list", "--porcelain"); got != registration {
		t.Fatalf("worktree registration changed: %q", got)
	}
	if got := git(t, repo, "show-ref"); got != refs {
		t.Fatalf("branch refs changed: %q", got)
	}
}
