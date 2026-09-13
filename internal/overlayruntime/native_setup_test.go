package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeStatusRejectsTrackedMissingOverride(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, codexRule), "tracked override\n")
	write(t, filepath.Join(repo, ".git", "info", "exclude"), localRule+"\n")
	git(t, repo, "add", sharedRule, codexRule)
	if err := os.Remove(filepath.Join(repo, codexRule)); err != nil {
		t.Fatal(err)
	}
	if code, out, errb := run(t, "", "check", "--runtime=codex", repo); code != 1 || !strings.Contains(out+errb, codexRule+" is tracked") {
		t.Fatalf("status missed tracked absent override: %d %q %q", code, out, errb)
	}
	if _, err := PlanRepository(context.Background(), repo, "codex"); err == nil || !strings.Contains(err.Error(), codexRule+" is tracked") {
		t.Fatalf("plan missed tracked absent override: %v", err)
	}
}

func TestNativeSetupRejectsCopiedGitIgnoreBeforeWrites(t *testing.T) {
	for _, rel := range []string{".gitignore", "config/.gitignore", "config/.GITIGNORE"} {
		t.Run(rel, func(t *testing.T) {
			repo, linked := regressionLinkedRepo(t)
			path := filepath.Join(repo, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, path, ".gitignore\n!AGENTS.local.md\n")
			before := regressionRead(t, path)
			for _, operation := range []string{"plan", "setup"} {
				var err error
				if operation == "plan" {
					_, err = PlanRepositoryWithPolicy(context.Background(), repo, "codex", "checkout", rel)
				} else {
					err = SetupRepository(context.Background(), repo, "codex", "checkout", rel)
				}
				if err == nil || !strings.Contains(err.Error(), "changes Git ignore rules") {
					t.Fatalf("%s allowed copying Git ignore rules: %v", operation, err)
				}
				for _, generated := range []string{filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(repo, codexRule), filepath.Join(linked, rel), filepath.Join(repo, ".git", "quota-instructions.json")} {
					if exists(generated) {
						t.Fatalf("%s wrote %s before rejecting the file", operation, generated)
					}
				}
				if regressionRead(t, path) != before {
					t.Fatalf("%s changed the source", operation)
				}
			}
		})
	}
}

func TestNativeSetupRegistersLocalFilesAndRefreshesEveryCopy(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	const relative = "config/private settings.local.json"
	if err := os.MkdirAll(filepath.Join(repo, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, relative), `{"value":"first"}`)
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.appendExclude("/config/"); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "", relative, relative); err != nil {
		t.Fatal(err)
	}
	state, err := ReadRepositoryState(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !state.NativeCodex || !reflect.DeepEqual(state.LocalFiles, []string{relative}) {
		t.Fatalf("registration missing: %+v", state)
	}
	if strings.Contains(regressionRead(t, filepath.Join(repo, ".gitignore")), relative) {
		t.Fatal("private file path entered a publishable ignore file")
	}
	first, err := os.Stat(filepath.Join(linked, codexRule))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), linked, "codex", "", relative); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(filepath.Join(linked, codexRule))
	if err != nil || !os.SameFile(first, second) || !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("identical setup rewrote the native file")
	}
	write(t, filepath.Join(repo, localRule), "updated private source\n")
	write(t, filepath.Join(repo, relative), `{"value":"second"}`)
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{repo, linked} {
		body := regressionRead(t, filepath.Join(target, codexRule))
		if strings.Count(body, "updated private source") != 1 || !strings.Contains(body, regressionRead(t, filepath.Join(target, sharedRule))) {
			t.Fatalf("stale or wrong native source: %q", body)
		}
	}
	if regressionRead(t, filepath.Join(linked, localRule)) != "updated private source\n" || regressionRead(t, filepath.Join(linked, relative)) != `{"value":"second"}` {
		t.Fatal("registered local copies were not refreshed")
	}
	beforeCopy := regressionRead(t, filepath.Join(linked, localRule))
	beforePrimary := regressionRead(t, filepath.Join(repo, codexRule))
	write(t, filepath.Join(linked, codexRule), "user-edited override")
	write(t, filepath.Join(repo, localRule), "third private source\n")
	if err := SetupRepository(context.Background(), repo, "codex", ""); err == nil {
		t.Fatal("edited override accepted")
	}
	if regressionRead(t, filepath.Join(linked, localRule)) != beforeCopy || regressionRead(t, filepath.Join(repo, codexRule)) != beforePrimary {
		t.Fatal("another file changed before the conflict was rejected")
	}
}

func TestNativeWorktreePreparationAndRemoval(t *testing.T) {
	repo, _ := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "all", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"native-prepared"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	if code, out, errb := run(t, "", "check", "--runtime=codex", target); code != 0 {
		t.Fatalf("native files were not ready at path return: %d %q %q", code, out, errb)
	}
	if regressionRead(t, filepath.Join(target, localRule)) != regressionRead(t, filepath.Join(repo, localRule)) {
		t.Fatal("worktree local copy differs")
	}
	if code, out, errb := run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove"); code != 0 {
		t.Fatalf("remove native generated files: %d %q %q", code, out, errb)
	}
	if exists(target) {
		t.Fatal("managed worktree was not removed")
	}
}

func TestNativeUninstallPreservesSourcesAndEditedOverride(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(linked, codexRule), "user edited native file")
	if err := UninstallRepository(context.Background(), repo, "codex"); err == nil {
		t.Fatal("edited native file removal accepted")
	}
	if regressionRead(t, filepath.Join(linked, codexRule)) != "user edited native file" {
		t.Fatal("edited file lost")
	}
	state, err := ReadRepositoryState(context.Background(), repo)
	if err != nil || !state.Disabled["codex"] || state.NativeCodex {
		t.Fatalf("provider not disabled: %+v %v", state, err)
	}
	for _, rel := range []string{sharedRule, localRule} {
		if !exists(filepath.Join(repo, rel)) {
			t.Fatalf("source removed: %s", rel)
		}
	}
}

func TestNativeOnlyWorktreePreparesPrimaryAndRegisteredFiles(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "tracked\n")
	git(t, repo, "add", "tracked.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, sharedRule), "primary shared\n")
	write(t, filepath.Join(repo, localRule), "primary private\n")
	const relative = "private value.local.json"
	write(t, filepath.Join(repo, relative), `{"value":"example"}`)
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.appendExclude("*.local.json"); err != nil {
		t.Fatal(err)
	}
	if err := UninstallRepository(context.Background(), repo, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "primary", relative); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"native-only"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	for _, rel := range []string{sharedRule, localRule, relative} {
		if regressionRead(t, filepath.Join(target, rel)) != regressionRead(t, filepath.Join(repo, rel)) {
			t.Fatalf("worktree source mismatch: %s", rel)
		}
	}
	if code, out, errb := run(t, "", "check", "--runtime=codex", target); code != 0 {
		t.Fatalf("unprepared native worktree: %d %q %q", code, out, errb)
	}
	if code, out, errb := run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove"); code != 0 {
		t.Fatalf("remove: %d %q %q", code, out, errb)
	}
}

func TestNativeSetupValidatesMergedDocumentBudgetBeforeWriting(t *testing.T) {
	for _, budget := range []int{4, 5} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, sharedRule), "ab")
			write(t, filepath.Join(repo, localRule), "c")
			write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("project_doc_max_bytes = %d\n", budget))
			err := SetupRepository(context.Background(), repo, "codex", "")
			if budget == 4 {
				if err == nil || !strings.Contains(err.Error(), "project_doc_max_bytes") {
					t.Fatalf("truncating native budget accepted: %v", err)
				}
				if exists(filepath.Join(repo, codexRule)) {
					t.Fatal("over-budget setup wrote native file")
				}
			} else if err != nil || regressionRead(t, filepath.Join(repo, codexRule)) != "ab\n\nc" {
				t.Fatalf("exact native byte budget rejected: %v", err)
			}
		})
	}
}

func TestNativeIgnoreConflictDoesNotRefreshClaudeCopies(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "all", ""); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]string)
	for _, path := range []string{filepath.Join(repo, codexRule), filepath.Join(linked, codexRule), filepath.Join(linked, localRule), filepath.Join(linked, localBridge)} {
		before[path] = regressionRead(t, path)
	}
	write(t, filepath.Join(linked, ".gitignore"), "!AGENTS.override.md\n")
	write(t, filepath.Join(repo, localRule), "changed private source\n")
	if err := SetupRepository(context.Background(), repo, "all", ""); err == nil || !strings.Contains(err.Error(), "still not ignored") {
		t.Fatalf("ignore conflict accepted: %v", err)
	}
	for path, contents := range before {
		if regressionRead(t, path) != contents {
			t.Fatalf("ignore conflict changed %s", path)
		}
	}
}

func TestWorktreeWithoutInstructionSourcesCreatesAndRemovesNormally(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "ordinary repository\n")
	git(t, repo, "add", "tracked.txt")
	seedCommit(t, repo)
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"ordinary"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("ordinary create: %d %q %q", code, out, errb)
	}
	target := strings.TrimSpace(out)
	for _, rel := range []string{sharedRule, localRule, codexRule, sharedBridge, localBridge} {
		if exists(filepath.Join(target, rel)) {
			t.Fatalf("ordinary repository received %s", rel)
		}
	}
	if code, out, errb := run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove"); code != 0 {
		t.Fatalf("ordinary remove: %d %q %q", code, out, errb)
	}
}

func TestNativeAdditionalFilePreservesOwnerExecution(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	const relative = "helper.local.sh"
	source, target := filepath.Join(repo, relative), filepath.Join(linked, relative)
	write(t, source, "#!/bin/sh\nprintf 'local helper ran'\n")
	regressionChmod(t, source, 0o755)
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.appendExclude("*.local.sh"); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "", relative); err != nil {
		t.Fatal(err)
	}
	regressionMode(t, target, 0o700)
	if out, err := exec.Command(target).CombinedOutput(); err != nil || string(out) != "local helper ran" {
		t.Fatalf("copied helper cannot execute: %q %v", out, err)
	}
	before, _ := os.Stat(target)
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged executable was rewritten")
	}
	regressionChmod(t, source, 0o600)
	if code, _, _ := run(t, "", "check", "--runtime=codex", linked); code == 0 {
		t.Fatal("stale owner execution permissions accepted")
	}
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	regressionMode(t, target, 0o600)
}

func TestNativeUninstallClearsLocalFileRegistrationsWithoutSources(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	write(t, filepath.Join(repo, "extra.bin"), "local data")
	git(t, repo, "add", sharedRule)
	seedCommit(t, repo)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.appendExclude("/extra.bin"); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "", "extra.bin"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "extra.bin"), filepath.Join(repo, "extra.saved")); err != nil {
		t.Fatal(err)
	}
	if err := UninstallRepository(context.Background(), repo, "all"); err != nil {
		t.Fatal(err)
	}
	state, err := ReadRepositoryState(context.Background(), repo)
	if err != nil || len(state.LocalFiles) != 0 || exists(filepath.Join(linked, "extra.bin")) {
		t.Fatalf("registration not cleared: %+v %v", state, err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatalf("old source path still required: %v", err)
	}
	if regressionRead(t, filepath.Join(repo, "extra.saved")) != "local data" {
		t.Fatal("original local file changed")
	}
}

func TestBareLocalFilesPrepareNewCheckoutIgnores(t *testing.T) {
	for _, operation := range []string{"setup", "create"} {
		t.Run(operation, func(t *testing.T) {
			seed := newRepo(t)
			write(t, filepath.Join(seed, sharedRule), "shared\n")
			write(t, filepath.Join(seed, ".gitignore"), "AGENTS.local.md\nAGENTS.override.md\nextra.bin\n")
			git(t, seed, "add", sharedRule, ".gitignore")
			seedCommit(t, seed)
			ignoredRef := git(t, seed, "rev-parse", "HEAD")
			write(t, filepath.Join(seed, ".gitignore"), "")
			git(t, seed, "add", ".gitignore")
			seedCommit(t, seed)
			unignoredRef := git(t, seed, "rev-parse", "HEAD")
			bare := filepath.Join(t.TempDir(), "bare.git")
			git(t, seed, "clone", "--bare", "-q", seed, bare)
			first := filepath.Join(t.TempDir(), "first")
			git(t, bare, "worktree", "add", "--detach", first, ignoredRef)
			write(t, filepath.Join(bare, localRule), "bare private\n")
			write(t, filepath.Join(bare, "extra.bin"), "extra private data")
			if err := SetupRepository(context.Background(), first, "codex", "", "extra.bin"); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(bare, "info", "exclude"), "")
			var target string
			if operation == "setup" {
				target = filepath.Join(t.TempDir(), "external")
				git(t, bare, "worktree", "add", "--detach", target, unignoredRef)
				if err := SetupRepository(context.Background(), target, "codex", ""); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
				t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF", unignoredRef)
				code, out, errb := run(t, `{"cwd":`+quote(first)+`,"name":"unignored-base"}`, "claude-worktree-create")
				if code != 0 {
					t.Fatalf("create: %d %q %q", code, out, errb)
				}
				target = strings.TrimSpace(out)
			}
			if regressionRead(t, filepath.Join(target, "extra.bin")) != "extra private data" {
				t.Fatal("additional bare file was not copied")
			}
			if code, out, errb := run(t, "", "check", "--runtime=codex", target); code != 0 {
				t.Fatalf("new checkout not prepared: %d %q %q", code, out, errb)
			}
		})
	}
}
