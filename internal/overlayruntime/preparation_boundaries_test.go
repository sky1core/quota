package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorktreeCreateRejectsFutureInstructionHooks(t *testing.T) {
	for _, body := range []string{`{"hooks":[]}`, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/example/quota-cli agent instructions _hook --agent=claude --event=SessionStart"}]}]}}`} {
		t.Run(body, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, sharedRule), "shared\n")
			write(t, filepath.Join(repo, localRule), "private\n")
			write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\nAGENTS.override.md\nCLAUDE.local.md\n/config/settings.json\n")
			for _, dir := range []string{".claude", "config"} {
				if err := os.Mkdir(filepath.Join(repo, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(repo, "config", "settings.json")
			write(t, source, "{}")
			alias := filepath.Join(repo, ".claude", "settings.json")
			if err := os.Symlink("../config/settings.json", alias); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", sharedRule, ".gitignore", ".claude/settings.json")
			seedCommit(t, repo)
			base := git(t, repo, "rev-parse", "HEAD")
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", ".claude/settings.json")
			seedCommit(t, repo)
			if err := SetupRepository(context.Background(), repo, "all", "", "config/settings.json"); err != nil {
				t.Fatal(err)
			}
			write(t, source, body)
			state := filepath.Join(repo, ".git", "quota-instructions.json")
			before := regressionRead(t, state)
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF", base)
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"future-hooks"}`, "claude-worktree-create")
			if code == 0 || out != "" || !strings.Contains(errb, "hooks") {
				t.Fatalf("future hooks accepted: %d %q %q", code, out, errb)
			}
			if regressionRead(t, state) != before || regressionRead(t, source) != body {
				t.Fatal("rejected hook configuration changed state or source")
			}
			r, err := resolveContext(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Checkouts()) != 1 {
				t.Fatalf("rejected worktree remained registered: %v", r.Checkouts())
			}
		})
	}
}

func TestBareSetupRejectsManagedMetadataSources(t *testing.T) {
	seed := newRepo(t)
	write(t, filepath.Join(seed, sharedRule), "shared\n")
	git(t, seed, "add", sharedRule)
	seedCommit(t, seed)
	bare := filepath.Join(t.TempDir(), "bare.git")
	git(t, seed, "clone", "--bare", "-q", seed, bare)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, bare, "worktree", "add", "--detach", linked, "HEAD")
	write(t, filepath.Join(bare, localRule), "private\n")
	if err := SetupRepository(context.Background(), linked, "codex", ""); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"quota-instructions.json", "quota-instructions.lock", "info/exclude"} {
		t.Run(rel, func(t *testing.T) {
			before := map[string]string{}
			for _, path := range []string{filepath.Join(bare, "quota-instructions.json"), filepath.Join(bare, "info", "exclude"), filepath.Join(linked, codexRule)} {
				before[path] = regressionRead(t, path)
			}
			for _, operation := range []string{"plan", "setup"} {
				var err error
				if operation == "plan" {
					_, err = PlanRepositoryWithPolicy(context.Background(), linked, "codex", "", rel)
				} else {
					err = SetupRepository(context.Background(), linked, "codex", "", rel)
				}
				if err == nil || !strings.Contains(err.Error(), "managed repository metadata") {
					t.Fatalf("%s accepted %s: %v", operation, rel, err)
				}
			}
			for path, body := range before {
				if regressionRead(t, path) != body {
					t.Fatalf("metadata conflict changed %s", path)
				}
			}
			if exists(filepath.Join(linked, rel)) {
				t.Fatalf("metadata copied into worktree: %s", rel)
			}
		})
	}
}

func TestSetupRejectsCanonicalLocalPathDuplicatesBeforeWrites(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	paths := []string{"private/caf\u00e9.txt", "private/cafe\u0301.txt"}
	if err := os.Mkdir(filepath.Join(repo, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	beforeIgnore := regressionRead(t, exclude)
	for _, rel := range paths {
		write(t, filepath.Join(repo, rel), "private data\n")
		beforeIgnore += "/" + rel + "\n"
	}
	write(t, exclude, beforeIgnore)
	state := filepath.Join(repo, ".git", "quota-instructions.json")
	beforeState := regressionRead(t, state)
	for _, operation := range []string{"plan", "setup"} {
		var err error
		if operation == "plan" {
			_, err = PlanRepositoryWithPolicy(context.Background(), repo, "codex", "", paths...)
		} else {
			err = SetupRepository(context.Background(), repo, "codex", "", paths...)
		}
		if err == nil || !strings.Contains(err.Error(), "duplicate local file path") {
			t.Fatalf("%s accepted canonical duplicates: %v", operation, err)
		}
		if regressionRead(t, state) != beforeState || regressionRead(t, exclude) != beforeIgnore {
			t.Fatal("duplicate path registration changed state or ignore rules")
		}
		if exists(filepath.Join(linked, "private")) {
			t.Fatal("duplicate paths were partially copied")
		}
	}
	if err := validateLocalFiles([]string{"private/caf\u00e9", "private/cafe\u0301/child"}); err == nil || !strings.Contains(err.Error(), "overlapping local file paths") {
		t.Fatalf("canonical parent overlap accepted: %v", err)
	}
	if err := validateLocalFiles([]string{"private/\u017fame.txt", "private/same.txt"}); err == nil || !strings.Contains(err.Error(), "duplicate local file path") {
		t.Fatalf("Unicode case aliases accepted: %v", err)
	}
}

func TestManagedReservedPathUsesCanonicalComparison(t *testing.T) {
	if err := validateLocalFiles([]string{"AGENT\u017f.override.md/child"}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("canonical reserved parent accepted: %v", err)
	}
}

func TestManagedMetadataSnapshotHardlinkRemainsStable(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(repo, ".git", "quota-instructions.json")
	const rel = "snapshot.json"
	source := filepath.Join(repo, rel)
	if err := os.Link(state, source); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	write(t, exclude, regressionRead(t, exclude)+"/snapshot.json\n")
	body := regressionRead(t, source)
	for n := 0; n < 2; n++ {
		if err := SetupRepository(context.Background(), repo, "codex", "", rel); err != nil {
			t.Fatal(err)
		}
		if regressionRead(t, source) != body || regressionRead(t, filepath.Join(linked, rel)) != body {
			t.Fatal("separate snapshot entry changed with repository metadata")
		}
		if code, out, errb := run(t, "", "check", "--runtime=codex", repo); code != 0 {
			t.Fatalf("snapshot created stale managed state: %d %q %q", code, out, errb)
		}
	}
}

func TestPrimarySharedCopyChecksRecordedOwnershipWhenSourceMatches(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "seed.txt"), "seed")
	git(t, repo, "add", "seed.txt")
	seedCommit(t, repo)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	write(t, filepath.Join(repo, sharedRule), "initial shared\n")
	if err := SetupRepository(context.Background(), repo, "codex", "primary"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repo, linked} {
		write(t, filepath.Join(dir, sharedRule), "same user edit\n")
	}
	state := filepath.Join(repo, ".git", "quota-instructions.json")
	before := regressionRead(t, state)
	if err := SetupRepository(context.Background(), repo, "codex", "primary"); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("matching shared user edit was accepted: %v", err)
	}
	if code, out, errb := run(t, "", "check", "--runtime=codex", repo); code == 0 || !strings.Contains(out+errb, "ownership") {
		t.Fatalf("matching shared user edit passed status: %d %q %q", code, out, errb)
	}
	if regressionRead(t, state) != before || regressionRead(t, filepath.Join(linked, sharedRule)) != "same user edit\n" {
		t.Fatal("shared ownership conflict changed files")
	}
}

func TestManagedWorktreeRemovalResolvesPathAliases(t *testing.T) {
	for _, extraLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "alias", true: "separate hardlink"}[extraLink], func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, sharedRule), "shared\n")
			write(t, filepath.Join(repo, localRule), "private\n")
			if err := os.Mkdir(filepath.Join(repo, "private"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(repo, "PRIVATE")); os.IsNotExist(err) {
				t.Skip("filesystem distinguishes case aliases")
			}
			write(t, filepath.Join(repo, "private", "seed.txt"), "seed")
			write(t, filepath.Join(repo, "private", "helper.env"), "example=1\n")
			write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\nAGENTS.override.md\nCLAUDE.local.md\nprivate/*.env\n")
			git(t, repo, "add", sharedRule, ".gitignore", "private/seed.txt")
			seedCommit(t, repo)
			if err := SetupRepository(context.Background(), repo, "all", "", "PRIVATE/helper.env"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"case-alias"}`, "claude-worktree-create")
			if code != 0 {
				t.Fatalf("create: %d %q %q", code, out, errb)
			}
			target := strings.TrimSpace(out)
			if extraLink {
				if err := os.Link(filepath.Join(target, "private", "helper.env"), filepath.Join(target, "private", "user.env")); err != nil {
					t.Fatal(err)
				}
			}
			code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
			if extraLink {
				if code == 0 || !exists(target) || !strings.Contains(errb, "user.env") {
					t.Fatalf("unmanaged hardlink was not protected: %d %q %q", code, out, errb)
				}
			} else if code != 0 || exists(target) {
				t.Fatalf("managed alias blocked removal: %d %q %q", code, out, errb)
			}
		})
	}
}

func TestWorktreeRemovalPreservesUnownedClaudeCopy(t *testing.T) {
	repo, _ := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "all", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"missing-ownership"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	copy := filepath.Join(target, localBridge)
	if err := UpdateRepositoryState(context.Background(), repo, func(state *RepositoryState) error { delete(state.Generated, copy); return nil }); err != nil {
		t.Fatal(err)
	}
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
	if code == 0 || !exists(copy) {
		t.Fatalf("unowned native copy was removed: %d %q %q", code, out, errb)
	}
}

func TestWorktreeRemovalPreservesStaleCopyAfterIgnoreRemoval(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	baseIgnores := "AGENTS.local.md\nAGENTS.override.md\nCLAUDE.local.md\n"
	write(t, filepath.Join(repo, ".gitignore"), baseIgnores)
	git(t, repo, "add", sharedRule, ".gitignore")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, ".gitignore"), baseIgnores+"extra.env\n")
	write(t, filepath.Join(repo, "extra.env"), "original=1\n")
	if err := SetupRepository(context.Background(), repo, "all", "", "extra.env"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"stale-unignored"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	write(t, filepath.Join(repo, ".git", "info", "exclude"), "")
	write(t, filepath.Join(repo, "extra.env"), "current=2\n")
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
	if code == 0 || !exists(target) || !strings.Contains(errb, "current source") {
		t.Fatalf("stale unignored copy was not protected: %d %q %q", code, out, errb)
	}
	if regressionRead(t, filepath.Join(target, "extra.env")) != "original=1\n" {
		t.Fatal("stale copy changed")
	}
}

func TestSourceFreeWorktreeRejectsTrackedPrivateInstructions(t *testing.T) {
	for _, rel := range []string{localRule, localBridge} {
		t.Run(rel, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "seed.txt"), "seed")
			write(t, filepath.Join(repo, rel), "synthetic private instruction\n")
			git(t, repo, "add", "seed.txt", rel)
			seedCommit(t, repo)
			base := git(t, repo, "rev-parse", "HEAD")
			if err := os.Remove(filepath.Join(repo, rel)); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", rel)
			seedCommit(t, repo)
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF", base)
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"private-base"}`, "claude-worktree-create")
			if code == 0 || out != "" || !strings.Contains(errb, "tracked") {
				t.Fatalf("private tracked base was accepted: %d %q %q", code, out, errb)
			}
		})
	}
}

func TestWorktreeRemovalPreservesChangedPermissions(t *testing.T) {
	for _, test := range []struct {
		rel  string
		mode os.FileMode
	}{{"extra.sh", 0o700}, {"extra.sh", 0o640}, {sharedRule, 0o744}, {sharedBridge, 0o744}, {localBridge, 0o700}} {
		t.Run(test.rel+test.mode.String(), func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "seed.txt"), "seed")
			git(t, repo, "add", "seed.txt")
			seedCommit(t, repo)
			write(t, filepath.Join(repo, sharedRule), "shared\n")
			write(t, filepath.Join(repo, localRule), "private\n")
			write(t, filepath.Join(repo, ".git", "info", "exclude"), "extra.sh\n")
			write(t, filepath.Join(repo, "extra.sh"), "#!/bin/sh\nexit 0\n")
			if err := SetupRepository(context.Background(), repo, "all", "primary", "extra.sh"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"changed-mode"}`, "claude-worktree-create")
			if code != 0 {
				t.Fatalf("create: %d %q %q", code, out, errb)
			}
			target := strings.TrimSpace(out)
			copy := filepath.Join(target, test.rel)
			if err := os.Chmod(copy, test.mode); err != nil {
				t.Fatal(err)
			}
			code, out, errb = run(t, "", "check", repo)
			if code == 0 || !strings.Contains(out+errb, "permissions changed") {
				t.Fatalf("status missed changed permissions: %d %q %q", code, out, errb)
			}
			if err := SetupRepository(context.Background(), repo, "all", "primary"); err == nil || !strings.Contains(err.Error(), "permissions changed") {
				t.Fatalf("setup did not preserve changed permissions: %v", err)
			}
			code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
			if code == 0 || !exists(copy) {
				t.Fatalf("permission change was deleted: %d %q %q", code, out, errb)
			}
		})
	}
}

func TestSourceFreeWorktreeChecksTargetSettings(t *testing.T) {
	for _, body := range []string{`{"disableAllHooks":true}`, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/example/quota-cli agent instructions _hook --agent=claude --event=SessionStart"}]}]}}`} {
		t.Run(body, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "seed.txt"), "seed")
			if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(repo, ".claude", "settings.json")
			write(t, settings, body)
			git(t, repo, "add", "seed.txt", ".claude/settings.json")
			seedCommit(t, repo)
			base := git(t, repo, "rev-parse", "HEAD")
			if err := os.Remove(settings); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", ".claude/settings.json")
			seedCommit(t, repo)
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF", base)
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"target-settings"}`, "claude-worktree-create")
			if code == 0 || out != "" || !strings.Contains(errb, "settings.json") {
				t.Fatalf("target settings were skipped: %d %q %q", code, out, errb)
			}
		})
	}
}

func TestClaudeOnlyWorktreeRemovalSkipsUnusedCodexMerge(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), strings.Repeat("s", 5<<20))
	write(t, filepath.Join(repo, localRule), strings.Repeat("p", 4<<20))
	git(t, repo, "add", sharedRule)
	seedCommit(t, repo)
	if err := SetupRepository(context.Background(), repo, "claude", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"claude-large"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	if exists(filepath.Join(target, codexRule)) {
		t.Fatal("Claude-only preparation created Codex instructions")
	}
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
	if code != 0 || exists(target) {
		t.Fatalf("unused Codex merge blocked cleanup: %d %q %q", code, out, errb)
	}
}

func TestWorktreeRemovalUsesRecordedUmaskMode(t *testing.T) {
	previousMask := unix.Umask(0o077)
	defer unix.Umask(previousMask)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "seed.txt"), "seed")
	git(t, repo, "add", "seed.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	if err := SetupRepository(context.Background(), repo, "all", "primary"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"private-umask"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	state, err := ReadRepositoryState(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{sharedRule, sharedBridge} {
		if state.GeneratedModes[filepath.Join(target, rel)] != 0o600 {
			t.Fatalf("actual umask mode not recorded: %s %#v", rel, state.GeneratedModes)
		}
	}
	unix.Umask(0o022)
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
	if code != 0 || exists(target) {
		t.Fatalf("normal umask output could not be removed: %d %q %q", code, out, errb)
	}
	state, err = ReadRepositoryState(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for path := range state.Generated {
		if instructionPathWithin(path, target) {
			t.Fatalf("removed ownership retained: %s", path)
		}
	}
	for path := range state.GeneratedModes {
		if instructionPathWithin(path, target) {
			t.Fatalf("removed mode retained: %s", path)
		}
	}
}

func TestWorktreeWithoutSourcesIgnoresRemovedOwnershipHistory(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "seed.txt"), "seed")
	git(t, repo, "add", "seed.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, localRule), "private\n")
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"with-source"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	removed := strings.TrimSpace(out)
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(removed)+`}`, "claude-worktree-remove")
	if code != 0 {
		t.Fatalf("remove: %d %q %q", code, out, errb)
	}
	if err := os.Remove(filepath.Join(repo, localRule)); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRepositoryState(context.Background(), repo, func(state *RepositoryState) error {
		state.Generated[filepath.Join(removed, localRule)] = digest([]byte("private\n"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"name":"without-source"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("historical ownership blocked normal worktree: %d %q %q", code, out, errb)
	}
	created := strings.TrimSpace(out)
	code, out, errb = run(t, `{"cwd":`+quote(repo)+`,"worktree_path":`+quote(created)+`}`, "claude-worktree-remove")
	if code != 0 {
		t.Fatalf("normal removal failed: %d %q %q", code, out, errb)
	}
}

func TestManagedModeTracksSourceExecutionChanges(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	linked = resolvePath(linked)
	write(t, filepath.Join(repo, ".git", "info", "exclude"), "extra.sh\n")
	source := filepath.Join(repo, "extra.sh")
	write(t, source, "#!/bin/sh\nexit 0\n")
	for _, mode := range []os.FileMode{0o600, 0o700, 0o600} {
		if err := os.Chmod(source, mode); err != nil {
			t.Fatal(err)
		}
		if err := SetupRepository(context.Background(), repo, "codex", "", "extra.sh"); err != nil {
			t.Fatal(err)
		}
		state, err := ReadRepositoryState(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		if state.GeneratedModes[filepath.Join(linked, "extra.sh")] != uint32(mode) {
			t.Fatalf("execution mode not updated to %04o: %#v", mode, state.GeneratedModes)
		}
	}
}

func TestSetupRecordsLegacyGeneratedModesWithoutRewriting(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "seed.txt"), "seed")
	git(t, repo, "add", "seed.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	if err := SetupRepository(context.Background(), repo, "all", "primary"); err != nil {
		t.Fatal(err)
	}
	before := map[string]os.FileInfo{}
	if err := UpdateRepositoryState(context.Background(), repo, func(state *RepositoryState) error {
		state.GeneratedModes = nil
		for path := range state.Generated {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			before[path] = info
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("missing generated outputs")
	}
	if err := SetupRepository(context.Background(), repo, "all", "primary"); err != nil {
		t.Fatal(err)
	}
	state, err := ReadRepositoryState(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for path, old := range before {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if state.GeneratedModes[path] != uint32(info.Mode()) || !os.SameFile(old, info) || !old.ModTime().Equal(info.ModTime()) {
			t.Fatalf("legacy mode not recorded without rewriting %s: %#v", path, state.GeneratedModes)
		}
	}
}

func TestSetupDoesNotAdoptEditedOrphanBridges(t *testing.T) {
	for _, source := range []string{sharedRule, localRule} {
		t.Run(source, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, sharedRule), "shared\n")
			write(t, filepath.Join(repo, localRule), "private\n")
			if err := SetupRepository(context.Background(), repo, "all", ""); err != nil {
				t.Fatal(err)
			}
			bridge := sharedBridge
			if source == localRule {
				bridge = localBridge
			}
			path := filepath.Join(repo, bridge)
			if err := os.Remove(filepath.Join(repo, source)); err != nil {
				t.Fatal(err)
			}
			regressionChmod(t, path, 0o700)
			statePath := filepath.Join(repo, ".git", "quota-instructions.json")
			before := regressionRead(t, statePath)
			if code, out, errb := run(t, "", "check", "--runtime=claude", repo); code != 1 || !strings.Contains(out+errb, "permissions changed") {
				t.Fatalf("status missed edited orphan bridge: %d %q %q", code, out, errb)
			}
			if err := SetupRepository(context.Background(), repo, "all", ""); err == nil || !strings.Contains(err.Error(), "permissions changed") {
				t.Fatalf("setup accepted edited orphan bridge: %v", err)
			}
			if regressionRead(t, statePath) != before {
				t.Fatal("failed setup changed the recorded ownership")
			}
			if err := UninstallRepository(context.Background(), repo, "all"); err == nil || !strings.Contains(err.Error(), "permissions changed") {
				t.Fatalf("uninstall accepted edited orphan bridge: %v", err)
			}
			regressionMode(t, path, 0o700)
		})
	}
}
