package overlayruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func removalWorktree(t *testing.T) (string, string) {
	t.Helper()
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared rules\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private rules\n")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\nCLAUDE.local.md\n.env\ndata/\n")
	git(t, repo, "add", "AGENTS.md", "CLAUDE.md", ".gitignore")
	seedCommit(t, repo)
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"remove-test"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: exit=%d out=%q err=%q", code, out, errb)
	}
	return repo, strings.TrimSpace(out)
}

func removalAttempt(t *testing.T, repo, target string, rollback bool) (int, string, string) {
	t.Helper()
	if !rollback {
		return run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
	}
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	leftovers := discardWorktree(r, target, "agents-overlay/remove-test")
	code := 0
	if len(leftovers) > 0 {
		code = 1
	}
	return code, strings.Join(leftovers, "\n"), ""
}

func TestWorktreeRemovalPreservesUserData(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*testing.T, string, string) (string, string)
		reason  string
	}{
		{"ignored-env", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, ".env")
			const body = "TOKEN=placeholder\n\x00\xff"
			write(t, path, body)
			return path, body
		}, "ignored user file"},
		{"ignored-data", func(t *testing.T, repo, target string) (string, string) {
			dir := filepath.Join(target, "data", "nested")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "records.bin")
			const body = "\x00\xffsaved records\n"
			write(t, path, body)
			return path, body
		}, "ignored user file"},
		{"changed-generated", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "CLAUDE.local.md")
			body := regressionRead(t, path) + "user addition\n"
			write(t, path, body)
			return path, body
		}, "differs from its current source"},
		{"changed-source", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "CLAUDE.local.md")
			body := regressionRead(t, path)
			write(t, filepath.Join(repo, "AGENTS.local.md"), "updated private rules\n")
			return path, body
		}, "differs from its current source"},
		{"orphan-generated", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "CLAUDE.local.md")
			source := filepath.Join(repo, "AGENTS.local.md")
			if err := os.Rename(source, source+".saved"); err != nil {
				t.Fatal(err)
			}
			return path, regressionRead(t, path)
		}, "AGENTS.local.md"},
		{"generated-symlink", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "CLAUDE.local.md")
			body := regressionRead(t, path)
			saved := filepath.Join(t.TempDir(), "saved")
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(saved, path); err != nil {
				t.Fatal(err)
			}
			return path, body
		}, "not a regular file"},
		{"source-symlink", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "CLAUDE.local.md")
			source := filepath.Join(repo, "AGENTS.local.md")
			if err := os.Rename(source, source+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source+".saved", source); err != nil {
				t.Fatal(err)
			}
			return path, regressionRead(t, path)
		}, "not a regular file"},
		{"unreadable-source", func(t *testing.T, repo, target string) (string, string) {
			if os.Getuid() == 0 {
				t.Skip("root can read mode-000 files")
			}
			source := filepath.Join(repo, "AGENTS.local.md")
			regressionChmod(t, source, 0)
			t.Cleanup(func() { regressionChmod(t, source, 0o600) })
			path := filepath.Join(target, "CLAUDE.local.md")
			return path, regressionRead(t, path)
		}, "permission denied"},
		{"non-utf8-name", func(t *testing.T, repo, target string) (string, string) {
			dir := filepath.Join(target, "data")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "saved-\xff")
			const body = "saved bytes\x00\xff"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EILSEQ) || errors.Is(err, syscall.EINVAL) {
					t.Skipf("filesystem rejected a non-UTF8 filename: %v", err)
				}
				t.Fatal(err)
			}
			return path, body
		}, "non-UTF8 name"},
		{"unowned-marker-only", func(t *testing.T, repo, target string) (string, string) {
			dir := filepath.Join(target, "data", "generated")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "agents-local-overlay.md")
			const body = "<!-- generated by agents-local-overlay; do not edit -->\nuser rules\n"
			write(t, path, body)
			return path, body
		}, "ignored user file"},
		{"ordinary-untracked", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "notes.txt")
			const body = "unfinished work\n"
			write(t, path, body)
			return path, body
		}, "modified or untracked files"},
		{"tracked-modification", func(t *testing.T, repo, target string) (string, string) {
			path := filepath.Join(target, "AGENTS.md")
			const body = "# changed rules\n"
			write(t, path, body)
			return path, body
		}, "modified or untracked files"},
	}
	for _, rollback := range []bool{false, true} {
		operation := "remove"
		if rollback {
			operation = "rollback"
		}
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				repo, target := removalWorktree(t)
				path, body := tc.prepare(t, repo, target)
				registration := git(t, repo, "worktree", "list", "--porcelain")
				branch := git(t, repo, "rev-parse", "refs/heads/agents-overlay/remove-test")
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				code, out, errb := removalAttempt(t, repo, target, rollback)
				if code != 1 || !strings.Contains(out+errb, tc.reason) {
					t.Fatalf("refusal: exit=%d out=%q err=%q, want %q", code, out, errb, tc.reason)
				}
				if got := regressionRead(t, path); got != body {
					t.Fatalf("user data changed: got %q, want %q", got, body)
				}
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(info, after) || info.Mode() != after.Mode() {
					t.Fatalf("user file replaced or mode changed: %v", err)
				}
				if got := git(t, repo, "worktree", "list", "--porcelain"); got != registration {
					t.Fatalf("worktree registration changed: %q", got)
				}
				if got := git(t, repo, "rev-parse", "refs/heads/agents-overlay/remove-test"); got != branch {
					t.Fatalf("branch changed: %q", got)
				}
			})
		}
	}
}

func TestWorktreeRemovalGeneratedOnly(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		operation := "remove"
		if rollback {
			operation = "rollback"
		}
		t.Run(operation, func(t *testing.T) {
			repo, target := removalWorktree(t)
			if got := regressionRead(t, filepath.Join(target, "CLAUDE.local.md")); !strings.HasSuffix(got, "private rules\n") {
				t.Fatalf("missing generated copy: %q", got)
			}
			code, out, errb := removalAttempt(t, repo, target, rollback)
			if code != 0 {
				t.Fatalf("remove: exit=%d out=%q err=%q", code, out, errb)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("worktree still exists: %v", err)
			}
			if got := git(t, repo, "worktree", "list", "--porcelain"); strings.Contains(got, target) {
				t.Fatalf("worktree still registered: %q", got)
			}
			if got := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/agents-overlay/remove-test"); got != "" {
				t.Fatalf("branch still exists: %q", got)
			}
			if got := regressionRead(t, filepath.Join(repo, "AGENTS.local.md")); got != "private rules\n" {
				t.Fatalf("primary source changed: %q", got)
			}
		})
	}
}

func TestWorktreeRemovalPreservesIgnoredDirectories(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		operation := "remove"
		if rollback {
			operation = "rollback"
		}
		for _, unreadable := range []bool{false, true} {
			name, reason := "empty", "ignored user directory"
			if unreadable {
				name, reason = "unreadable", "permission denied"
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				if unreadable && os.Getuid() == 0 {
					t.Skip("root can inspect mode-000 directories")
				}
				repo, target := removalWorktree(t)
				dir := filepath.Join(target, "data")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "records.bin")
				if unreadable {
					write(t, path, "saved data\x00\xff")
					regressionChmod(t, dir, 0)
					t.Cleanup(func() { regressionChmod(t, dir, 0o700) })
				}
				registration := git(t, repo, "worktree", "list", "--porcelain")
				branch := git(t, repo, "rev-parse", "refs/heads/agents-overlay/remove-test")
				code, out, errb := removalAttempt(t, repo, target, rollback)
				if code != 1 || !strings.Contains(out+errb, reason) {
					t.Fatalf("refusal: exit=%d out=%q err=%q, want %q", code, out, errb, reason)
				}
				if _, err := os.Lstat(dir); err != nil {
					t.Fatalf("ignored directory lost: %v", err)
				}
				if unreadable {
					regressionChmod(t, dir, 0o700)
					if got := regressionRead(t, path); got != "saved data\x00\xff" {
						t.Fatalf("ignored file changed: %q", got)
					}
				}
				if got := git(t, repo, "worktree", "list", "--porcelain"); got != registration {
					t.Fatalf("worktree registration changed: %q", got)
				}
				if got := git(t, repo, "rev-parse", "refs/heads/agents-overlay/remove-test"); got != branch {
					t.Fatalf("branch changed: %q", got)
				}
			})
		}
	}
}
