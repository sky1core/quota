package overlayruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func untrackedInstructionWorktree(t *testing.T) (string, string) {
	t.Helper()
	repo := newRepo(t)
	git(t, repo, "rm", "--cached", "AGENTS.md")
	git(t, repo, "commit", "-qm", "untrack instructions")
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-qb", "linked", linked)
	return repo, linked
}

func prepare(t *testing.T, dir, agent string) error {
	t.Helper()
	return PrepareDelegationInstructions(context.Background(), dir, agent, false)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestDelegationPreparesUntrackedInstructionsToMatchPrimary(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			home := testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			write(t, filepath.Join(repo, "AGENTS.md"), "primary root\n")
			write(t, filepath.Join(repo, "sub", "AGENTS.md"), "primary nested\n")
			write(t, filepath.Join(repo, "other", "AGENTS.md"), "primary other\n")
			write(t, filepath.Join(repo, "sub", "deep", "AGENTS.md"), "primary deep\n")
			write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\nvendor/\n")
			write(t, filepath.Join(repo, "vendor", "pkg", "AGENTS.md"), "primary vendored\n")
			start := filepath.Join(linked, "sub")
			if err := os.Mkdir(start, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := prepare(t, start, agent); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(linked, "AGENTS.md")
			nested := filepath.Join(start, "AGENTS.md")
			if got := readFile(t, root); got != "primary root\n" {
				t.Fatalf("root = %q", got)
			}
			if got := readFile(t, nested); got != "primary nested\n" {
				t.Fatalf("nested = %q", got)
			}
			for rel, want := range map[string]string{"other/AGENTS.md": "primary other\n", "sub/deep/AGENTS.md": "primary deep\n", "vendor/pkg/AGENTS.md": "primary vendored\n"} {
				if got := readFile(t, filepath.Join(linked, filepath.FromSlash(rel))); got != want {
					t.Fatalf("%s = %q", rel, got)
				}
			}
			if info, err := os.Lstat(root); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("prepared file is not regular: %v %v", info, err)
			}
			if _, err := os.Lstat(filepath.Join(home, ".config", "quota", "instructions")); !os.IsNotExist(err) {
				t.Fatalf("preparation recorded state: %v", err)
			}
			past := time.Now().Add(-time.Hour)
			if err := os.Chtimes(root, past, past); err != nil {
				t.Fatal(err)
			}
			if err := prepare(t, start, agent); err != nil {
				t.Fatal(err)
			}
			if info, _ := os.Stat(root); !info.ModTime().Equal(past) {
				t.Fatal("matching worktree instructions were rewritten")
			}
			write(t, root, "stale copy\n")
			if err := prepare(t, start, agent); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, root); got != "primary root\n" {
				t.Fatalf("stale copy kept: %q", got)
			}
			write(t, filepath.Join(repo, "AGENTS.md"), "primary root v2\n")
			if err := prepare(t, linked, agent); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, root); got != "primary root v2\n" {
				t.Fatalf("changed primary not mirrored: %q", got)
			}
			if err := os.Remove(filepath.Join(repo, "sub", "AGENTS.md")); err != nil {
				t.Fatal(err)
			}
			if err := prepare(t, start, agent); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, nested); got != "primary nested\n" {
				t.Fatalf("worktree file changed after primary removal: %q", got)
			}
		})
	}
}

func TestDelegationLeavesTrackedInstructionsToGit(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-qb", "linked", linked)
	git(t, linked, "rm", "-q", "AGENTS.md")
	git(t, linked, "commit", "-qm", "branch without instructions")
	write(t, filepath.Join(repo, "AGENTS.md"), "modified tracked primary\n")
	for _, agent := range []string{"claude", "codex"} {
		if err := prepare(t, linked, agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(linked, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("tracked primary instructions were copied into a checkout without them: %v", err)
	}

	repo, linked = untrackedInstructionWorktree(t)
	target := filepath.Join(linked, "AGENTS.md")
	write(t, target, "tracked in worktree\n")
	git(t, linked, "add", "AGENTS.md")
	git(t, linked, "commit", "-qm", "track instructions in worktree")
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "tracked in worktree\n" {
		t.Fatalf("tracked worktree file overwritten: %q", got)
	}
	if out := git(t, linked, "status", "--porcelain"); out != "" {
		t.Fatalf("worktree became dirty: %q", out)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("deleted tracked worktree file was recreated from primary: %v", err)
	}
	write(t, target, "invalid tracked\x00content")
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatalf("tracked worktree file was validated: %v", err)
	}
}

func TestDelegationPreservesCaseRenamedTrackedTargets(t *testing.T) {
	for _, tc := range []struct{ indexed, target string }{
		{"AGENTS.md", "AGENTS.md"},
		{"agents.md", "AGENTS.md"},
		{"docs/AGENTS.md", "Docs/AGENTS.md"},
	} {
		t.Run(tc.indexed, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			git(t, repo, "config", "extensions.worktreeConfig", "true")
			git(t, repo, "config", "--worktree", "core.ignorecase", "false")
			git(t, linked, "config", "--worktree", "core.ignorecase", "true")
			write(t, filepath.Join(repo, tc.target), "primary instructions\n")
			indexed, target := filepath.Join(linked, tc.indexed), filepath.Join(linked, tc.target)
			write(t, indexed, "committed target\n")
			git(t, linked, "add", "--", tc.indexed)
			git(t, linked, "commit", "-qm", "track worktree instructions")
			if tc.indexed != tc.target {
				from, to := indexed, target
				if filepath.Dir(indexed) != filepath.Dir(target) {
					from, to = filepath.Dir(indexed), filepath.Dir(target)
				}
				if err := os.Rename(from, to); err != nil {
					t.Fatal(err)
				}
			}
			write(t, target, "uncommitted target edit\n")
			for _, agent := range []string{"claude", "codex"} {
				if err := prepare(t, linked, agent); err != nil {
					t.Fatal(err)
				}
				if got := readFile(t, target); got != "uncommitted target edit\n" {
					t.Fatalf("tracked target edit overwritten: %q", got)
				}
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := prepare(t, linked, "codex"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("deleted tracked target recreated: %v", err)
			}
		})
	}
}

func TestDelegationPreservesCaseRenamedTargetGitlinks(t *testing.T) {
	for _, dir := range []string{"vendor/pkg", "vendor/Pkg", "Vendor/Pkg"} {
		t.Run(dir, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			git(t, repo, "config", "extensions.worktreeConfig", "true")
			git(t, repo, "config", "--worktree", "core.ignorecase", "false")
			git(t, linked, "config", "--worktree", "core.ignorecase", "true")
			rel := filepath.Join(dir, "sub", "AGENTS.md")
			write(t, filepath.Join(repo, rel), "primary nested instructions\n")
			sha := strings.TrimSpace(git(t, linked, "rev-parse", "HEAD"))
			git(t, linked, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/pkg")
			if err := os.MkdirAll(filepath.Join(linked, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := prepare(t, linked, "codex"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(linked, rel)); !os.IsNotExist(err) {
				t.Fatalf("instructions created inside target gitlink: %v", err)
			}
			if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
				t.Fatalf("root instructions not prepared: %q", got)
			}
		})
	}
}

func TestDelegationExcludesTargetBareRepositoriesUnderExplicitPolicy(t *testing.T) {
	for _, policy := range []string{"all", "explicit"} {
		t.Run(policy, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			rel := filepath.Join("vendor", "pkg", "AGENTS.md")
			write(t, filepath.Join(repo, rel), "primary nested instructions\n")
			write(t, filepath.Join(repo, "vendor", "pkg", "docs", "AGENTS.md"), "primary docs\n")
			bare := filepath.Join(linked, "vendor", "pkg")
			git(t, linked, "init", "-q", "--bare", bare)
			write(t, filepath.Join(bare, "AGENTS.md"), "foreign instructions\n")
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
			t.Setenv("GIT_CONFIG_VALUE_0", policy)
			if err := prepare(t, linked, "codex"); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(bare, "AGENTS.md")); got != "foreign instructions\n" {
				t.Fatalf("nested bare instructions changed: %q", got)
			}
			if _, err := os.Lstat(filepath.Join(bare, "docs")); !os.IsNotExist(err) {
				t.Fatalf("directory created inside the nested bare repository: %v", err)
			}
			if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
				t.Fatalf("root instructions not prepared: %q", got)
			}
		})
	}
}

func TestDelegationRejectsFIFOCommonDirectoryWithoutWaiting(t *testing.T) {
	for _, location := range []string{"primary without instructions", "primary with instructions", "target"} {
		t.Run(location, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			dir := filepath.Join(repo, "fixture")
			if location == "target" {
				write(t, filepath.Join(dir, "AGENTS.md"), "primary fixture\n")
				dir = filepath.Join(linked, "fixture")
			}
			write(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
			if location != "primary without instructions" {
				write(t, filepath.Join(dir, "AGENTS.md"), "fixture instructions\n")
			}
			common := filepath.Join(dir, "commondir")
			if err := syscall.Mkfifo(common, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := PrepareDelegationInstructions(ctx, linked, "codex", false)
			if ctx.Err() != nil {
				t.Fatal("preparation blocked on commondir until the context expired")
			}
			if err == nil || !strings.Contains(err.Error(), common) {
				t.Fatalf("FIFO commondir was not reported: %v", err)
			}
			if location == "target" {
				if got := readFile(t, filepath.Join(dir, "AGENTS.md")); got != "fixture instructions\n" {
					t.Fatalf("target overwritten after failed repository check: %q", got)
				}
			} else if _, err := os.Lstat(filepath.Join(linked, "AGENTS.md")); !os.IsNotExist(err) {
				t.Fatalf("instructions copied after failed primary inspection: %v", err)
			}
		})
	}
}

func TestDelegationChecksCommonDirectoryBehindGitMarkers(t *testing.T) {
	for _, location := range []string{"primary", "target"} {
		for _, layout := range []string{"directory", "absolute gitfile", "relative gitfile", "relative gitfile through symlink"} {
			for _, fifo := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/fifo=%t", location, layout, fifo), func(t *testing.T) {
					testHome(t)
					repo, linked := untrackedInstructionWorktree(t)
					fixture := filepath.Join(repo, "fixture")
					if location == "target" {
						write(t, filepath.Join(fixture, "AGENTS.md"), "primary fixture\n")
						fixture = filepath.Join(linked, "fixture")
						write(t, filepath.Join(fixture, "AGENTS.md"), "foreign instructions\n")
					}
					gitdir := filepath.Join(fixture, ".git")
					if layout != "directory" {
						gitdir = filepath.Join(t.TempDir(), "metadata")
						ref := gitdir
						if layout == "relative gitfile through symlink" {
							child := filepath.Join(gitdir, "child")
							if err := os.MkdirAll(child, 0o755); err != nil {
								t.Fatal(err)
							}
							ref = filepath.Join(t.TempDir(), "link")
							if err := os.Symlink(child, ref); err != nil {
								t.Fatal(err)
							}
						}
						if strings.HasPrefix(layout, "relative gitfile") {
							var err error
							ref, err = filepath.Rel(fixture, ref)
							if err != nil {
								t.Fatal(err)
							}
						}
						if layout == "relative gitfile through symlink" {
							ref += string(os.PathSeparator) + ".."
						}
						write(t, filepath.Join(fixture, ".git"), "gitdir: "+ref+"\r\n")
					}
					git(t, repo, "init", "-q", "--bare", gitdir)
					common := filepath.Join(gitdir, "commondir")
					if fifo {
						if err := syscall.Mkfifo(common, 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						write(t, common, ".\n")
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					err := PrepareDelegationInstructions(ctx, linked, "codex", false)
					if ctx.Err() != nil {
						t.Fatal("preparation blocked on commondir behind a Git marker")
					}
					if fifo {
						if err == nil || !strings.Contains(err.Error(), "commondir") {
							t.Fatalf("FIFO commondir was not reported: %v", err)
						}
					} else if err != nil {
						t.Fatalf("normal nested repository failed preparation: %v", err)
					}
					if location == "target" {
						if got := readFile(t, filepath.Join(fixture, "AGENTS.md")); got != "foreign instructions\n" {
							t.Fatalf("foreign instructions overwritten: %q", got)
						}
					} else if _, err := os.Lstat(filepath.Join(linked, "fixture")); !os.IsNotExist(err) {
						t.Fatalf("instructions prepared inside a nested repository: %v", err)
					}
				})
			}
		}
	}
}

func TestDelegationAcceptsNativeRepositoryMetadata(t *testing.T) {
	for _, layout := range []string{"bare", "directory", "gitfile"} {
		for _, metadata := range []string{"NUL in commondir", "unreadable HEAD referent"} {
			t.Run(layout+"/"+metadata, func(t *testing.T) {
				if metadata == "unreadable HEAD referent" && os.Geteuid() == 0 {
					t.Skip("file permissions do not restrict root")
				}
				testHome(t)
				repo, linked := untrackedInstructionWorktree(t)
				for _, root := range []string{repo, linked} {
					name := "source"
					if root == linked {
						name = "target"
					}
					fixture := filepath.Join(root, name)
					write(t, filepath.Join(fixture, "AGENTS.md"), "foreign instructions\n")
					gitdir, suspect := fixture, fixture
					if layout == "directory" {
						gitdir = filepath.Join(fixture, ".git")
						suspect = gitdir
					} else if layout == "gitfile" {
						gitdir = filepath.Join(t.TempDir(), "metadata")
						suspect = filepath.Join(fixture, ".git")
						write(t, suspect, "gitdir: "+gitdir+"\n")
					}
					git(t, repo, "init", "-q", "--bare", gitdir)
					if metadata == "NUL in commondir" {
						write(t, filepath.Join(gitdir, "commondir"), ".\x00\n")
					} else {
						ref := filepath.Join(gitdir, "refs", "heads", "main")
						write(t, ref, strings.Repeat("0", 40)+"\n")
						if err := os.Chmod(ref, 0); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { os.Chmod(ref, 0o644) })
						head := filepath.Join(gitdir, "HEAD")
						if err := os.Remove(head); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink("refs/heads/main", head); err != nil {
							t.Fatal(err)
						}
					}
					git(t, repo, "rev-parse", "--resolve-git-dir", suspect)
				}
				write(t, filepath.Join(repo, "source", "sub", "AGENTS.md"), "foreign child\n")
				write(t, filepath.Join(repo, "target", "AGENTS.md"), "primary target\n")
				write(t, filepath.Join(repo, "target", "sub", "AGENTS.md"), "primary child\n")
				if err := prepare(t, linked, "codex"); err != nil {
					t.Fatalf("native-valid nested repository blocked preparation: %v", err)
				}
				if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
					t.Fatalf("root instructions not prepared: %q", got)
				}
				if got := readFile(t, filepath.Join(linked, "target", "AGENTS.md")); got != "foreign instructions\n" {
					t.Fatalf("foreign instructions changed: %q", got)
				}
				for _, rel := range []string{"source", "target/sub"} {
					if _, err := os.Lstat(filepath.Join(linked, rel)); !os.IsNotExist(err) {
						t.Fatalf("foreign instructions copied: %s: %v", rel, err)
					}
				}
			})
		}
	}
}

func TestDelegationDoesNotValidateTrackedPrimaryInstructions(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	path := filepath.Join(repo, "AGENTS.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "tracked target\n")
	if err := os.Symlink("CLAUDE.md", path); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "AGENTS.md", "CLAUDE.md")
	git(t, repo, "commit", "-qm", "track symlink instructions")
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-qb", "linked", linked)
	for _, agent := range []string{"claude", "codex"} {
		for _, createsWorktree := range []bool{false, true} {
			if err := PrepareDelegationInstructions(context.Background(), linked, agent, createsWorktree); err != nil {
				t.Fatalf("%s createsWorktree=%t: tracked primary symlink blocked delegation: %v", agent, createsWorktree, err)
			}
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, "tracked regular\n")
	git(t, repo, "add", "AGENTS.md")
	git(t, repo, "commit", "-qm", "track regular instructions")
	write(t, path, "\xff")
	for _, createsWorktree := range []bool{false, true} {
		if err := PrepareDelegationInstructions(context.Background(), repo, "codex", createsWorktree); err != nil {
			t.Fatalf("createsWorktree=%t: modified tracked primary file was validated: %v", createsWorktree, err)
		}
	}
	if link, err := os.Readlink(filepath.Join(linked, "AGENTS.md")); err != nil || link != "CLAUDE.md" {
		t.Fatalf("tracked worktree symlink changed: %q %v", link, err)
	}
}

func TestDelegationRejectsUnpreparableTargets(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	source := filepath.Join(repo, "AGENTS.md")
	target := filepath.Join(linked, "AGENTS.md")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	err := prepare(t, linked, "claude")
	if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory target accepted: %v", err)
	}
	write(t, filepath.Join(target, "child.md"), "tracked child\n")
	git(t, linked, "add", "AGENTS.md/child.md")
	err = prepare(t, linked, "codex")
	if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory target with a tracked child accepted: %v", err)
	}
	git(t, linked, "rm", "-qf", "AGENTS.md/child.md")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("directory target remained after removing its tracked child: %v", err)
	}
	elsewhere := filepath.Join(linked, "elsewhere.md")
	write(t, elsewhere, "linked content\n")
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatal(err)
	}
	err = prepare(t, linked, "codex")
	if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink target accepted: %v", err)
	}
	if link, err := os.Readlink(target); err != nil || link != elsewhere || readFile(t, elsewhere) != "linked content\n" {
		t.Fatalf("symlink target modified: %q %v", link, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "newdir", "AGENTS.md"), "new directory\n")
	write(t, filepath.Join(linked, "newdir"), "a file where the directory belongs\n")
	err = prepare(t, linked, "claude")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(linked, "newdir", "AGENTS.md")) || !strings.Contains(err.Error(), filepath.Join(repo, "newdir", "AGENTS.md")) {
		t.Fatalf("file blocking the target directory accepted: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "newdir")); got != "a file where the directory belongs\n" {
		t.Fatalf("blocking file modified: %q", got)
	}
	if err := os.Remove(filepath.Join(linked, "newdir")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, "newdir")); err != nil {
		t.Fatal(err)
	}
	err = prepare(t, linked, "codex")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(linked, "newdir")) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked parent directory accepted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("instructions written outside the worktree: %v", err)
	}
}

func TestDelegationLeavesNestedRepositoryFilesToThatRepository(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, nested string)
		want  string
	}{
		{"tracked", func(t *testing.T, nested string) {
			write(t, filepath.Join(nested, "AGENTS.md"), "nested repository\n")
			git(t, nested, "add", "AGENTS.md")
			git(t, nested, "commit", "-qm", "nested")
		}, "nested repository\n"},
		{"untracked", func(t *testing.T, nested string) {
			write(t, filepath.Join(nested, "AGENTS.md"), "nested untracked\n")
		}, "nested untracked\n"},
		{"ignored", func(t *testing.T, nested string) {
			write(t, filepath.Join(nested, ".gitignore"), "AGENTS.md\n")
			write(t, filepath.Join(nested, "AGENTS.md"), "nested ignored\n")
		}, "nested ignored\n"},
		{"absent", func(t *testing.T, nested string) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			write(t, filepath.Join(repo, "vendor", "pkg", "AGENTS.md"), "primary vendored\n")
			nested := filepath.Join(linked, "vendor", "pkg")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			git(t, nested, "init", "-q")
			tc.setup(t, nested)
			if err := prepare(t, linked, "claude"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(nested, "AGENTS.md"))
			switch {
			case tc.want == "" && !os.IsNotExist(err):
				t.Fatalf("file created inside the nested repository: %q %v", got, err)
			case tc.want != "" && (err != nil || string(got) != tc.want):
				t.Fatalf("nested repository file changed: %q %v", got, err)
			}
			if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
				t.Fatalf("root not prepared: %q", got)
			}
		})
	}
}

func TestDelegationSkipsSymlinksInsideANestedRepository(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "vendor", "pkg", "docs", "AGENTS.md"), "primary vendored docs\n")
	nested := filepath.Join(linked, "vendor", "pkg")
	if err := os.MkdirAll(filepath.Join(nested, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q")
	if err := os.Symlink(filepath.Join(nested, "real"), filepath.Join(nested, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatalf("symlink inside the nested repository failed the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(nested, "real", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("file written through the nested repository's symlink: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
		t.Fatalf("root not prepared: %q", got)
	}
}

func TestDelegationSkipsSymlinksInsideANestedBareRepository(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "vendor", "pkg", "docs", "AGENTS.md"), "primary vendored docs\n")
	bare := filepath.Join(linked, "vendor", "pkg")
	if err := os.MkdirAll(filepath.Join(bare, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "-q", "--bare")
	if err := os.Symlink(filepath.Join(bare, "real"), filepath.Join(bare, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatalf("symlink inside the nested bare repository failed the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(bare, "real", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("file written through the bare repository's symlink: %v", err)
	}
}

func TestDelegationLeavesNestedBareRepositoryFilesToThatRepository(t *testing.T) {
	t.Run("primary worktree", func(t *testing.T) {
		testHome(t)
		repo, linked := untrackedInstructionWorktree(t)
		git(t, repo, "init", "-q", "--bare", "mirror.git")
		write(t, filepath.Join(repo, "mirror.git", "AGENTS.md"), "mirror instructions\n")
		if err := prepare(t, linked, "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(linked, "mirror.git")); !os.IsNotExist(err) {
			t.Fatalf("nested bare repository file copied into the worktree: %v", err)
		}
		if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
			t.Fatalf("root not prepared: %q", got)
		}
	})
	t.Run("worktree created after launch", func(t *testing.T) {
		testHome(t)
		repo := newRepo(t)
		git(t, repo, "init", "-q", "--bare", "mirror.git")
		write(t, filepath.Join(repo, "mirror.git", "AGENTS.md"), "mirror instructions\n")
		if err := PrepareDelegationInstructions(context.Background(), repo, "codex", true); err != nil {
			t.Fatalf("nested bare repository file blocked worktree creation: %v", err)
		}
	})
	t.Run("bare primary", func(t *testing.T) {
		testHome(t)
		repo := newRepo(t)
		bare := filepath.Join(filepath.Dir(repo), "bare.git")
		git(t, repo, "clone", "-q", "--bare", repo, bare)
		bare = resolvePath(bare)
		checkout := filepath.Join(filepath.Dir(repo), "checkout")
		git(t, bare, "worktree", "add", "-q", checkout, "main")
		git(t, bare, "init", "-q", "--bare", "mirror.git")
		write(t, filepath.Join(bare, "mirror.git", "AGENTS.md"), "mirror instructions\n")
		write(t, filepath.Join(bare, "sub", "AGENTS.md"), "bare nested\n")
		if err := prepare(t, checkout, "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(checkout, "mirror.git")); !os.IsNotExist(err) {
			t.Fatalf("nested bare repository file copied into the checkout: %v", err)
		}
		if got := readFile(t, filepath.Join(checkout, "sub", "AGENTS.md")); got != "bare nested\n" {
			t.Fatalf("bare nested = %q", got)
		}
	})
}

func TestDelegationReportsBrokenNestedRepositoryMetadata(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "docs", "AGENTS.md"), "primary docs\n")
	write(t, filepath.Join(linked, "docs", ".git"), "gitdir: "+filepath.Join(linked, "missing-gitdir")+"\n")
	err := prepare(t, linked, "claude")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(linked, "docs", "AGENTS.md")) {
		t.Fatalf("broken nested repository metadata was not reported: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(linked, "docs", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("file written below broken repository metadata: %v", err)
	}
}

func TestDelegationSkipsBareRepositoryAtTheTargetPath(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "vendor", "pkg", "AGENTS.md"), "primary vendored\n")
	bare := filepath.Join(linked, "vendor", "pkg")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "-q", "--bare")
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatalf("bare repository at the target path failed the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(bare, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("file created inside the bare repository: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
		t.Fatalf("root not prepared: %q", got)
	}
}

func TestDelegationLeavesNestedWorktreesAndSubmodulePathsAlone(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "inner", "AGENTS.md"), "primary inner\n")
	write(t, filepath.Join(repo, "vendor", "pkg", "AGENTS.md"), "primary vendored\n")
	inner := filepath.Join(linked, "inner")
	git(t, repo, "worktree", "add", "-qb", "inner", inner)
	sha := strings.TrimSpace(git(t, linked, "rev-parse", "HEAD"))
	git(t, linked, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/pkg")
	if err := os.MkdirAll(filepath.Join(linked, "vendor", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{filepath.Join("inner", "AGENTS.md"), filepath.Join("vendor", "pkg", "AGENTS.md")} {
		if _, err := os.Lstat(filepath.Join(linked, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s was prepared inside another worktree or a submodule path: %v", rel, err)
		}
	}
	if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
		t.Fatalf("root not prepared: %q", got)
	}
}

func TestDelegationLeavesOnlyInstructionFilesInTheWorktree(t *testing.T) {
	home := testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "src", "AGENTS.md"), "primary src\n")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-qm", "ignore instructions")
	git(t, linked, "checkout", "-q", "--detach", strings.TrimSpace(git(t, repo, "rev-parse", "HEAD")))
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(linked, "src", "AGENTS.md")); got != "primary src\n" {
		t.Fatalf("nested not prepared: %q", got)
	}
	if status := git(t, linked, "status", "--porcelain"); status != "" {
		t.Fatalf("preparation left files in the worktree:\n%s", status)
	}
	var stray []string
	filepath.WalkDir(linked, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), ".quota") {
			stray = append(stray, path)
		}
		return nil
	})
	if len(stray) != 0 {
		t.Fatalf("preparation left quota files in the worktree: %v", stray)
	}
	locks, err := filepath.Glob(filepath.Join(home, ".config", "quota", "instruction-locks", "*.lock"))
	if err != nil || len(locks) == 0 {
		t.Fatalf("lock files not kept under the quota state directory: %v %v", locks, err)
	}
}

func TestDelegationRejectsWorktreeCreatedAfterLaunch(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	for _, agent := range []string{"claude", "codex"} {
		if err := PrepareDelegationInstructions(context.Background(), repo, agent, true); err != nil {
			t.Fatalf("tracked instructions blocked worktree creation: %v", err)
		}
	}
	git(t, repo, "rm", "--cached", "AGENTS.md")
	git(t, repo, "commit", "-qm", "untrack instructions")
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-qb", "linked", linked)
	write(t, filepath.Join(linked, "AGENTS.md"), "already prepared\n")
	source := filepath.Join(repo, "AGENTS.md")
	for _, agent := range []string{"claude", "codex"} {
		for _, start := range []string{repo, linked} {
			err := PrepareDelegationInstructions(context.Background(), start, agent, true)
			if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), "--worktree") {
				t.Fatalf("%s from %s: untracked instructions allowed before worktree creation: %v", agent, start, err)
			}
		}
	}
	git(t, repo, "add", "AGENTS.md")
	git(t, repo, "commit", "-qm", "track root")
	nested := filepath.Join(repo, "sub", "AGENTS.md")
	write(t, nested, "nested\n")
	for _, start := range []string{filepath.Dir(nested), repo} {
		err := PrepareDelegationInstructions(context.Background(), start, "codex", true)
		if err == nil || !strings.Contains(err.Error(), nested) {
			t.Fatalf("untracked nested instructions allowed from %s: %v", start, err)
		}
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDelegationInstructions(context.Background(), filepath.Dir(nested), "codex", true); err != nil {
		t.Fatalf("absent instructions blocked: %v", err)
	}
}

func TestDelegationPreparesFromBarePrimary(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	bare := filepath.Join(filepath.Dir(repo), "bare.git")
	git(t, repo, "clone", "-q", "--bare", repo, bare)
	bare = resolvePath(bare)
	checkout := filepath.Join(filepath.Dir(repo), "checkout")
	git(t, bare, "worktree", "add", "-q", checkout, "main")
	git(t, bare, "branch", "AGENTS.md")
	write(t, filepath.Join(bare, "remotes", "AGENTS.md"), "URL: legacy remote\n")
	write(t, filepath.Join(bare, "sub", "AGENTS.md"), "bare nested\n")
	start := filepath.Join(checkout, "sub")
	if err := os.Mkdir(start, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, start, "claude"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(start, "AGENTS.md")); got != "bare nested\n" {
		t.Fatalf("bare nested = %q", got)
	}
	if got := readFile(t, filepath.Join(checkout, "AGENTS.md")); got != "# shared placeholder\n" {
		t.Fatalf("tracked checkout root changed: %q", got)
	}
	for _, rel := range []string{"refs/heads/AGENTS.md", "remotes/AGENTS.md"} {
		if _, err := os.Lstat(filepath.Join(checkout, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("bare repository internals %s copied as instructions: %v", rel, err)
		}
	}
}

func TestDelegationFailsWhenPrimaryCannotBeListedCompletely(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "sub", "AGENTS.md"), "hidden nested\n")
	if err := os.Chmod(filepath.Join(repo, "sub"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(repo, "sub"), 0o755) })
	for _, createsWorktree := range []bool{false, true} {
		err := PrepareDelegationInstructions(context.Background(), linked, "codex", createsWorktree)
		if err == nil || !strings.Contains(err.Error(), "sub") {
			t.Fatalf("createsWorktree=%t: unreadable primary directory ignored: %v", createsWorktree, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(linked, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("preparation proceeded despite an incomplete listing: %v", err)
	}
	if err := os.Chmod(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.fsyncObjectFiles", "true")
	t.Setenv("GIT_TRACE", "1")
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatalf("git diagnostics on stderr failed the delegation: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "sub", "AGENTS.md")); got != "hidden nested\n" {
		t.Fatalf("nested not prepared after the directory became readable: %q", got)
	}
}

func TestDelegationRejectsInvalidInstructionSources(t *testing.T) {
	for _, file := range []string{"AGENTS.md", "AGENTS.local.md", "sub/AGENTS.md"} {
		kinds := []string{"symlink", "invalid UTF-8", "NUL"}
		if file == "AGENTS.local.md" {
			kinds = append(kinds, "directory", "directory with tracked child")
		}
		for _, kind := range kinds {
			t.Run(file+"/"+kind, func(t *testing.T) {
				testHome(t)
				repo := newRepo(t)
				dir := filepath.Join(repo, "sub")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(repo, file)
				if file == "AGENTS.md" {
					git(t, repo, "rm", "-q", "AGENTS.md")
					git(t, repo, "commit", "-qm", "untrack instructions")
				}
				switch kind {
				case "directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "directory with tracked child":
					write(t, filepath.Join(path, "child.md"), "tracked child\n")
					git(t, repo, "add", "-f", filepath.Join(file, "child.md"))
				case "symlink":
					write(t, filepath.Join(repo, "other"), "instructions\n")
					if err := os.Symlink(filepath.Join(repo, "other"), path); err != nil {
						t.Fatal(err)
					}
				case "invalid UTF-8":
					write(t, path, "\xff")
				case "NUL":
					write(t, path, "text\x00text")
				}
				for _, agent := range []string{"claude", "codex"} {
					for _, createsWorktree := range []bool{false, true} {
						err := PrepareDelegationInstructions(context.Background(), dir, agent, createsWorktree)
						if err == nil || !strings.Contains(err.Error(), path) {
							t.Fatalf("%s createsWorktree=%t: invalid source accepted: %v", agent, createsWorktree, err)
						}
					}
				}
			})
		}
	}
}

func TestDelegationClaudeLimitAndOptionalLocalInstructions(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	for _, agent := range []string{"claude", "codex"} {
		if err := prepare(t, repo, agent); err != nil {
			t.Fatalf("optional local absence rejected: %v", err)
		}
	}
	path := filepath.Join(repo, "AGENTS.local.md")
	write(t, path, strings.Repeat("가", 10000))
	if err := prepare(t, repo, "claude"); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("Claude limit not rejected: %v", err)
	}
	if err := prepare(t, repo, "codex"); err != nil {
		t.Fatalf("Claude-only limit applied to Codex: %v", err)
	}
}

func TestDelegationDoesNotRedirectOrHideInspectionFailures(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, alias, "codex"); err != nil {
		t.Fatal(err)
	}
	t.Run("Git override", func(t *testing.T) {
		for name, value := range map[string]string{"GIT_WORK_TREE": repo, "GIT_LITERAL_PATHSPECS": "1", "GIT_GLOB_PATHSPECS": "1", "GIT_NOGLOB_PATHSPECS": "1", "GIT_ICASE_PATHSPECS": "1"} {
			t.Setenv(name, value)
			if err := prepare(t, repo, "codex"); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s override accepted: %v", name, err)
			}
			os.Unsetenv(name)
		}
	})
	t.Run("missing Git", func(t *testing.T) {
		t.Setenv("PATH", "")
		if err := prepare(t, repo, "codex"); err == nil {
			t.Fatal("Git inspection failure accepted")
		}
		if err := prepare(t, t.TempDir(), "codex"); err != nil {
			t.Fatalf("non-repository execution acquired a Git dependency: %v", err)
		}
	})
}

func TestDelegationFailsWhenBarePrimaryCannotBeListedCompletely(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	testHome(t)
	repo := newRepo(t)
	bare := filepath.Join(filepath.Dir(repo), "bare.git")
	git(t, repo, "clone", "-q", "--bare", repo, bare)
	bare = resolvePath(bare)
	checkout := filepath.Join(filepath.Dir(repo), "checkout")
	git(t, bare, "worktree", "add", "-q", checkout, "main")
	write(t, filepath.Join(bare, "private", "AGENTS.md"), "hidden nested\n")
	write(t, filepath.Join(bare, "sub", "AGENTS.md"), "bare nested\n")
	private := filepath.Join(bare, "private")
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(private, 0o755) })
	if err := prepare(t, checkout, "claude"); err == nil || !strings.Contains(err.Error(), private) {
		t.Fatalf("unreadable bare primary directory ignored: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(checkout, "sub", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("preparation proceeded despite an incomplete listing: %v", err)
	}
}

func TestDelegationDoesNotDiscoverRepositoriesBelowThePrimary(t *testing.T) {
	explicitBareRepositories := func(t *testing.T, bare string) {
		t.Helper()
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
		t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
		if _, stderr, err := gitOutputWithStderr(context.Background(), bare, "rev-parse", "--git-dir"); err == nil || !strings.Contains(stderr, "safe.bareRepository") {
			t.Skipf("git did not refuse implicit bare repository discovery: %q %v", stderr, err)
		}
	}
	t.Run("bare primary", func(t *testing.T) {
		testHome(t)
		repo := newRepo(t)
		bare := filepath.Join(filepath.Dir(repo), "bare.git")
		git(t, repo, "clone", "-q", "--bare", repo, bare)
		bare = resolvePath(bare)
		checkout := filepath.Join(filepath.Dir(repo), "checkout")
		git(t, bare, "worktree", "add", "-q", checkout, "main")
		git(t, bare, "init", "-q", "--bare", "mirror.git")
		write(t, filepath.Join(bare, "mirror.git", "AGENTS.md"), "mirror instructions\n")
		write(t, filepath.Join(bare, "sub", "AGENTS.md"), "bare nested\n")
		explicitBareRepositories(t, bare)
		if err := prepare(t, checkout, "claude"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(checkout, "mirror.git")); !os.IsNotExist(err) {
			t.Fatalf("nested bare repository file copied into the checkout: %v", err)
		}
		if got := readFile(t, filepath.Join(checkout, "sub", "AGENTS.md")); got != "bare nested\n" {
			t.Fatalf("bare nested = %q", got)
		}
	})
	t.Run("primary worktree", func(t *testing.T) {
		testHome(t)
		repo, linked := untrackedInstructionWorktree(t)
		git(t, repo, "init", "-q", "--bare", "mirror.git")
		write(t, filepath.Join(repo, "mirror.git", "AGENTS.md"), "mirror instructions\n")
		write(t, filepath.Join(repo, "sub", "AGENTS.md"), "primary sub\n")
		explicitBareRepositories(t, filepath.Join(repo, "mirror.git"))
		if err := prepare(t, linked, "codex"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(linked, "mirror.git")); !os.IsNotExist(err) {
			t.Fatalf("nested bare repository file copied into the worktree: %v", err)
		}
		if got := readFile(t, filepath.Join(linked, "sub", "AGENTS.md")); got != "primary sub\n" {
			t.Fatalf("primary sub = %q", got)
		}
		if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
			t.Fatalf("root not prepared: %q", got)
		}
	})
}

func TestDelegationIgnoresUnreadableInternalsOfNestedBareRepository(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	git(t, repo, "init", "-q", "--bare", "mirror.git")
	unreadable := filepath.Join(repo, "mirror.git", "objects", "unreadable")
	if err := os.MkdirAll(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o755) })
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatalf("unreadable directory inside a nested bare repository blocked the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(linked, "mirror.git")); !os.IsNotExist(err) {
		t.Fatalf("nested bare repository copied into the worktree: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "AGENTS.md")); got != "# shared placeholder\n" {
		t.Fatalf("root not prepared: %q", got)
	}
}

func TestDelegationReportsInvalidRepositoryMarkersInThePrimary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"empty .git directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"gitdir link to a missing directory", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(dir, "missing")+"\n")
		}},
		{"gitdir link without the gitdir prefix", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".git"), "not a gitdir link\n")
		}},
		{"dangling .git symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			dir := filepath.Join(repo, "vendor", "pkg")
			write(t, filepath.Join(dir, "AGENTS.md"), "behind an invalid marker\n")
			tc.setup(t, dir)
			marker := filepath.Join(dir, ".git")
			if err := prepare(t, linked, "claude"); err == nil || !strings.Contains(err.Error(), marker) {
				t.Fatalf("invalid repository marker accepted: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(linked, "AGENTS.md")); !os.IsNotExist(err) {
				t.Fatalf("preparation proceeded despite a failed repository check: %v", err)
			}
		})
	}
}

func TestDelegationDistinguishesRepositoryLayoutsFromPlainDirectories(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "docs", "HEAD"), "not a ref\n")
	write(t, filepath.Join(repo, "docs", "objects"), "plain file\n")
	write(t, filepath.Join(repo, "docs", "refs"), "plain file\n")
	write(t, filepath.Join(repo, "docs", "AGENTS.md"), "primary docs\n")
	if err := os.Mkdir(filepath.Join(repo, "docs", "commondir"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "stale", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(repo, "stale", "commondir"), "../..\n")
	write(t, filepath.Join(repo, "stale", "AGENTS.md"), "stale metadata\n")
	write(t, filepath.Join(repo, "wt", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(repo, "wt", "commondir"), "../.git\n")
	write(t, filepath.Join(repo, "wt", "AGENTS.md"), "worktree metadata\n")
	if err := os.MkdirAll(filepath.Join(repo, "unborn", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "unborn", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("refs", "heads", "nothere"), filepath.Join(repo, "unborn", "HEAD")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "unborn", "AGENTS.md"), "unborn repository\n")
	git(t, repo, "init", "-q", "--bare", "mirror.git")
	write(t, filepath.Join(repo, "crlf", ".git"), "gitdir: ../mirror.git\r\n")
	write(t, filepath.Join(repo, "crlf", "AGENTS.md"), "behind a CRLF gitfile\n")
	if err := os.MkdirAll(filepath.Join(repo, "badhead", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "badhead", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "badhead", "HEAD"), "not a ref\n")
	write(t, filepath.Join(repo, "badhead", "AGENTS.md"), "invalid HEAD content\n")
	if err := os.MkdirAll(filepath.Join(repo, "fifo", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "fifo", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(repo, "fifo", "HEAD"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "fifo", "AGENTS.md"), "HEAD is a fifo\n")
	done := make(chan error, 1)
	go func() { done <- prepare(t, linked, "codex") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("preparation blocked on a repository-like directory")
	}
	for rel, want := range map[string]string{"docs": "primary docs\n", "stale": "stale metadata\n", "badhead": "invalid HEAD content\n", "fifo": "HEAD is a fifo\n"} {
		if got := readFile(t, filepath.Join(linked, rel, "AGENTS.md")); got != want {
			t.Fatalf("plain directory %s skipped: %q", rel, got)
		}
	}
	for _, rel := range []string{"wt", "unborn", "crlf", "mirror.git"} {
		if _, err := os.Lstat(filepath.Join(linked, rel)); !os.IsNotExist(err) {
			t.Fatalf("repository %s copied as instructions: %v", rel, err)
		}
	}
}

func TestDelegationLeavesCaseRenamedSourceGitlinkPathsAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	if strings.TrimSpace(git(t, repo, "config", "core.ignorecase")) != "true" {
		t.Skip("core.ignorecase is not enabled")
	}
	sha := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/pkg")
	write(t, filepath.Join(repo, "vendor", "Pkg", "AGENTS.md"), "inside a gitlink\n")
	if _, err := os.Stat(filepath.Join(repo, "vendor", "pkg", "AGENTS.md")); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	private := filepath.Join(repo, "vendor", "Pkg", "private")
	if err := os.Mkdir(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(private, 0o755) })
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatalf("case-renamed gitlink path blocked the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(linked, "vendor")); !os.IsNotExist(err) {
		t.Fatalf("file below a gitlink copied into the worktree: %v", err)
	}
}

func TestDelegationLeavesSourceGitlinkPathsAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	sha := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/pkg")
	write(t, filepath.Join(repo, "vendor", "pkg", "AGENTS.md"), "inside a gitlink\n")
	private := filepath.Join(repo, "vendor", "pkg", "private")
	if err := os.Mkdir(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(private, 0o755) })
	if err := prepare(t, linked, "claude"); err != nil {
		t.Fatalf("gitlink path in the primary index blocked the delegation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(linked, "vendor")); !os.IsNotExist(err) {
		t.Fatalf("file below a gitlink copied into the worktree: %v", err)
	}
	if err := PrepareDelegationInstructions(context.Background(), linked, "claude", true); err == nil || !strings.Contains(err.Error(), filepath.Join(repo, "AGENTS.md")) {
		t.Fatalf("root untracked instructions must still block worktree creation: %v", err)
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDelegationInstructions(context.Background(), linked, "claude", true); err != nil {
		t.Fatalf("gitlink contents counted as untracked instructions: %v", err)
	}
}

func TestDelegationTreatsCaseInsensitiveTrackedInstructionsAsTracked(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "sub", "agents.md"), "tracked lower\n")
	if _, err := os.Stat(filepath.Join(repo, "sub", "AGENTS.md")); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	if strings.TrimSpace(git(t, repo, "config", "core.ignorecase")) != "true" {
		t.Skip("core.ignorecase is not enabled")
	}
	git(t, repo, "add", "sub/agents.md")
	git(t, repo, "commit", "-qm", "lowercase instructions")
	if err := os.Remove(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "sub", "agents.md"), filepath.Join(repo, "sub", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	git(t, linked, "merge", "-q", "--ff-only", "main")
	if err := PrepareDelegationInstructions(context.Background(), linked, "codex", true); err != nil {
		t.Fatalf("case-renamed tracked file counted as untracked: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "sub", "agents.md")); got != "tracked lower\n" {
		t.Fatalf("tracked file changed: %q", got)
	}
}

func TestDelegationReportsUnreadableRepositoryLayoutsInThePrimary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not restrict root")
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) string
	}{
		{"unreadable HEAD", func(t *testing.T, dir string) string {
			for _, name := range []string{"objects", "refs"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
			return filepath.Join(dir, "HEAD")
		}},
		{"unreadable commondir", func(t *testing.T, dir string) string {
			write(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
			write(t, filepath.Join(dir, "commondir"), "../.git\n")
			return filepath.Join(dir, "commondir")
		}},
		{"unreadable objects", func(t *testing.T, dir string) string {
			for _, name := range []string{"objects", "refs"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
			return filepath.Join(dir, "objects")
		}},
		{"empty commondir", func(t *testing.T, dir string) string {
			write(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
			write(t, filepath.Join(dir, "commondir"), "")
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testHome(t)
			repo, linked := untrackedInstructionWorktree(t)
			dir := filepath.Join(repo, "vendor", "pkg")
			write(t, filepath.Join(dir, "AGENTS.md"), "behind an unreadable layout\n")
			locked := tc.setup(t, dir)
			if locked != "" {
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(locked, 0o755) })
			}
			if err := prepare(t, linked, "claude"); err == nil || !strings.Contains(err.Error(), dir) {
				t.Fatalf("unreadable repository layout treated as a plain directory: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(linked, "AGENTS.md")); !os.IsNotExist(err) {
				t.Fatalf("preparation proceeded despite a failed repository check: %v", err)
			}
		})
	}
}

func TestDelegationFoldsGitlinkCaseLikeGit(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	git(t, repo, "config", "core.ignorecase", "true")
	sha := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/\u00c4")
	write(t, filepath.Join(repo, "vendor", "\u00e4", "AGENTS.md"), "plain unicode folder\n")
	if _, err := os.Stat(filepath.Join(repo, "vendor", "\u00c4")); err == nil {
		t.Skip("filesystem folds Unicode case")
	}
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(linked, "vendor", "\u00e4", "AGENTS.md")); got != "plain unicode folder\n" {
		t.Fatalf("Unicode folder folded onto a gitlink: %q", got)
	}
}

func TestDelegationPreparesPlainDirectoriesWithGitTrace(t *testing.T) {
	testHome(t)
	repo, linked := untrackedInstructionWorktree(t)
	write(t, filepath.Join(repo, "docs", "HEAD"), "not a ref\n")
	write(t, filepath.Join(repo, "docs", "AGENTS.md"), "plain docs\n")
	t.Setenv("GIT_TRACE", "1")
	if err := prepare(t, linked, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(linked, "docs", "AGENTS.md")); got != "plain docs\n" {
		t.Fatalf("plain directory instructions not prepared: %q", got)
	}
}

func TestDelegationReportsUnreadableCommonDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	for _, name := range []string{"objects", "refs"} {
		for _, relative := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/relative=%t", name, relative), func(t *testing.T) {
				testHome(t)
				repo := newRepo(t)
				bare := filepath.Join(filepath.Dir(repo), "bare.git")
				git(t, repo, "clone", "-q", "--bare", repo, bare)
				bare = resolvePath(bare)
				checkout := filepath.Join(filepath.Dir(repo), "checkout")
				git(t, bare, "worktree", "add", "-q", checkout, "main")
				common := filepath.Join(filepath.Dir(repo), "separate.git")
				git(t, repo, "init", "-q", "--bare", common)
				nested := filepath.Join(bare, "nested")
				write(t, filepath.Join(nested, "HEAD"), "ref: refs/heads/main\n")
				commonRef := common
				if relative {
					var err error
					commonRef, err = filepath.Rel(nested, common)
					if err != nil {
						t.Fatal(err)
					}
				}
				write(t, filepath.Join(nested, "commondir"), commonRef+"\r\n")
				write(t, filepath.Join(nested, "AGENTS.md"), "separate instructions\n")
				write(t, filepath.Join(bare, "AGENTS.md"), "primary instructions\n")
				if err := prepare(t, checkout, "claude"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Join(checkout, "nested")); !os.IsNotExist(err) {
					t.Fatalf("nested instructions copied before permission change: %v", err)
				}
				locked := filepath.Join(common, name)
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(locked, 0o755) })
				if err := prepare(t, checkout, "claude"); err == nil || !errors.Is(err, os.ErrPermission) {
					t.Fatalf("common directory inspection error ignored: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(checkout, "nested")); !os.IsNotExist(err) {
					t.Fatalf("instructions copied after failed repository inspection: %v", err)
				}
			})
		}
	}
}
