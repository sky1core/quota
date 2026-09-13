package overlayruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func regressionLinkedRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared rules\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private rule one\n")
	git(t, repo, "add", "AGENTS.md", "CLAUDE.md")
	seedCommit(t, repo)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	return repo, linked
}

func regressionRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func regressionMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func regressionChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func regressionSetup(t *testing.T, repo string) {
	t.Helper()
	if code, out, errb := run(t, "", "setup", repo); code != 0 {
		t.Fatalf("setup: exit=%d out=%q err=%q", code, out, errb)
	}
}

func regressionSession(t *testing.T, repo string) string {
	t.Helper()
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`}`, "json", "SessionStart", "x", "y", repo, "claude-session")
	if code != 0 {
		t.Fatalf("session: exit=%d out=%q err=%q", code, out, errb)
	}
	return out + errb
}

func TestRegressionTrackedGeneratedCopyPreserved(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	original := regressionRead(t, target)
	git(t, linked, "add", "-f", "CLAUDE.local.md")
	source := filepath.Join(repo, "AGENTS.local.md")
	write(t, source, "private rule two\n")
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
		}
		code, out, errb := run(t, "", "setup", repo)
		if code != 1 {
			t.Fatalf("tracked setup (missing=%v): exit=%d out=%q err=%q", missing, code, out, errb)
		}
		if got := regressionRead(t, target); got != original {
			t.Fatalf("setup changed tracked copy (missing=%v): %q", missing, got)
		}
		if notice := regressionSession(t, linked); !strings.Contains(notice, "tracked") {
			t.Fatalf("tracked refusal missing: %q", notice)
		}
		if got := regressionRead(t, target); got != original {
			t.Fatalf("session changed tracked copy (missing=%v): %q", missing, got)
		}
	}
}

func TestRegressionUnignoredGeneratedCopyPreserved(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	original := regressionRead(t, target)
	write(t, filepath.Join(linked, ".gitignore"), "!CLAUDE.local.md\n")
	source := filepath.Join(repo, "AGENTS.local.md")
	write(t, source, "private rule two\n")
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
		}
		for _, operation := range []string{"session", "setup"} {
			if operation == "session" {
				if notice := regressionSession(t, linked); !strings.Contains(notice, "not git-ignored") {
					t.Fatalf("ignore refusal missing: %q", notice)
				}
			} else if code, out, errb := run(t, "", "setup", repo); code != 1 {
				t.Fatalf("unignored setup: exit=%d out=%q err=%q", code, out, errb)
			}
			if got := regressionRead(t, target); got != original {
				t.Fatalf("%s changed unignored copy (missing=%v): %q", operation, missing, got)
			}
		}
	}
}

func TestRegressionTrackedLocalSourceNotDelivered(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	original := regressionRead(t, target)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private rule two\n")
	git(t, repo, "add", "-f", "AGENTS.local.md")
	if code, out, errb := run(t, "", "setup", repo); code != 1 {
		t.Fatalf("tracked source setup: exit=%d out=%q err=%q", code, out, errb)
	}
	if got := regressionRead(t, target); got != original {
		t.Fatalf("setup delivered tracked source: %q", got)
	}
	regressionSession(t, linked)
	if got := regressionRead(t, target); got != original {
		t.Fatalf("session delivered tracked source: %q", got)
	}
	code, out, errb := run(t, `{"cwd":`+quote(linked)+`,"source":"startup"}`, "json", "SessionStart", "x", "y", linked, "codex-session")
	if code != 0 || strings.Contains(out+errb, "private rule two") || !strings.Contains(out+errb, "tracked") {
		t.Fatalf("tracked source hook: exit=%d out=%q err=%q", code, out, errb)
	}
}

func TestRegressionUnownedCopyPreservedBySessionAndSetup(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	const original = "hand-written local rules\n"
	write(t, target, original)
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
				t.Fatal(err)
			}
		}
		regressionSession(t, linked)
		if got := regressionRead(t, target); got != original {
			t.Fatalf("session changed unowned copy: %q", got)
		}
		if code, out, errb := run(t, "", "setup", repo); code != 1 {
			t.Fatalf("unowned setup: exit=%d out=%q err=%q", code, out, errb)
		}
		if got := regressionRead(t, target); got != original {
			t.Fatalf("setup changed unowned copy: %q", got)
		}
	}
}

func TestRegressionGeneratedCopyPermissions(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	source := filepath.Join(repo, "AGENTS.local.md")
	regressionChmod(t, source, 0o600)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	regressionMode(t, target, 0o600)
	for _, operation := range []string{"setup", "session"} {
		for _, mode := range []os.FileMode{0o644, 0o400} {
			previous := regressionRead(t, target)
			regressionChmod(t, target, mode)
			body := "private rules for " + operation + " " + mode.String() + "\n"
			write(t, source, body)
			if operation == "setup" {
				if code, out, errb := run(t, "", "setup", repo); code != 1 || !strings.Contains(out+errb, "permissions changed") {
					t.Fatalf("setup accepted changed permissions: %d %q %q", code, out, errb)
				}
			} else if notice := regressionSession(t, linked); !strings.Contains(notice, "permissions changed") {
				t.Fatalf("session accepted changed permissions: %q", notice)
			}
			regressionMode(t, target, mode)
			if regressionRead(t, target) != previous {
				t.Fatal("copy with changed permissions was overwritten")
			}
			regressionChmod(t, target, 0o600)
			if operation == "setup" {
				regressionSetup(t, repo)
			} else {
				regressionSession(t, linked)
			}
			if !strings.HasSuffix(regressionRead(t, target), body) {
				t.Fatal("copy was not refreshed after restoring generated permissions")
			}
		}
	}
	regressionSession(t, linked)
	regressionMode(t, target, 0o600)
}

func TestRegressionSetupRespectsUmaskAndExistingModes(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	previousMask := syscall.Umask(0o077)
	code, out, errb := run(t, "", "setup", repo)
	syscall.Umask(previousMask)
	if code != 0 {
		t.Fatalf("setup with umask: %d %s %s", code, out, errb)
	}
	bridge := filepath.Join(repo, "CLAUDE.md")
	ignore := filepath.Join(repo, ".gitignore")
	regressionMode(t, bridge, 0o600)
	regressionMode(t, ignore, 0o600)
	regressionSetup(t, repo)
	regressionMode(t, bridge, 0o600)
	write(t, bridge, "@AGENTS.md\n\n")
	if code, out, errb := run(t, "", "setup", repo); code != 1 || !strings.Contains(out+errb, "user edits") {
		t.Fatalf("edited generated bridge accepted: %d %q %q", code, out, errb)
	}
	if got := regressionRead(t, bridge); got != "@AGENTS.md\n\n" {
		t.Fatalf("edited generated bridge changed: %q", got)
	}
	repo = newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	bridge = filepath.Join(repo, "CLAUDE.md")
	ignore = filepath.Join(repo, ".gitignore")
	write(t, bridge, "@AGENTS.md\n\n")
	write(t, ignore, "AGENTS.local.md\n")
	regressionChmod(t, bridge, 0o400)
	regressionChmod(t, ignore, 0o640)
	regressionSetup(t, repo)
	regressionMode(t, bridge, 0o400)
	regressionMode(t, ignore, 0o640)
	if got := regressionRead(t, bridge); got != "@AGENTS.md\n" {
		t.Fatalf("bridge not normalized: %q", got)
	}
	if got := git(t, repo, "check-ignore", "CLAUDE.local.md"); got != "CLAUDE.local.md" {
		t.Fatalf("ignore not updated: %q", got)
	}
}

func TestRegressionCodexDefaultBudgetWithoutConfig(t *testing.T) {
	repo := newRepo(t)
	shared := filepath.Join(repo, "AGENTS.md")
	write(t, shared, "# shared\n")
	regressionSetup(t, repo)
	config := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("expected absent config: %v", err)
	}
	for _, size := range []int{32768, 32769} {
		write(t, shared, strings.Repeat("a", size))
		code, out, errb := run(t, "", "check", repo)
		want := 0
		if size > 32768 {
			want = 1
		}
		if code != want {
			t.Fatalf("size %d: exit=%d want=%d out=%q err=%q", size, code, want, out, errb)
		}
	}
	write(t, shared, "# shared\n")
	write(t, config, "[invalid\n")
	if code, out, errb := run(t, "", "check", repo); code != 1 {
		t.Fatalf("invalid config: exit=%d out=%q err=%q", code, out, errb)
	}
}

func regressionCore(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCore(context.Background(), args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRegressionRuntimeVerificationIsolation(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private rules\n")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n")
	settings := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	write(t, settings, `{"disableAllHooks":true}`)
	if code, out, errb := regressionCore(t, "setup", "--runtime=codex", repo); code != 0 {
		t.Fatalf("native preparation: exit=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := regressionCore(t, "verify", "--runtime=codex", repo); code != 0 {
		t.Fatalf("codex required Claude configuration: exit=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := regressionCore(t, "setup", "--runtime=codex", repo); code != 0 {
		t.Fatalf("codex setup: exit=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := regressionCore(t, "verify", "--runtime=claude", repo); code != 1 {
		t.Fatalf("claude ignored disabled hooks: exit=%d out=%q err=%q", code, out, errb)
	}
	write(t, settings, "{}")
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), "[invalid\n")
	write(t, filepath.Join(repo, "AGENTS.md"), strings.Repeat("a", 32769))
	write(t, filepath.Join(repo, "AGENTS.local.md"), strings.Repeat("b", 32769))
	if code, out, errb := regressionCore(t, "setup", "--runtime=claude", repo); code != 0 {
		t.Fatalf("claude required Codex configuration: exit=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := regressionCore(t, "verify", "--runtime=codex", repo); code != 1 {
		t.Fatalf("codex ignored its configuration: exit=%d out=%q err=%q", code, out, errb)
	}
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private rules\n")
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), "")
	for _, runtime := range []string{"claude", "codex", "all"} {
		want := 0
		if code, out, errb := regressionCore(t, "verify", "--runtime="+runtime, repo); code != want {
			t.Fatalf("%s verification: exit=%d out=%q err=%q", runtime, code, out, errb)
		}
	}
	git(t, repo, "add", "-f", "AGENTS.local.md")
	for _, runtime := range []string{"claude", "codex"} {
		if code, out, errb := regressionCore(t, "verify", "--runtime="+runtime, repo); code != 1 {
			t.Fatalf("%s skipped shared invariant: exit=%d out=%q err=%q", runtime, code, out, errb)
		}
	}
}

func TestRegressionInvalidRuntimeArgumentsDoNotMutate(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	original := regressionRead(t, exclude)
	for _, args := range [][]string{
		{"--runtime=unknown", repo},
		{"--runtime=", repo},
		{"--runtime=claude", "--runtime=codex", repo},
		{"--unknown", repo},
		{repo, "extra"},
	} {
		for _, command := range []string{"setup", "verify"} {
			code, out, errb := regressionCore(t, append([]string{command}, args...)...)
			if code != 2 {
				t.Fatalf("%s %v: exit=%d out=%q err=%q", command, args, code, out, errb)
			}
			for _, name := range []string{"CLAUDE.md", "CLAUDE.local.md", ".gitignore"} {
				if _, err := os.Lstat(filepath.Join(repo, name)); !os.IsNotExist(err) {
					t.Fatalf("invalid args created %s: %v", name, err)
				}
			}
			if got := regressionRead(t, exclude); got != original {
				t.Fatal("invalid args modified Git exclude")
			}
		}
	}
}
