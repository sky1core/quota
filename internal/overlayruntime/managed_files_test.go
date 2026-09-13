package overlayruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func managedFixture(t *testing.T) (repoContext, RepositoryState, string) {
	t.Helper()
	root, linked := regressionLinkedRepo(t)
	r, err := resolveContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Common, "info", "exclude"), "/AGENTS.local.md\n/AGENTS.override.md\n/private data/\n")
	return r, RepositoryState{SharedSource: "checkout", Generated: make(map[string]string)}, resolvePath(linked)
}

func managedApply(t *testing.T, r repoContext, agent string, state *RepositoryState) []string {
	t.Helper()
	plans, err := r.planManagedFiles(agent, *state)
	if err != nil {
		t.Fatal(err)
	}
	var changes []string
	if err = r.applyManagedFiles(plans, state, &changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func managedAssertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := readRegular(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s: data %q, error %v; want %q", path, got, err, want)
	}
}

func TestManagedFilesRefreshAndStableReapply(t *testing.T) {
	r, state, linked := managedFixture(t)
	local := []byte("private body\r\n\n\n")
	write(t, r.localSource(), string(local))
	write(t, filepath.Join(linked, sharedRule), "linked shared\n\n")
	bridge := filepath.Join(linked, localBridge)
	write(t, bridge, "independent Claude bridge\n")
	if changes := managedApply(t, r, "all", &state); len(changes) != 3 {
		t.Fatalf("changes = %v", changes)
	}
	managedAssertBytes(t, filepath.Join(linked, localRule), local)
	managedAssertBytes(t, filepath.Join(linked, codexRule), append([]byte("linked shared\n\n\n"), local...))
	managedAssertBytes(t, filepath.Join(r.Root, codexRule), append([]byte("# shared rules\n\n"), local...))
	managedAssertBytes(t, bridge, []byte("independent Claude bridge\n"))
	for path := range state.Generated {
		regressionMode(t, path, 0o600)
	}
	before := make(map[string]os.FileInfo)
	for path := range state.Generated {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	if changes := managedApply(t, r, "all", &state); len(changes) != 0 {
		t.Fatalf("reapply changed files: %v", changes)
	}
	for path, old := range before {
		info, err := os.Stat(path)
		if err != nil || !os.SameFile(old, info) || !old.ModTime().Equal(info.ModTime()) {
			t.Fatalf("reapply rewrote %s: %v", path, err)
		}
	}
	if issues := r.checkManagedFiles("all", state); len(issues) != 0 {
		t.Fatal(issues)
	}
	write(t, r.localSource(), "updated private body")
	if issues := r.checkManagedFiles("all", state); len(issues) != 3 {
		t.Fatalf("source update issues = %v", issues)
	}
	if changes := managedApply(t, r, "all", &state); len(changes) != 3 {
		t.Fatalf("refresh changes = %v", changes)
	}
	managedAssertBytes(t, filepath.Join(linked, localRule), []byte("updated private body"))
	managedAssertBytes(t, filepath.Join(linked, codexRule), []byte("linked shared\n\n\nupdated private body"))
	managedAssertBytes(t, r.localSource(), []byte("updated private body"))
	managedAssertBytes(t, bridge, []byte("independent Claude bridge\n"))
	write(t, filepath.Join(linked, sharedRule), "changed checkout body")
	if changes := managedApply(t, r, "all", &state); !reflect.DeepEqual(changes, []string{filepath.Join(linked, codexRule)}) {
		t.Fatalf("checkout shared update changes = %v", changes)
	}
	managedAssertBytes(t, filepath.Join(linked, codexRule), []byte("changed checkout body\n\nupdated private body"))
}

func TestManagedFilesConflictsPreserveAllDestinations(t *testing.T) {
	for _, rel := range []string{localRule, codexRule} {
		for _, owned := range []bool{false, true} {
			t.Run(rel+"/owned="+map[bool]string{false: "false", true: "true"}[owned], func(t *testing.T) {
				r, state, linked := managedFixture(t)
				managedApply(t, r, "codex", &state)
				conflict := filepath.Join(linked, rel)
				if owned {
					write(t, conflict, "user edited generated file\n")
				} else {
					delete(state.Generated, conflict)
				}
				before := make(map[string]string)
				for _, path := range []string{filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(r.Root, codexRule)} {
					before[path] = regressionRead(t, path)
				}
				plans, err := r.planManagedFiles("codex", state)
				if err == nil || plans != nil || !strings.Contains(err.Error(), "ownership") {
					t.Fatalf("conflict plan = %v, %v", plans, err)
				}
				write(t, r.localSource(), "new source\n")
				if _, err = r.planManagedFiles("codex", state); err == nil {
					t.Fatal("source update accepted conflict")
				}
				for path, want := range before {
					managedAssertBytes(t, path, []byte(want))
				}
			})
		}
	}
}

func TestManagedFilesApplyRevalidatesEveryDestination(t *testing.T) {
	for _, mutation := range []string{"edit", "create", "remove", "track", "unignore", "parent"} {
		t.Run(mutation, func(t *testing.T) {
			r, state, linked := managedFixture(t)
			state.LocalFiles = []string{"private data/nested/file.bin"}
			if err := os.MkdirAll(filepath.Join(r.Root, "private data/nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(r.Root, state.LocalFiles[0]), "payload")
			if mutation != "create" && mutation != "parent" {
				managedApply(t, r, "codex", &state)
			}
			write(t, r.localSource(), "source update\n")
			plans, err := r.planManagedFiles("codex", state)
			if err != nil {
				t.Fatal(err)
			}
			last := filepath.Join(linked, codexRule)
			switch mutation {
			case "edit", "create":
				write(t, last, "user body")
			case "remove":
				if err := os.Remove(last); err != nil {
					t.Fatal(err)
				}
			case "track":
				git(t, linked, "add", "-f", codexRule)
			case "unignore":
				write(t, filepath.Join(r.Common, "info", "exclude"), "/AGENTS.local.md\n/private data/\n")
			case "parent":
				if err := os.Symlink(t.TempDir(), filepath.Join(linked, "private data")); err != nil {
					t.Fatal(err)
				}
			}
			var changes []string
			if err = r.applyManagedFiles(plans, &state, &changes); err == nil || len(changes) != 0 {
				t.Fatalf("apply after %s: changes %v, error %v", mutation, changes, err)
			}
			first := filepath.Join(linked, localRule)
			if mutation == "create" || mutation == "parent" {
				if exists(first) {
					t.Fatalf("created %s before detecting conflict", first)
				}
			} else {
				managedAssertBytes(t, first, []byte("private rule one\n"))
			}
		})
	}
}

func TestManagedFilesExtraLocalBinaryAndClaudeSelection(t *testing.T) {
	r, state, linked := managedFixture(t)
	state.LocalFiles = []string{"private data/nested/key bytes.bin"}
	source := filepath.Join(r.Root, state.LocalFiles[0])
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 0xff, 0xfe, '\r', '\n', 42}
	write(t, source, string(data))
	if changes := managedApply(t, r, "claude", &state); len(changes) != 2 {
		t.Fatalf("changes = %v", changes)
	}
	managedAssertBytes(t, filepath.Join(linked, state.LocalFiles[0]), data)
	regressionMode(t, filepath.Join(linked, state.LocalFiles[0]), 0o600)
	regressionMode(t, filepath.Dir(filepath.Join(linked, state.LocalFiles[0])), 0o700)
	if exists(filepath.Join(linked, codexRule)) || exists(filepath.Join(r.Root, codexRule)) {
		t.Fatal("Claude selection created Codex overrides")
	}
	if issues := r.checkManagedFiles("claude", state); len(issues) != 0 {
		t.Fatal(issues)
	}
	write(t, source, "binary source update\x00")
	managedApply(t, r, "claude", &state)
	managedAssertBytes(t, filepath.Join(linked, state.LocalFiles[0]), []byte("binary source update\x00"))
}

func TestManagedFilesSharedPoliciesAndLocalOnly(t *testing.T) {
	for _, policy := range []string{"checkout", "primary", "local-only", "empty-local"} {
		t.Run(policy, func(t *testing.T) {
			r, state, linked := managedFixture(t)
			write(t, filepath.Join(linked, sharedRule), "checkout body")
			want := "checkout body\n\nprivate rule one\n"
			if policy == "primary" {
				state.SharedSource = "primary"
				want = "# shared rules\n\nprivate rule one\n"
			}
			if policy == "local-only" || policy == "empty-local" {
				for _, w := range r.Checkouts() {
					if err := os.Remove(filepath.Join(w, sharedRule)); err != nil {
						t.Fatal(err)
					}
				}
				want = "private rule one\n"
				if policy == "empty-local" {
					write(t, r.localSource(), "")
					want = ""
				}
			}
			managedApply(t, r, "codex", &state)
			managedAssertBytes(t, filepath.Join(linked, codexRule), []byte(want))
			if issues := r.checkManagedFiles("codex", state); len(issues) != 0 {
				t.Fatal(issues)
			}
		})
	}
}

func TestManagedFilesMissingSourceCleanup(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "edited"}[edited], func(t *testing.T) {
			r, state, linked := managedFixture(t)
			managedApply(t, r, "codex", &state)
			if edited {
				write(t, filepath.Join(linked, codexRule), "user edit")
			}
			if err := os.Remove(r.localSource()); err != nil {
				t.Fatal(err)
			}
			if edited {
				if _, err := r.planManagedFiles("codex", state); err == nil {
					t.Fatal("cleanup accepted edited override")
				}
				managedAssertBytes(t, filepath.Join(linked, codexRule), []byte("user edit"))
				managedAssertBytes(t, filepath.Join(linked, localRule), []byte("private rule one\n"))
				return
			}
			if issues := r.checkManagedFiles("codex", state); len(issues) != 3 {
				t.Fatalf("cleanup issues = %v", issues)
			}
			if changes := managedApply(t, r, "codex", &state); len(changes) != 3 {
				t.Fatalf("cleanup changes = %v", changes)
			}
			for _, path := range []string{filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(r.Root, codexRule)} {
				if exists(path) || state.Generated[path] != "" {
					t.Fatalf("cleanup retained %s", path)
				}
			}
			if changes := managedApply(t, r, "codex", &state); len(changes) != 0 {
				t.Fatalf("cleanup reapply changes = %v", changes)
			}
		})
	}
}

func TestManagedFilesMissingExtraSourceIsError(t *testing.T) {
	r, state, linked := managedFixture(t)
	state.LocalFiles = []string{"private data/missing.bin"}
	if _, err := r.planManagedFiles("codex", state); err == nil || !strings.Contains(err.Error(), "local source") {
		t.Fatalf("missing extra source error = %v", err)
	}
	if exists(filepath.Join(linked, localRule)) || exists(filepath.Join(r.Root, codexRule)) {
		t.Fatal("failed plan wrote files")
	}
	source := filepath.Join(r.Root, state.LocalFiles[0])
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, source, "private bytes")
	write(t, filepath.Join(filepath.Dir(source), "unlisted.bin"), "unlisted bytes")
	managedApply(t, r, "codex", &state)
	if exists(filepath.Join(linked, "private data/unlisted.bin")) {
		t.Fatal("copied unlisted ignored file")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := r.planManagedFiles("codex", state); err == nil {
		t.Fatal("missing extra source scheduled cleanup")
	}
	managedAssertBytes(t, filepath.Join(linked, state.LocalFiles[0]), []byte("private bytes"))
}

func TestManagedFilesRemovalRevalidatesOwnership(t *testing.T) {
	r, state, linked := managedFixture(t)
	managedApply(t, r, "codex", &state)
	if err := os.Remove(r.localSource()); err != nil {
		t.Fatal(err)
	}
	plans, err := r.planManagedFiles("codex", state)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(linked, codexRule), "edited after removal plan")
	var changes []string
	if err = r.applyManagedFiles(plans, &state, &changes); err == nil || len(changes) != 0 {
		t.Fatalf("removal after edit: changes %v, error %v", changes, err)
	}
	managedAssertBytes(t, filepath.Join(linked, localRule), []byte("private rule one\n"))
	managedAssertBytes(t, filepath.Join(linked, codexRule), []byte("edited after removal plan"))
}

func TestManagedFilesRejectUnsafePaths(t *testing.T) {
	for _, paths := range [][]string{
		{""}, {"/absolute"}, {"."}, {".."}, {"a/../b"}, {"./a"}, {"a//b"}, {"a/"},
		{".git/config"}, {"a/.GIT/config"}, {localRule}, {codexRule}, {sharedRule}, {sharedBridge}, {localBridge},
		{"AGENTS.local.md/child"}, {"a", "a"}, {"a", "A"}, {"a/b", "a"},
		{"a:b"}, {"a\\b"}, {"a\x00b"}, {"*.env"}, {"a\nb"},
	} {
		if err := validateLocalFiles(paths); err == nil {
			t.Errorf("accepted %q", paths)
		}
	}
	if err := validateLocalFiles([]string{"private data/nested/key.bin", "another.env"}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedFilesRejectUnsafeFilesystemAndTracking(t *testing.T) {
	for _, failure := range []string{"source-symlink", "source-parent-symlink", "target-symlink", "target-parent-symlink", "target-parent-file", "source-parent-file", "tracked-source", "tracked-target", "tracked-root-local", "tracked-override", "unignored-source"} {
		t.Run(failure, func(t *testing.T) {
			r, state, linked := managedFixture(t)
			state.LocalFiles = []string{"private data/file.bin"}
			source := filepath.Join(r.Root, state.LocalFiles[0])
			target := filepath.Join(linked, state.LocalFiles[0])
			for _, dir := range []string{filepath.Dir(source), filepath.Dir(target)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(t, source, "payload")
			symlink := func(path string) {
				t.Helper()
				if exists(path) {
					if err := os.Rename(path, path+"-original"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			}
			switch failure {
			case "source-symlink":
				symlink(source)
			case "source-parent-symlink":
				symlink(filepath.Dir(source))
			case "target-symlink":
				symlink(target)
			case "target-parent-symlink":
				symlink(filepath.Dir(target))
			case "target-parent-file", "source-parent-file":
				path := filepath.Dir(target)
				if failure == "source-parent-file" {
					path = filepath.Dir(source)
				}
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				write(t, path, "not a directory")
			case "tracked-source":
				git(t, r.Root, "add", "-f", state.LocalFiles[0])
			case "tracked-target":
				write(t, target, "payload")
				state.Generated[target] = digest([]byte("payload"))
				git(t, linked, "add", "-f", state.LocalFiles[0])
			case "tracked-root-local":
				git(t, r.Root, "add", "-f", localRule)
			case "tracked-override":
				write(t, filepath.Join(linked, codexRule), "override")
				git(t, linked, "add", "-f", codexRule)
			case "unignored-source":
				write(t, filepath.Join(r.Common, "info", "exclude"), "/AGENTS.local.md\n/AGENTS.override.md\n")
			}
			if _, err := r.planManagedFiles("codex", state); err == nil {
				t.Fatalf("accepted %s", failure)
			}
			if exists(filepath.Join(r.Root, codexRule)) {
				t.Fatal("plan wrote root override")
			}
		})
	}
}

func TestManagedFilesIgnorePolicyAndCheckPermissions(t *testing.T) {
	r, state, linked := managedFixture(t)
	exclude := filepath.Join(r.Common, "info", "exclude")
	write(t, exclude, "")
	plans, err := r.planManagedFiles("codex", state)
	if err != nil {
		t.Fatal(err)
	}
	var changes []string
	if err = r.applyManagedFiles(plans, &state, &changes); err == nil || len(changes) != 0 {
		t.Fatalf("unignored apply: %v, %v", changes, err)
	}
	write(t, exclude, "/AGENTS.local.md\n/AGENTS.override.md\n")
	if err = r.applyManagedFiles(plans, &state, &changes); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linked, codexRule)
	regressionChmod(t, path, 0o644)
	if issues := r.checkManagedFiles("codex", state); len(issues) != 1 || !strings.Contains(issues[0], "permissions") {
		t.Fatalf("permissions issues = %v", issues)
	}
	regressionMode(t, path, 0o644)
	if _, err = r.planManagedFiles("codex", state); err == nil || !strings.Contains(err.Error(), "permissions changed") {
		t.Fatalf("plan accepted changed permissions: %v", err)
	}
	regressionMode(t, path, 0o644)
	regressionChmod(t, path, 0o600)
	if changes = managedApply(t, r, "codex", &state); len(changes) != 0 {
		t.Fatalf("unchanged generated file rewritten: %v", changes)
	}
	regressionMode(t, path, 0o600)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if issues := r.checkManagedFiles("codex", state); len(issues) != 1 || !strings.Contains(issues[0], "missing") {
		t.Fatalf("missing issues = %v", issues)
	}
}

func TestManagedFilesBarePrimary(t *testing.T) {
	root, _ := regressionLinkedRepo(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	git(t, root, "clone", "--bare", "-q", root, bare)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, bare, "worktree", "add", "-q", "--detach", linked, "HEAD")
	r, err := resolveContext(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Bare() {
		t.Fatal("fixture is not bare")
	}
	write(t, r.localSource(), "bare private source")
	write(t, filepath.Join(r.Common, "info", "exclude"), "/AGENTS.local.md\n/AGENTS.override.md\n/private.bin\n")
	write(t, filepath.Join(r.Root, "private.bin"), "\x00\xff")
	state := RepositoryState{SharedSource: "checkout", LocalFiles: []string{"private.bin"}}
	managedApply(t, r, "codex", &state)
	managedAssertBytes(t, filepath.Join(r.Top, localRule), []byte("bare private source"))
	managedAssertBytes(t, filepath.Join(r.Top, "private.bin"), []byte{0, 0xff})
	if exists(filepath.Join(r.Root, codexRule)) {
		t.Fatal("override generated in bare primary")
	}
	git(t, r.Top, "add", "-f", "private.bin")
	if _, err := r.planManagedFiles("codex", state); err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("bare tracked source error = %v", err)
	}
}
