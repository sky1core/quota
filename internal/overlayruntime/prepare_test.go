package overlayruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func testHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
	t.Setenv("QUOTA_INSTRUCTIONS_CLAUDE_WORKTREE_DIR", filepath.Join(home, "worktrees"))
	write(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n")
	return home
}

func globalIgnore(t *testing.T, lines ...string) {
	t.Helper()
	if contains(lines, ".claude/AGENTS.md") && !contains(lines, ".claude/CLAUDE.md") {
		lines = append(lines, ".claude/CLAUDE.md")
	}
	write(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "ignore"), strings.Join(lines, "\n")+"\n")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := resolvePath(filepath.Join(t.TempDir(), "repo"))
	return newRepoAt(t, dir)
}

func newRepoAt(t *testing.T, dir string) string {
	t.Helper()
	dir = resolvePath(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "AGENTS.md"), "# shared rules\n")
	write(t, filepath.Join(dir, ".gitignore"), "AGENTS.local.md\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func addWorktree(t *testing.T, repo, name string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(repo), name)
	git(t, repo, "worktree", "add", "-q", "-b", name, dir)
	return resolvePath(dir)
}

func gitTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(filepath.Join(dir, ".git"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path+"|"+info.ModTime().String()+"|"+string(rune(info.Size())))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func prepare(t *testing.T, dir string) PrepareResult {
	t.Helper()
	res, err := PrepareCheckout(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode %o, want %o", path, info.Mode().Perm(), want)
	}
}

func stateFileFor(t *testing.T, dir string) string {
	t.Helper()
	r, err := resolveContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := statePath(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepareCreatesRefreshesAndRemovesGeneratedFiles(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	before := gitTree(t, repo)
	res := prepare(t, repo)
	bridge, override := filepath.Join(repo, ".claude/AGENTS.md"), filepath.Join(repo, "AGENTS.override.md")
	if !res.LocalPresent || res.LocalBody != "private body" || len(res.Skipped) != 0 || !res.Changed(bridge) || !res.Changed(override) || len(res.Created) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, bridge) != "@../AGENTS.local.md\n" || read(t, override) != "# shared rules\n\nprivate body\n" {
		t.Fatalf("bridge=%q override=%q", read(t, bridge), read(t, override))
	}
	assertMode(t, override, 0o600)
	if after := gitTree(t, repo); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf(".git changed:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
	state := stateFileFor(t, repo)
	if !strings.HasPrefix(state, filepath.Join(home, ".config", "quota", "instructions")+string(filepath.Separator)) {
		t.Fatalf("state path %s", state)
	}
	assertMode(t, state, 0o600)
	assertMode(t, state+".lock", 0o600)
	var saved RepositoryState
	if err := json.Unmarshal([]byte(read(t, state)), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Generated) != 2 || saved.GeneratedModes[override] == 0 {
		t.Fatalf("state = %+v", saved)
	}
	stateInfo, _ := os.Stat(state)
	res = prepare(t, repo)
	if len(res.Created)+len(res.Updated)+len(res.Removed)+len(res.Skipped) != 0 {
		t.Fatalf("second preparation changed files: %+v", res)
	}
	if again, _ := os.Stat(state); !again.ModTime().Equal(stateInfo.ModTime()) {
		t.Fatal("unchanged state was rewritten")
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "changed body\n")
	res = prepare(t, repo)
	if !res.Changed(override) || res.Changed(bridge) || len(res.Updated) != 1 {
		t.Fatalf("refresh result = %+v", res)
	}
	if read(t, override) != "# shared rules\n\nchanged body\n" {
		t.Fatalf("override = %q", read(t, override))
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, repo)
	if res.LocalPresent || len(res.Removed) != 2 || exists(bridge) || exists(override) {
		t.Fatalf("removal result = %+v", res)
	}
	saved = RepositoryState{}
	if err := json.Unmarshal([]byte(read(t, state)), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Generated) != 0 || len(saved.GeneratedModes) != 0 {
		t.Fatalf("state after removal = %+v", saved)
	}
	if after := gitTree(t, repo); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatal(".git changed during refresh or removal")
	}
}

func TestPrepareSkipsUnignoredFilesWithReason(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	res := prepare(t, repo)
	if len(res.Created) != 0 || len(res.Skipped) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for _, skip := range res.Skipped {
		rel, err := filepath.Rel(repo, skip.Path)
		if err != nil {
			t.Fatal(err)
		}
		if exists(skip.Path) || !strings.Contains(skip.Reason, "not git-ignored") || !strings.Contains(skip.Reason, `"`+rel+`"`) || !strings.Contains(skip.Reason, "global git ignore") {
			t.Fatalf("skip = %+v", skip)
		}
	}
	globalIgnore(t, "AGENTS.override.md")
	res = prepare(t, repo)
	if len(res.Created) != 1 || len(res.Skipped) != 1 || filepath.Base(res.Created[0]) != "AGENTS.override.md" || res.Skipped[0].Path != filepath.Join(repo, ".claude", "AGENTS.md") {
		t.Fatalf("partial result = %+v", res)
	}
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	res = prepare(t, repo)
	if len(res.Created) != 1 || len(res.Skipped) != 0 || res.Created[0] != filepath.Join(repo, ".claude", "AGENTS.md") {
		t.Fatalf("completed result = %+v", res)
	}
	globalIgnore(t, ".claude/AGENTS.md")
	res = prepare(t, repo)
	if len(res.Created) != 0 || len(res.Updated) != 0 || len(res.Skipped) != 1 || filepath.Base(res.Skipped[0].Path) != "AGENTS.override.md" || !strings.Contains(res.Skipped[0].Reason, "not git-ignored") || !exists(res.Skipped[0].Path) {
		t.Fatalf("ignore drift result = %+v", res)
	}
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || len(status.Problems) == 0 || !strings.Contains(strings.Join(status.Problems, "\n"), res.Skipped[0].Path+": "+res.Skipped[0].Reason) {
		t.Fatalf("status after ignore drift = %+v, %v", status, err)
	}
}

func TestPrepareReportsUnownedClaudeLocalBridgeAfterLocalRemoval(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	bridge := filepath.Join(repo, ".claude", "AGENTS.md")
	write(t, bridge, "@../AGENTS.local.md\n")
	res := prepare(t, repo)
	if len(res.Skipped) != 1 || res.Skipped[0].Path != bridge || !strings.Contains(res.Skipped[0].Reason, "not quota-generated") {
		t.Fatalf("unowned stale bridge was not reported: %+v", res)
	}
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "not quota-generated") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
}

func TestPreparePreservesUserFilesAndEditedGeneratedFiles(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	bridge, override := filepath.Join(repo, ".claude/AGENTS.md"), filepath.Join(repo, "AGENTS.override.md")
	write(t, override, "# shared rules\n\nprivate body\n")
	write(t, bridge, "@../AGENTS.local.md\n")
	res := prepare(t, repo)
	if len(res.Created) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Path != override || !strings.Contains(res.Skipped[0].Reason, "not quota-generated") {
		t.Fatalf("identical user override adopted: %+v", res)
	}
	if read(t, override) != "# shared rules\n\nprivate body\n" || read(t, bridge) != "@../AGENTS.local.md\n" {
		t.Fatal("user files rewritten")
	}
	write(t, bridge, "# my own notes\n")
	res = prepare(t, repo)
	if len(res.Skipped) != 2 || res.Skipped[0].Path != bridge || read(t, bridge) != "# my own notes\n" {
		t.Fatalf("user bridge not reported or changed: %+v", res)
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bridge); err != nil {
		t.Fatal(err)
	}
	if res = prepare(t, repo); len(res.Created) != 2 {
		t.Fatalf("result = %+v", res)
	}
	write(t, override, "# shared rules\n\nedited by user\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "new body\n")
	res = prepare(t, repo)
	if len(res.Updated) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "user edits") || read(t, override) != "# shared rules\n\nedited by user\n" {
		t.Fatalf("edited generated file not preserved: %+v", res)
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, repo)
	if len(res.Removed) != 1 || res.Removed[0] != bridge || !exists(override) || len(res.Skipped) != 1 {
		t.Fatalf("edited generated file removed or unchanged bridge kept: %+v", res)
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "new body\n")
	if res = prepare(t, repo); len(res.Created) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if err := os.Chmod(override, 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "third body\n")
	res = prepare(t, repo)
	if len(res.Updated) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "permissions changed") {
		t.Fatalf("permission change not preserved: %+v", res)
	}
	assertMode(t, override, 0o644)
}

func TestRemoveRechecksGeneratedFileBeforeDelete(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(t *testing.T, path string)
		want        string
		wantSymlink bool
	}{
		{
			name: "edited",
			mutate: func(t *testing.T, path string) {
				write(t, path, "user edit\n")
			},
			want: "user edit\n",
		},
		{
			name: "replaced",
			mutate: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				tmp := filepath.Join(filepath.Dir(path), ".replacement")
				if err := os.WriteFile(tmp, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			},
			want: "# shared rules\n\nprivate body\n",
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, path string) {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("AGENTS.md", path); err != nil {
					t.Fatal(err)
				}
			},
			wantSymlink: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testHome(t)
			globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
			prepare(t, repo)
			target := filepath.Join(repo, "AGENTS.override.md")
			if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
				t.Fatal(err)
			}
			r, err := resolveContext(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			state, err := readState(r)
			if err != nil {
				t.Fatal(err)
			}
			var remove plannedAction
			for _, a := range r.evaluateCheckout(repo, state, nil) {
				if a.Path == target && a.Action == actionRemove {
					remove = a
					break
				}
			}
			if remove.Path == "" {
				t.Fatal("remove action not planned")
			}
			tc.mutate(t, target)
			var res PrepareResult
			r.applyAction(repo, remove, &state, &res, nil)
			if len(res.Removed) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "changed") && !strings.Contains(res.Skipped[0].Reason, "user edits") && !strings.Contains(res.Skipped[0].Reason, "regular") {
				t.Fatalf("result = %+v, want skipped removal", res)
			}
			if tc.want != "" && read(t, target) != tc.want {
				t.Fatalf("target changed: %q", read(t, target))
			}
			if tc.wantSymlink {
				info, err := os.Lstat(target)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("target mode = %v, want symlink", info.Mode())
				}
			}
			if state.Generated[target] == "" {
				t.Fatal("ownership record was dropped for a preserved file")
			}
		})
	}
}

func TestUserOverrideWithoutLocalSourceIsReported(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	override := filepath.Join(repo, "AGENTS.override.md")
	write(t, override, "user override\n")
	res := prepare(t, repo)
	if len(res.Skipped) != 1 || res.Skipped[0].Path != override || !strings.Contains(res.Skipped[0].Reason, "not quota-generated") {
		t.Fatalf("unowned override hidden: %+v", res)
	}
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || len(status.Problems) == 0 || !strings.Contains(strings.Join(status.Problems, "\n"), "not quota-generated") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
}

func TestPrepareRejectsTrackedOrInvalidLocalSource(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	local := filepath.Join(repo, "AGENTS.local.md")
	write(t, local, "private\n")
	git(t, repo, "add", "-f", "AGENTS.local.md")
	if _, err := PrepareCheckout(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("tracked private source accepted: %v", err)
	}
	git(t, repo, "rm", "-q", "--cached", "AGENTS.local.md")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "AGENTS.md"), local); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCheckout(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink source accepted: %v", err)
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	write(t, local, "bad\x00byte\n")
	if _, err := PrepareCheckout(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("NUL source accepted: %v", err)
	}
	if exists(filepath.Join(repo, ".claude/AGENTS.md")) || exists(filepath.Join(repo, "AGENTS.override.md")) {
		t.Fatal("files written despite invalid source")
	}
	write(t, filepath.Join(repo, "AGENTS.override.md"), "user\n")
	git(t, repo, "add", "-f", "AGENTS.override.md")
	write(t, local, "private\n")
	res := prepare(t, repo)
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != "is tracked; it must stay local-only" || read(t, filepath.Join(repo, "AGENTS.override.md")) != "user\n" {
		t.Fatalf("tracked override changed: %+v", res)
	}
}

func TestPrepareLinkedWorktreeCopiesLocalSourceAndRegisteredFiles(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n/config/\n")
	git(t, repo, "commit", "-q", "-am", "ignore config")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(repo, "config", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files, err := AddLocalFiles(ctx, repo, []string{"config/run.sh"})
	if err != nil || strings.Join(files, ",") != "config/run.sh" {
		t.Fatalf("register: %v %v", files, err)
	}
	linked := addWorktree(t, repo, "feature")
	write(t, filepath.Join(linked, "AGENTS.md"), "# linked rules\n")
	res := prepare(t, linked)
	if len(res.Skipped) != 0 || len(res.Created) != 4 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body\n" || read(t, filepath.Join(linked, "AGENTS.override.md")) != "# linked rules\n\nprivate body\n" || read(t, filepath.Join(linked, ".claude/AGENTS.md")) != "@../AGENTS.local.md\n" || read(t, filepath.Join(linked, "config", "run.sh")) != "#!/bin/sh\n" {
		t.Fatal("copies differ from sources")
	}
	assertMode(t, filepath.Join(linked, "AGENTS.local.md"), 0o600)
	assertMode(t, filepath.Join(linked, "config", "run.sh"), 0o700)
	if exists(filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatal("primary checkout was prepared while preparing the linked worktree")
	}
	if err := os.Chmod(filepath.Join(repo, "config", "run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, linked)
	if len(res.Updated) != 1 || filepath.Base(res.Updated[0]) != "run.sh" {
		t.Fatalf("execution bit change not refreshed: %+v", res)
	}
	assertMode(t, filepath.Join(linked, "config", "run.sh"), 0o600)
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, linked)
	if len(res.Removed) != 4 || exists(filepath.Join(linked, "AGENTS.local.md")) || exists(filepath.Join(linked, "config", "run.sh")) {
		t.Fatalf("copies not removed: %+v", res)
	}
}

func TestLocalFileRegistrationRules(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n/config/\n")
	git(t, repo, "commit", "-q", "-am", "ignore config")
	write(t, filepath.Join(repo, "config", "app.json"), "{}\n")
	write(t, filepath.Join(repo, "config", "café.txt"), "x\n")
	write(t, filepath.Join(repo, "tracked.txt"), "x\n")
	git(t, repo, "add", "tracked.txt")
	ctx := context.Background()
	for _, bad := range []string{".gitignore", "config/.gitignore", "AGENTS.override.md", ".claude/AGENTS.md", "claude.local.md", "/etc/passwd", "../outside", "config/../config/app.json", "config/missing.json", "tracked.txt", "AGENTS.md"} {
		if _, err := AddLocalFiles(ctx, repo, []string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := AddLocalFiles(ctx, repo, []string{"config/app.json", "Config/App.json"}); err == nil {
		t.Fatal("case duplicate accepted")
	}
	if _, err := AddLocalFiles(ctx, repo, []string{"config/café.txt", "config/café.txt"}); err == nil {
		t.Fatal("Unicode normalization duplicate accepted")
	}
	if files, err := ListLocalFiles(ctx, repo); err != nil || len(files) != 0 {
		t.Fatalf("rejected registrations were saved: %v %v", files, err)
	}
	files, err := AddLocalFiles(ctx, repo, []string{"config/app.json", "config/app.json"})
	if err != nil || strings.Join(files, ",") != "config/app.json" {
		t.Fatalf("register: %v %v", files, err)
	}
	if _, err := AddLocalFiles(ctx, repo, []string{"CONFIG/app.json"}); err == nil {
		t.Fatal("case alias of registered path accepted")
	}
	if _, err := RemoveLocalFiles(ctx, repo, []string{"config/other.json"}); err == nil {
		t.Fatal("unregistered removal accepted")
	}
	files, err = RemoveLocalFiles(ctx, repo, []string{"config/app.json"})
	if err != nil || len(files) != 0 {
		t.Fatalf("remove: %v %v", files, err)
	}
	if !exists(filepath.Join(repo, "config", "app.json")) {
		t.Fatal("source file removed")
	}
}

func TestLegacyStateIsReadForOwnershipOnly(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	override := filepath.Join(repo, "AGENTS.override.md")
	write(t, override, "# shared rules\n\nold body\n")
	if err := os.Chmod(override, 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(override)
	legacy := filepath.Join(repo, ".git", "quota-instructions.json")
	legacyBody, _ := json.Marshal(map[string]any{"disabled": map[string]bool{}, "shared_source": "checkout", "generated": map[string]string{override: digest([]byte("# shared rules\n\nold body\n"))}, "generated_modes": map[string]uint32{override: uint32(info.Mode())}, "native_codex": true})
	if err := os.WriteFile(legacy, legacyBody, 0o600); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, repo)
	if !res.Changed(override) || len(res.Updated) != 1 || read(t, override) != "# shared rules\n\nprivate body\n" {
		t.Fatalf("legacy ownership not honored: %+v", res)
	}
	if read(t, legacy) != string(legacyBody) {
		t.Fatal("legacy state was rewritten")
	}
	state := stateFileFor(t, repo)
	if !exists(state) || strings.Contains(read(t, state), "native_codex") {
		t.Fatalf("new state = %q", read(t, state))
	}
	if err := os.WriteFile(state, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCheckout(context.Background(), repo); err == nil {
		t.Fatal("corrupt state was replaced by defaults")
	}
	if read(t, state) != "{not json" {
		t.Fatal("corrupt state rewritten")
	}
	if err := os.Chmod(state, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCheckout(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("group-readable state accepted: %v", err)
	}
}

func TestPrepareBareRepositoryCheckout(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	source := newRepo(t)
	bare := filepath.Join(filepath.Dir(source), "bare.git")
	git(t, source, "clone", "-q", "--bare", source, bare)
	bare = resolvePath(bare)
	checkout := filepath.Join(filepath.Dir(source), "checkout")
	git(t, bare, "worktree", "add", "-q", checkout, "main")
	checkout = resolvePath(checkout)
	write(t, filepath.Join(bare, "AGENTS.local.md"), "bare private\n")
	res := prepare(t, checkout)
	if res.Primary != bare || len(res.Skipped) != 0 || len(res.Created) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, filepath.Join(checkout, "AGENTS.local.md")) != "bare private\n" || read(t, filepath.Join(checkout, "AGENTS.override.md")) != "# shared rules\n\nbare private\n" {
		t.Fatal("bare copies differ")
	}
	if entries, _ := filepath.Glob(filepath.Join(bare, "quota-instructions*")); len(entries) != 0 {
		t.Fatalf("state written into the bare repository: %v", entries)
	}
}

func hook(t *testing.T, agent, event string, input map[string]any) (int, string, string) {
	t.Helper()
	b, _ := json.Marshal(input)
	var stdout, stderr bytes.Buffer
	code := RunPrepareHook(context.Background(), agent, event, bytes.NewReader(b), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func additionalContext(t *testing.T, stdout string) string {
	t.Helper()
	if stdout == "" {
		return ""
	}
	var out struct {
		Hook map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("hook output %q: %v", stdout, err)
	}
	return out.Hook["additionalContext"]
}

func TestSessionStartDeliversBodyOnlyOnFirstPreparedStartup(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "resume"})
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume delivered: %d %q %q", code, stdout, stderr)
	}
	if !exists(filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatal("resume did not prepare the checkout")
	}
	if err := os.Remove(filepath.Join(repo, ".claude/AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body" || !exists(filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatalf("startup after creation: %d %q", code, stdout)
	}
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stdout != "" {
		t.Fatalf("unchanged startup delivered: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "codex body\n")
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stdout != "" {
		t.Fatalf("Claude delivered on override-only refresh: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "codex body 2\n")
	code, stdout, _ = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "# shared rules\n\ncodex body 2\n" {
		t.Fatalf("Codex startup after override refresh must deliver the merged override: %d %q", code, stdout)
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "resume"})
	if code != 0 || stdout != "" {
		t.Fatalf("Codex resume delivered: %d %q", code, stdout)
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "codex body 2" {
		t.Fatalf("Codex startup after override creation must deliver the local body: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "codex body 3\n")
	code, stdout, _ = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "compact"})
	if code != 0 || stdout != "" || read(t, filepath.Join(repo, "AGENTS.override.md")) != "# shared rules\n\ncodex body 3\n" {
		t.Fatalf("compact delivered or did not refresh: %d %q", code, stdout)
	}
	code, stdout, _ = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo})
	if code != 0 || stdout != "" {
		t.Fatalf("missing source delivered: %d %q", code, stdout)
	}
	outside := t.TempDir()
	code, stdout, stderr = hook(t, "claude", "SessionStart", map[string]any{"cwd": outside, "source": "startup"})
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("non-repository: %d %q %q", code, stdout, stderr)
	}
	var out bytes.Buffer
	if code := RunPrepareHook(context.Background(), "claude", "SessionStart", strings.NewReader("not json"), &out, &out); code != 1 {
		t.Fatalf("malformed input exit %d", code)
	}
	if code := RunPrepareHook(context.Background(), "codex", "WorktreeCreate", strings.NewReader("{}"), &out, &out); code != 2 {
		t.Fatalf("unsupported combination exit %d", code)
	}
}

func TestClaudeNativeDisabledDoesNotFallbackInjectBody(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.Contains(context, "CLAUDE_CODE_DISABLE_CLAUDE_MDS") || strings.Contains(context, "private body") {
		t.Fatalf("disabled native channel leaked body: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestClaudeStartupDeliversWhenClaudeBridgeIsCreated(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stderr != "" || additionalContext(t, stdout) != "private body" {
		t.Fatalf("startup after Claude bridge creation: %d %q %q", code, stdout, stderr)
	}
	if read(t, filepath.Join(repo, ".claude", "CLAUDE.md")) != "@../AGENTS.local.md\n" {
		t.Fatalf("Claude local bridge was not created")
	}
	code, stdout, stderr = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stderr != "" || stdout != "" {
		t.Fatalf("unchanged startup delivered: %d %q %q", code, stdout, stderr)
	}
}

func TestClaudeStartupBridgeMigrationDeliversOnlyMissingBody(t *testing.T) {
	t.Run("root Claude removed after local bridge existed", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
		prepare(t, repo)
		if err := os.Remove(filepath.Join(repo, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
		if code != 0 || stderr != "" || additionalContext(t, stdout) != "shared body" {
			t.Fatalf("startup after bridge migration: %d %q %q", code, stdout, stderr)
		}
		if !exists(filepath.Join(repo, ".claude", "AGENTS.md")) || exists(filepath.Join(repo, ".claude", "CLAUDE.md")) {
			t.Fatalf("unexpected bridge files after migration")
		}
	})

	t.Run("legacy local with root Claude was already read", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "CLAUDE.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
		legacy := filepath.Join(repo, "CLAUDE.local.md")
		write(t, legacy, "@AGENTS.local.md\n")
		recordGeneratedForTest(t, repo, legacy)
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
		if code != 0 || stderr != "" || stdout != "" {
			t.Fatalf("legacy local duplicate delivered: %d %q %q", code, stdout, stderr)
		}
		if exists(legacy) || !exists(filepath.Join(repo, ".claude", "CLAUDE.md")) {
			t.Fatalf("legacy local was not replaced")
		}
	})

	t.Run("legacy local without root Claude needs shared body", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "CLAUDE.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		legacy := filepath.Join(repo, "CLAUDE.local.md")
		write(t, legacy, "@AGENTS.local.md\n")
		recordGeneratedForTest(t, repo, legacy)
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
		if code != 0 || stderr != "" || additionalContext(t, stdout) != "shared body" {
			t.Fatalf("legacy local migration did not deliver shared body: %d %q %q", code, stdout, stderr)
		}
		if exists(legacy) || !exists(filepath.Join(repo, ".claude", "AGENTS.md")) {
			t.Fatalf("legacy local was not replaced")
		}
	})

	t.Run("owned root and local bridges were already read", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "CLAUDE.md", "CLAUDE.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		rootBridge := filepath.Join(repo, "CLAUDE.md")
		localBridge := filepath.Join(repo, "CLAUDE.local.md")
		write(t, rootBridge, "@AGENTS.md\n")
		write(t, localBridge, "@AGENTS.local.md\n")
		recordGeneratedForTest(t, repo, rootBridge)
		recordGeneratedForTest(t, repo, localBridge)
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
		if code != 0 || stderr != "" || stdout != "" {
			t.Fatalf("already-read bridge migration delivered duplicate body: %d %q %q", code, stdout, stderr)
		}
		if exists(rootBridge) || exists(localBridge) || !exists(filepath.Join(repo, ".claude", "AGENTS.md")) {
			t.Fatalf("owned bridges were not replaced")
		}
	})

	t.Run("linked local refresh while switching to AGENTS bridge", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", ".claude/CLAUDE.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 1\n")
		linked := addWorktree(t, repo, "feature")
		localCopy := filepath.Join(linked, "AGENTS.local.md")
		localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
		write(t, localCopy, "private body 1\n")
		write(t, localBridge, "@../AGENTS.local.md\n")
		recordGeneratedForTest(t, linked, localCopy)
		recordGeneratedForTest(t, linked, localBridge)
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 2\n")
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
		if code != 0 || stderr != "" || additionalContext(t, stdout) != "# shared rules\n\nprivate body 2" {
			t.Fatalf("linked migration did not deliver missing bodies: %d %q %q", code, stdout, stderr)
		}
		if exists(localBridge) || !exists(filepath.Join(linked, ".claude", "AGENTS.md")) || read(t, localCopy) != "private body 2\n" {
			t.Fatalf("linked generated files were not refreshed")
		}
	})

	t.Run("excluded legacy local bridge needs local body", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "CLAUDE.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
		legacy := filepath.Join(repo, "CLAUDE.local.md")
		write(t, legacy, "@AGENTS.local.md\n")
		recordGeneratedForTest(t, repo, legacy)
		settings, _ := json.Marshal(map[string]any{"claudeMdExcludes": []string{legacy}})
		write(t, filepath.Join(repo, ".claude", "settings.local.json"), string(settings))
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
		if code != 0 || stderr != "" || additionalContext(t, stdout) != "private body" {
			t.Fatalf("excluded legacy local did not deliver local body: %d %q %q", code, stdout, stderr)
		}
		if exists(legacy) || !exists(filepath.Join(repo, ".claude", "CLAUDE.md")) {
			t.Fatalf("legacy local was not replaced")
		}
	})
}

func TestClaudeSessionStartReportsAgentMDBlockers(t *testing.T) {
	for _, rel := range []string{"CLAUDE.md", "CLAUDE.local.md", filepath.Join(".claude", "CLAUDE.md")} {
		t.Run(rel, func(t *testing.T) {
			testHome(t)
			globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
			write(t, filepath.Join(repo, rel), "# old instruction file\n")
			code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
			context := additionalContext(t, stdout)
			want := "do not import"
			if rel == "CLAUDE.local.md" {
				want = "single local instruction source"
			}
			if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, rel) || !strings.Contains(context, want) {
				t.Fatalf("blocker was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestClaudeFileImportsMarkdownVisibleReferences(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	target := filepath.Join(repo, "AGENTS.md")
	write(t, target, "shared\n")
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"line only", "@AGENTS.md\n", true},
		{"inline", "See @AGENTS.md for shared rules.\n", true},
		{"inline punctuation", "See @AGENTS.md.\n", true},
		{"code span", "`@AGENTS.md`\n", false},
		{"fenced", "```\n@AGENTS.md\n```\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(repo, "CLAUDE.md")
			write(t, path, tc.body)
			got, err := claudeFileImports(path, target)
			if err != nil || got != tc.want {
				t.Fatalf("claudeFileImports = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestSessionStartDoesNotBypassNativeExclusions(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"claudeMdExcludes":["**/.claude/AGENTS.md"]}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, ".claude/AGENTS.md") || !strings.Contains(context, "will not load") {
		t.Fatalf("Claude native exclusion bypassed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	repoKey, _ := json.Marshal(repo)
	write(t, filepath.Join(home, ".codex", "config.toml"), "[projects."+string(repoKey)+"]\ntrust_level = \"untrusted\"\n")
	if err := os.Remove(filepath.Join(repo, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context = additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "untrusted") {
		t.Fatalf("Codex native trust bypassed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSessionStartReportsClaudeMDOnlyInstructionFiles(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.md"), "shared body\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"claude-md"}}}}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "claude-md disables") {
		t.Fatalf("claude-md-only setting was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSessionStartReportsClaudeMDOnlyInstructionFilesForLocalOnlyRepo(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	if err := os.Remove(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"claude-md"}}}}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "claude-md disables") {
		t.Fatalf("local-only claude-md setting was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSessionStartReportsManagedOnlyInstructionFiles(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"managed-only"}}}}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "managed-only") {
		t.Fatalf("managed-only setting was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if exists(filepath.Join(repo, ".claude", "AGENTS.md")) || exists(filepath.Join(repo, ".claude", "CLAUDE.md")) {
		t.Fatalf("managed-only mode created a Claude bridge")
	}

	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"projectInstructions":"none"}}}}`)
	code, stdout, stderr = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context = additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "managed-only") {
		t.Fatalf("legacy managed-only setting was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestClaudeAndAgentsModeAllowsClaudeFileWithoutSharedImport(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "# project instructions\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"claude-md-and-agents-md"}}}}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "do not import") || context != "private body" {
		t.Fatalf("claude-and mode was incorrectly blocked: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !exists(filepath.Join(repo, ".claude", "CLAUDE.md")) {
		t.Fatalf("claude-and mode did not create the local bridge")
	}
}

func TestSessionStartIgnoresProjectLocalClaudeMDOnlyInstructionFiles(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"claude-md"}}}}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || additionalContext(t, stdout) != "private body" || strings.Contains(context, "instructionFiles=claude-md") {
		t.Fatalf("project-local claude-md setting blocked native loading: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCodexStartupAfterLocalRemovalDeliversCurrentSharedRule(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	write(t, filepath.Join(repo, "AGENTS.md"), "shared after local removal\n")
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stderr != "" || additionalContext(t, stdout) != "shared after local removal" || exists(filepath.Join(repo, "AGENTS.override.md")) {
		t.Fatalf("startup after local removal: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSessionStartReportsSkipsWithoutBlocking(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, noticePrefix) || !strings.Contains(context, "not git-ignored") {
		t.Fatalf("skip notice: %d %q %q", code, stdout, stderr)
	}
}

func TestClaudeStartupRequiresCompletePreparedNativeFiles(t *testing.T) {
	t.Run("bridge without local copy", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, ".gitignore"), "AGENTS.override.md\n.claude/AGENTS.md\n")
		git(t, repo, "add", ".gitignore")
		git(t, repo, "commit", "-q", "-m", "drop local ignore")
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		linked := addWorktree(t, repo, "feature")
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
		context := additionalContext(t, stdout)
		if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "AGENTS.local.md") || !strings.Contains(context, "not git-ignored") {
			t.Fatalf("partial Claude preparation delivered: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
	t.Run("local copy without bridge", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		linked := addWorktree(t, repo, "feature")
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
		context := additionalContext(t, stdout)
		if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, ".claude/AGENTS.md") || !strings.Contains(context, "not git-ignored") {
			t.Fatalf("partial Claude preparation delivered: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
	t.Run("local copy with user bridge that does not import local", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		linked := addWorktree(t, repo, "feature")
		write(t, filepath.Join(linked, ".claude", "AGENTS.md"), "# user bridge\n")
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
		context := additionalContext(t, stdout)
		if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, ".claude/AGENTS.md") || !strings.Contains(context, "not quota-generated") {
			t.Fatalf("partial Claude preparation delivered: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
	t.Run("bridge with skipped stale local copy", func(t *testing.T) {
		testHome(t)
		globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
		linked := addWorktree(t, repo, "feature")
		write(t, filepath.Join(linked, "AGENTS.local.md"), "stale user body\n")
		code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
		context := additionalContext(t, stdout)
		if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "AGENTS.local.md") || !strings.Contains(context, "not quota-generated") {
			t.Fatalf("stale local copy delivered: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestWorktreeCreateAndRemoveHooks(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "task-1"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	if !strings.HasPrefix(target, resolvePath(os.Getenv("QUOTA_INSTRUCTIONS_CLAUDE_WORKTREE_DIR"))) || within(target, repo) {
		t.Fatalf("target %q", target)
	}
	if read(t, filepath.Join(target, "AGENTS.local.md")) != "private body\n" || read(t, filepath.Join(target, ".claude/AGENTS.md")) != "@../AGENTS.local.md\n" || read(t, filepath.Join(target, "AGENTS.override.md")) != "# shared rules\n\nprivate body\n" {
		t.Fatal("worktree not prepared")
	}
	if code, _, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "task-1"}); code == 0 {
		t.Fatalf("duplicate name accepted: %q", stderr)
	}
	write(t, filepath.Join(target, "notes.txt"), "user data\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) || !strings.Contains(stderr, "untracked files: notes.txt") {
		t.Fatalf("untracked user file removed: %q", stderr)
	}
	if !exists(filepath.Join(target, "AGENTS.override.md")) || !exists(filepath.Join(target, ".claude/AGENTS.md")) || !exists(filepath.Join(target, "AGENTS.local.md")) {
		t.Fatal("generated files deleted before the refusal")
	}
	if err := os.Remove(filepath.Join(target, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(target, "AGENTS.md"), "modified\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) {
		t.Fatalf("modified tracked file removed: %q", stderr)
	}
	git(t, target, "checkout", "-q", "--", "AGENTS.md")
	write(t, filepath.Join(target, "AGENTS.override.md"), "edited\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) {
		t.Fatalf("edited generated file removed: %q", stderr)
	}
	write(t, filepath.Join(target, "AGENTS.override.md"), "# shared rules\n\nprivate body\n")
	if err := os.Chmod(filepath.Join(target, "AGENTS.override.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) || !strings.Contains(stderr, "permissions changed") {
		t.Fatalf("generated file with changed permissions removed: %q", stderr)
	}
	if !exists(filepath.Join(target, ".claude/AGENTS.md")) || !exists(filepath.Join(target, "AGENTS.local.md")) {
		t.Fatal("other generated files deleted before the refusal")
	}
	if err := os.Chmod(filepath.Join(target, "AGENTS.override.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code != 0 || exists(target) {
		t.Fatalf("remove: %d %q", code, stderr)
	}
	if branches := git(t, repo, "branch", "--list", "quota-instructions/task-1"); strings.TrimSpace(branches) != "" {
		t.Fatalf("branch kept: %q", branches)
	}
	state := stateFileFor(t, repo)
	if strings.Contains(read(t, state), target) {
		t.Fatal("state still records the removed worktree")
	}
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": repo}); code == 0 {
		t.Fatalf("primary removal accepted: %q", stderr)
	}
}

func TestWorktreeCreateChecksStateBeforeCreating(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	state := stateFileFor(t, repo)
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(`{"generated":`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir, err := worktreeRepoDir(r)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(worktreeDir, "broken-state")
	code, _, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "broken-state"})
	if code == 0 || exists(target) || !strings.Contains(stderr, "parse instructions state") {
		t.Fatalf("create with corrupt state: code=%d target=%t stderr=%q", code, exists(target), stderr)
	}
	if branches := git(t, repo, "branch", "--list", "quota-instructions/broken-state"); strings.TrimSpace(branches) != "" {
		t.Fatalf("branch created before state check: %q", branches)
	}
}

func TestWorktreeRemoveAllowsEmptyIgnoredDirectoriesAfterOrphanRemoval(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n/config/\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-q", "-m", "ignore config")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "orphan-dir"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	copyPath := filepath.Join(target, "config", "run.sh")
	if !exists(copyPath) {
		t.Fatal("registered local file was not copied")
	}
	if _, err := RemoveLocalFiles(context.Background(), repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, target)
	if len(res.Removed) != 1 || res.Removed[0] != copyPath || exists(copyPath) || !exists(filepath.Join(target, "config")) {
		t.Fatalf("orphan removal result = %+v", res)
	}
	ignoredUserFile := filepath.Join(target, "config", "user.txt")
	write(t, ignoredUserFile, "user data\n")
	if code, _, stderr = hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) || !exists(ignoredUserFile) || !strings.Contains(stderr, "ignored user file") {
		t.Fatalf("ignored user file was not preserved: %d %q", code, stderr)
	}
	if err := os.Remove(ignoredUserFile); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr = hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code != 0 || exists(target) {
		t.Fatalf("empty ignored directory blocked removal: %d %q", code, stderr)
	}
	if branches := git(t, repo, "branch", "--list", "quota-instructions/orphan-dir"); strings.TrimSpace(branches) != "" {
		t.Fatalf("branch kept: %q", branches)
	}
	if strings.Contains(read(t, stateFileFor(t, repo)), target) {
		t.Fatal("state still records the removed worktree")
	}
}

func TestWorktreeRemoveKeepsUnmergedBranch(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "unmerged"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	write(t, filepath.Join(target, "feature.txt"), "kept commit\n")
	git(t, target, "add", "feature.txt")
	git(t, target, "commit", "-q", "-m", "feature")

	code, _, stderr = hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target})
	if code != 0 || exists(target) || !strings.Contains(stderr, "kept branch quota-instructions/unmerged") {
		t.Fatalf("remove: %d %q", code, stderr)
	}
	if branches := git(t, repo, "branch", "--list", "quota-instructions/unmerged"); !strings.Contains(branches, "quota-instructions/unmerged") {
		t.Fatalf("branch not kept: %q", branches)
	}
}

func TestWorktreeRemoveIgnoresSharedSourceErrors(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "broken-shared"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	shared := filepath.Join(target, "AGENTS.md")
	if err := os.Remove(shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", shared); err != nil {
		t.Fatal(err)
	}
	git(t, target, "add", "AGENTS.md")
	git(t, target, "commit", "-q", "-m", "break shared source")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code != 0 || exists(target) {
		t.Fatalf("shared source error blocked removal: %d %q", code, stderr)
	}
}

func TestCheckRepositoryReportsPreparationAndIgnoreState(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	ctx := context.Background()
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	status, err := CheckRepository(ctx, repo, "all", CheckOptions{})
	if err != nil || len(status.Problems) != 2 {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "not prepared") {
		t.Fatalf("status = %+v", status)
	}
	prepare(t, repo)
	status, _ = CheckRepository(ctx, repo, "all", CheckOptions{})
	if len(status.Problems) != 0 {
		t.Fatalf("status = %+v", status)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "changed\n")
	status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "stale") {
		t.Fatalf("status = %+v", status)
	}
	if contains(status.Generated, filepath.Join(repo, "AGENTS.override.md")) {
		t.Fatalf("stale generated file reported as ready: %+v", status)
	}
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "CLAUDE_CODE_DISABLE_CLAUDE_MDS") {
		t.Fatalf("status = %+v", status)
	}
}

func linkedWorktreeWithRegisteredFile(t *testing.T) (string, string) {
	t.Helper()
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n/config/\n")
	git(t, repo, "commit", "-q", "-am", "ignore config")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	linked := addWorktree(t, repo, "feature")
	if res := prepare(t, linked); len(res.Created) != 4 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v", res)
	}
	return repo, linked
}

func TestSymlinkedParentInLinkedWorktreeNeverReachesPrimaryFiles(t *testing.T) {
	repo, linked := linkedWorktreeWithRegisteredFile(t)
	if err := os.RemoveAll(filepath.Join(linked, "config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "config"), filepath.Join(linked, "config")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\necho changed\n")
	res := prepare(t, linked)
	if len(res.Skipped) != 1 || filepath.Base(res.Skipped[0].Path) != "run.sh" || !strings.Contains(res.Skipped[0].Reason, "not a regular directory") || len(res.Updated) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, filepath.Join(repo, "config", "run.sh")) != "#!/bin/sh\necho changed\n" {
		t.Fatal("primary file was rewritten through the symlink")
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, linked)
	if len(res.Skipped) != 1 || filepath.Base(res.Skipped[0].Path) != "run.sh" || len(res.Removed) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if !exists(filepath.Join(repo, "config", "run.sh")) {
		t.Fatal("primary file was removed through the symlink")
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "managed"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	managed := strings.TrimSpace(stdout)
	if read(t, filepath.Join(managed, "config", "run.sh")) != "#!/bin/sh\n" {
		t.Fatal("managed worktree not prepared")
	}
	if err := os.RemoveAll(filepath.Join(managed, "config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "config"), filepath.Join(managed, "config")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": managed}); code == 0 || !exists(managed) {
		t.Fatalf("removal through symlinked parent accepted: %d %q", code, stderr)
	}
	if !exists(filepath.Join(repo, "config", "run.sh")) {
		t.Fatal("primary file was removed through the symlink during worktree removal")
	}
	if err := os.Remove(filepath.Join(managed, "config")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(managed, "shadow", "run.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(managed, "shadow", "run.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(managed, "shadow"), filepath.Join(managed, "config")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": managed}); code == 0 || !exists(managed) || !strings.Contains(stderr, "untracked files") {
		t.Fatalf("removal through in-worktree symlinked parent accepted: %d %q", code, stderr)
	}
	if !exists(filepath.Join(managed, "shadow", "run.sh")) {
		t.Fatal("file behind the symlinked parent was removed")
	}
}

func TestSourceReadFailurePreservesCopyWithReason(t *testing.T) {
	repo, linked := linkedWorktreeWithRegisteredFile(t)
	source := filepath.Join(repo, "config", "run.sh")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "AGENTS.md"), source); err != nil {
		t.Fatal(err)
	}
	status, err := CheckRepository(context.Background(), linked, "all", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res := prepare(t, linked)
	if len(res.Removed) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Path != filepath.Join(linked, "config", "run.sh") {
		t.Fatalf("result = %+v", res)
	}
	if len(status.Problems) != 1 || status.Problems[0] != res.SkipReasons()[0] {
		t.Fatalf("status %+v does not match prepare %+v", status.Problems, res.SkipReasons())
	}
	if read(t, filepath.Join(linked, "config", "run.sh")) != "#!/bin/sh\n" {
		t.Fatal("copy changed after source read failure")
	}
	if !strings.Contains(read(t, stateFileFor(t, repo)), filepath.Join(linked, "config", "run.sh")) {
		t.Fatal("ownership record dropped after source read failure")
	}
}

func TestUnregisteredLocalFileCopyIsRemovedAsOrphan(t *testing.T) {
	repo, linked := linkedWorktreeWithRegisteredFile(t)
	copyPath := filepath.Join(linked, "config", "run.sh")
	ctx := context.Background()
	if files, err := RemoveLocalFiles(ctx, repo, []string{"config/run.sh"}); err != nil || len(files) != 0 {
		t.Fatalf("remove: %v %v", files, err)
	}
	status, err := CheckRepository(ctx, linked, "all", CheckOptions{})
	if err != nil || len(status.Problems) != 1 || status.Problems[0] != copyPath+" is a stale generated file; the next session start removes it" {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	res := prepare(t, linked)
	if len(res.Removed) != 1 || res.Removed[0] != copyPath || exists(copyPath) {
		t.Fatalf("orphan not removed: %+v", res)
	}
	if status, _ = CheckRepository(ctx, linked, "all", CheckOptions{}); len(status.Problems) != 0 {
		t.Fatalf("status after prepare = %+v", status)
	}
	if strings.Contains(read(t, stateFileFor(t, repo)), copyPath) {
		t.Fatal("state still records the orphan")
	}
	if !exists(filepath.Join(repo, "config", "run.sh")) {
		t.Fatal("primary source removed")
	}
	if _, err := AddLocalFiles(ctx, repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	prepare(t, linked)
	write(t, copyPath, "edited\n")
	if _, err := RemoveLocalFiles(ctx, repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	res = prepare(t, linked)
	if len(res.Removed) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Path != copyPath || read(t, copyPath) != "edited\n" {
		t.Fatalf("edited orphan not preserved: %+v", res)
	}
}

func TestDecodeStateRejectsNonObjectTopLevel(t *testing.T) {
	for _, body := range []string{"null", "[]", `"text"`, "1", "", "  "} {
		for _, strict := range []bool{true, false} {
			if _, err := decodeState([]byte(body), "state.json", strict); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
				t.Fatalf("decodeState(%q, strict=%t) = %v", body, strict, err)
			}
		}
	}
	if _, err := decodeState([]byte(`{"generated":{}}`), "state.json", true); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRepositoryUsesEffectiveCodexBudgetAndChecksClaudeCompatibility(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	ctx := context.Background()
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	status, err := CheckRepository(ctx, repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "# notes only\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) == 0 || len(status.Warnings) != 0 || !strings.Contains(strings.Join(status.Problems, "\n"), "do not import") {
		t.Fatalf("status = %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	prepare(t, repo)
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("status = %+v", status)
	}
	excludedRoot, _ := json.Marshal(map[string]any{"claudeMdExcludes": []string{filepath.Join(repo, "CLAUDE.md")}})
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), string(excludedRoot))
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) == 0 || !strings.Contains(strings.Join(status.Problems, "\n"), "CLAUDE.md") || !strings.Contains(strings.Join(status.Problems, "\n"), "will not load") {
		t.Fatalf("root CLAUDE.md exclusion was not reported: %+v", status)
	}
	if err := os.Remove(filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	if status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{}); len(status.Problems) != 0 {
		t.Fatalf("Codex status blocked by the Claude file: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.local.md"), "# old local rules\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "single local instruction source") {
		t.Fatalf("CLAUDE.local.md was not reported: %+v", status)
	}
	if err := os.Remove(filepath.Join(repo, "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"claudeMdExcludes":["**/.claude/CLAUDE.md"]}`)
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], ".claude/CLAUDE.md") || !strings.Contains(status.Problems[0], "will not load") {
		t.Fatalf("local bridge exclusion was not reported: %+v", status)
	}
	if err := os.Remove(filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	small := int64(10)
	status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{CodexProjectDocMaxBytes: &small})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "over Codex project_doc_max_bytes 10 from the effective Codex config") {
		t.Fatalf("status = %+v", status)
	}
	home := os.Getenv("HOME")
	repoKey, _ := json.Marshal(repo)
	write(t, filepath.Join(home, ".codex", "config.toml"), "project_doc_max_bytes = 0\n[projects."+string(repoKey)+"]\ntrust_level = \"trusted\"\n")
	status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{CodexProjectDocMaxBytes: &small})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "over Codex project_doc_max_bytes 10 from the effective Codex config") {
		t.Fatalf("effective budget was not used before file validation: %+v", status)
	}
	zero := int64(0)
	status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{CodexProjectDocMaxBytes: &zero})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "must be positive") {
		t.Fatalf("status = %+v", status)
	}
}

func TestCheckRepositoryReportsClaudeFilesAboveCheckout(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	parent := filepath.Join(home, "projects")
	repo := newRepoAt(t, filepath.Join(parent, "repo"))
	ctx := context.Background()
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)

	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# user memory\n")
	status, err := CheckRepository(ctx, repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 0 {
		t.Fatalf("user Claude file should not block AGENTS.md: %+v, err = %v", status, err)
	}

	write(t, filepath.Join(parent, "CLAUDE.md"), "# parent rules\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if !strings.Contains(strings.Join(status.Problems, "\n"), filepath.Join(parent, "CLAUDE.md")+" do not import") {
		t.Fatalf("parent Claude file did not block missing AGENTS import: %+v", status)
	}
	if status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{}); len(status.Problems) != 0 {
		t.Fatalf("Codex status blocked by the Claude parent file: %+v", status)
	}

	if err := os.Remove(filepath.Join(parent, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(parent, ".claude", "CLAUDE.md"), "# parent scoped rules\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if !strings.Contains(strings.Join(status.Problems, "\n"), filepath.Join(parent, ".claude", "CLAUDE.md")+" do not import") {
		t.Fatalf("parent .claude Claude file did not block missing AGENTS import: %+v", status)
	}
}

func TestClaudeStartDirectoryInstructionBlocksRootAgentFallback(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	subdir := filepath.Join(repo, "subdir")
	write(t, filepath.Join(subdir, "CLAUDE.md"), "# subdir rules\n")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": subdir, "source": "startup"})
	ctxText := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(ctxText, "private body") || !strings.Contains(ctxText, filepath.Join(subdir, "CLAUDE.md")+" do not import") {
		t.Fatalf("subdir Claude blocker was not reported: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	status, err := CheckRepository(context.Background(), subdir, "claude", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Problems, "\n"), filepath.Join(subdir, "CLAUDE.md")+" do not import") {
		t.Fatalf("status did not report subdir Claude blocker: %+v err=%v", status, err)
	}
}

func TestCheckRepositoryAllowsExcludedDuplicateClaudeFile(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	write(t, filepath.Join(repo, ".claude", "CLAUDE.md"), "@../AGENTS.md\n@../AGENTS.local.md\n")
	excludedRoot, _ := json.Marshal(map[string]any{"claudeMdExcludes": []string{filepath.Join(repo, "CLAUDE.md")}})
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), string(excludedRoot))
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("excluded duplicate root CLAUDE.md blocked valid .claude/CLAUDE.md: %+v err=%v", status, err)
	}
}

func TestCheckRepositoryChecksSharedAndOverrideCodexBudgets(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	ctx := context.Background()
	small := int64(20)
	trustRepo := func(repo string) {
		t.Helper()
		repoKey, _ := json.Marshal(repo)
		write(t, filepath.Join(home, ".codex", "config.toml"), "[projects."+string(repoKey)+"]\ntrust_level = \"trusted\"\n")
	}

	sharedOnly := newRepo(t)
	trustRepo(sharedOnly)
	write(t, filepath.Join(sharedOnly, "AGENTS.md"), "shared document over budget\n")
	status, err := CheckRepository(ctx, sharedOnly, "codex", CheckOptions{CodexProjectDocMaxBytes: &small})
	if err != nil || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "AGENTS.md") || len(status.Warnings) != 0 {
		t.Fatalf("shared-only status = %+v, err = %v", status, err)
	}

	withOverride := newRepo(t)
	trustRepo(withOverride)
	write(t, filepath.Join(withOverride, "AGENTS.md"), "shared document over budget\n")
	write(t, filepath.Join(withOverride, "AGENTS.local.md"), "local\n")
	prepare(t, withOverride)
	status, err = CheckRepository(ctx, withOverride, "codex", CheckOptions{CodexProjectDocMaxBytes: &small})
	if err != nil || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "AGENTS.override.md") {
		t.Fatalf("override status problems = %+v, err = %v", status, err)
	}
	if len(status.Warnings) != 1 || !strings.Contains(status.Warnings[0], "AGENTS.md") || !strings.Contains(status.Warnings[0], "inactive while AGENTS.override.md exists") {
		t.Fatalf("override status warnings = %+v", status)
	}
}

func TestPrepareBlocksOversizedCodexOverride(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), strings.Repeat("x", int(codexInstructionMaxBytes)))

	res := prepare(t, repo)
	override := filepath.Join(repo, "AGENTS.override.md")
	if exists(override) {
		t.Fatal("oversized AGENTS.override.md was created")
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Path != override || !strings.Contains(res.Skipped[0].Reason, "over quota 32767-byte Codex instruction limit") {
		t.Fatalf("oversized override skip not reported: %+v", res.Skipped)
	}
	if !contains(res.Created, filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatalf("Claude local bridge should still be created: %+v", res.Created)
	}
}

func TestCheckRepositoryReportsQuotaInstructionSizeLimit(t *testing.T) {
	home := testHome(t)
	ctx := context.Background()
	trustRepo := func(repo string) {
		t.Helper()
		repoKey, _ := json.Marshal(repo)
		write(t, filepath.Join(home, ".codex", "config.toml"), "[projects."+string(repoKey)+"]\ntrust_level = \"trusted\"\n")
	}

	repo := newRepo(t)
	trustRepo(repo)
	write(t, filepath.Join(repo, "AGENTS.md"), strings.Repeat("a", int(codexInstructionMaxBytes)))
	status, err := CheckRepository(ctx, repo, "codex", CheckOptions{})
	if err != nil || len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("max-size shared status = %+v, err = %v", status, err)
	}

	write(t, filepath.Join(repo, "AGENTS.md"), strings.Repeat("a", int(codexInstructionMaxBytes+1)))
	status, err = CheckRepository(ctx, repo, "codex", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Problems, "\n"), "AGENTS.md") || !strings.Contains(strings.Join(status.Problems, "\n"), "over quota 32767-byte Codex instruction limit") {
		t.Fatalf("oversized shared status = %+v, err = %v", status, err)
	}

	withOverride := newRepo(t)
	trustRepo(withOverride)
	write(t, filepath.Join(withOverride, "AGENTS.md"), strings.Repeat("s", int(codexInstructionMaxBytes+1)))
	write(t, filepath.Join(withOverride, "AGENTS.override.md"), strings.Repeat("o", int(codexInstructionMaxBytes+1)))
	status, err = CheckRepository(ctx, withOverride, "codex", CheckOptions{})
	allProblems, allWarnings := strings.Join(status.Problems, "\n"), strings.Join(status.Warnings, "\n")
	if err != nil || !strings.Contains(allProblems, "AGENTS.override.md") || !strings.Contains(allProblems, "over quota 32767-byte Codex instruction limit") {
		t.Fatalf("oversized override status = %+v, err = %v", status, err)
	}
	if !strings.Contains(allWarnings, "AGENTS.md") || !strings.Contains(allWarnings, "inactive while AGENTS.override.md exists") {
		t.Fatalf("inactive shared warning missing: %+v", status)
	}
}

func TestCheckRepositoryReportsSharedOnlyNativeProblems(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	ctx := context.Background()
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
	status, err := CheckRepository(ctx, repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "CLAUDE_CODE_DISABLE_CLAUDE_MDS") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
	write(t, filepath.Join(repo, "AGENTS.md"), "bad\x00rule\n")
	status, err = CheckRepository(ctx, repo, "all", CheckOptions{})
	if err != nil || len(status.Problems) == 0 || !strings.Contains(strings.Join(status.Problems, "\n"), "contains NUL") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
}

func TestCheckRepositoryReportsPreparedGeneratedFiles(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)

	status, err := CheckRepository(context.Background(), repo, "all", CheckOptions{})
	if err != nil || len(status.Problems) != 0 {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	if !contains(status.Generated, filepath.Join(repo, "AGENTS.override.md")) || !contains(status.Generated, filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatalf("generated files not reported: %+v", status.Generated)
	}
}

func TestCodexSessionStartReportsDocumentBudgetProblem(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), strings.Repeat("private body\n", 3))
	repoKey, _ := json.Marshal(repo)
	write(t, filepath.Join(home, ".codex", "config.toml"), "project_doc_max_bytes = 10\n[projects."+string(repoKey)+"]\ntrust_level = \"trusted\"\n")
	code, stdout, stderr := hook(t, "codex", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	ctxText := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.Contains(ctxText, "over Codex project_doc_max_bytes 10") {
		t.Fatalf("budget notice missing: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	effective := int64(10)
	write(t, filepath.Join(home, ".codex", "config.toml"), "project_doc_max_bytes = 1000\n[projects."+string(repoKey)+"]\ntrust_level = \"trusted\"\n")
	var out, errOut bytes.Buffer
	repoJSON, _ := json.Marshal(repo)
	code = RunPrepareHookWithOptions(context.Background(), "codex", "SessionStart", strings.NewReader(`{"cwd":`+string(repoJSON)+`,"source":"startup"}`), &out, &errOut, PrepareHookOptions{CodexProjectDocMaxBytes: &effective})
	ctxText = additionalContext(t, out.String())
	if code != 0 || errOut.String() != "" || !strings.Contains(ctxText, "over Codex project_doc_max_bytes 10 from the effective Codex config") {
		t.Fatalf("effective budget notice missing: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestWorktreeCreateReturnsPathWhenFilesAreSkipped(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "task-2"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	if !exists(filepath.Join(target, "AGENTS.override.md")) || exists(filepath.Join(target, ".claude/AGENTS.md")) {
		t.Fatalf("unexpected files in %s", target)
	}
	if !strings.Contains(stderr, ".claude/AGENTS.md") || !strings.Contains(stderr, `".claude/AGENTS.md"`) || !strings.Contains(stderr, "not git-ignored") {
		t.Fatalf("skip reason not reported: %q", stderr)
	}
}

func TestTrackedGeneratedFilesAreNeverRemoved(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	git(t, repo, "add", "-f", "AGENTS.override.md")
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, repo)
	if len(res.Skipped) != 1 || res.Skipped[0].Path != filepath.Join(repo, "AGENTS.override.md") || res.Skipped[0].Reason != "is tracked; it must stay local-only" {
		t.Fatalf("result = %+v", res)
	}
	if !exists(filepath.Join(repo, "AGENTS.override.md")) || exists(filepath.Join(repo, ".claude/AGENTS.md")) {
		t.Fatal("tracked generated file removed or untracked one kept")
	}
	status, _ := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if len(status.Problems) != 1 || status.Problems[0] != res.SkipReasons()[0] {
		t.Fatalf("status = %+v", status)
	}
}

func TestSharedSourceErrorBlocksOverrideRemoval(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	local := filepath.Join(repo, "AGENTS.local.md")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(repo, "AGENTS.md")
	if err := os.Remove(shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing"), shared); err != nil {
		t.Fatal(err)
	}
	res, err := PrepareCheckout(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(repo, "AGENTS.override.md")
	if contains(res.Removed, override) || !exists(override) || len(res.Skipped) == 0 || !strings.Contains(res.Skipped[0].Reason, "symlink") {
		t.Fatalf("result = %+v", res)
	}
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Problems, "\n"), "symlink") || contains(status.Generated, override) {
		t.Fatalf("status = %+v err=%v", status, err)
	}
}

func TestRegisteredSourceErrorBlocksCopyRemoval(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, source string)
		want string
	}{
		{
			name: "missing",
			edit: func(t *testing.T, source string) {
				t.Helper()
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
			},
			want: "no such file",
		},
		{
			name: "symlink",
			edit: func(t *testing.T, source string) {
				t.Helper()
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(filepath.Dir(source), "missing.sh"), source); err != nil {
					t.Fatal(err)
				}
			},
			want: "symlink",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, linked := linkedWorktreeWithRegisteredFile(t)
			source := filepath.Join(repo, "config", "run.sh")
			copyPath := filepath.Join(linked, "config", "run.sh")
			if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
				t.Fatal(err)
			}
			tc.edit(t, source)
			res := prepare(t, linked)
			if contains(res.Removed, copyPath) || !exists(copyPath) || len(res.Skipped) == 0 || !strings.Contains(res.Skipped[0].Reason, "local file source") || !strings.Contains(res.Skipped[0].Reason, tc.want) {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestTrackedOrphanCopyIsPreserved(t *testing.T) {
	repo, linked := linkedWorktreeWithRegisteredFile(t)
	copyPath := filepath.Join(linked, "config", "run.sh")
	git(t, linked, "add", "-f", "config/run.sh")
	if _, err := RemoveLocalFiles(context.Background(), repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, linked)
	if len(res.Removed) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Path != copyPath || res.Skipped[0].Reason != "is tracked; it must stay local-only" || !exists(copyPath) {
		t.Fatalf("result = %+v", res)
	}
}

func TestOrphanCopyWithSymlinkParentIsReported(t *testing.T) {
	repo, linked := linkedWorktreeWithRegisteredFile(t)
	copyPath := filepath.Join(linked, "config", "run.sh")
	if _, err := RemoveLocalFiles(context.Background(), repo, []string{"config/run.sh"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(copyPath)); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "config")
	write(t, filepath.Join(external, "run.sh"), "#!/bin/sh\n")
	if err := os.Symlink(external, filepath.Dir(copyPath)); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, linked)
	if len(res.Skipped) != 1 || res.Skipped[0].Path != copyPath || !strings.Contains(res.Skipped[0].Reason, "not a regular directory") {
		t.Fatalf("result = %+v", res)
	}
	status, err := CheckRepository(context.Background(), linked, "all", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Problems, "\n"), "not a regular directory") || contains(status.Generated, copyPath) {
		t.Fatalf("status = %+v err=%v", status, err)
	}
}

func TestLegacyLocalFilesAreCarriedOver(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\n/config/\n")
	git(t, repo, "commit", "-q", "-am", "ignore config")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "config", "run.sh"), "#!/bin/sh\n")
	linked := addWorktree(t, repo, "feature")
	copyPath := filepath.Join(linked, "config", "run.sh")
	write(t, copyPath, "#!/bin/sh\n")
	if err := os.Chmod(copyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(repo, ".git", "quota-instructions.json")
	legacyBody, _ := json.Marshal(map[string]any{"local_files": []string{"config/run.sh"}, "generated": map[string]string{copyPath: digest([]byte("#!/bin/sh\n"))}, "generated_modes": map[string]uint32{copyPath: 0o600}})
	if err := os.WriteFile(legacy, legacyBody, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if files, err := ListLocalFiles(ctx, repo); err != nil || strings.Join(files, ",") != "config/run.sh" {
		t.Fatalf("legacy registrations = %v, err = %v", files, err)
	}
	res := prepare(t, linked)
	if len(res.Removed) != 0 || len(res.Skipped) != 0 || exists(copyPath) == false || read(t, copyPath) != "#!/bin/sh\n" {
		t.Fatalf("legacy copy treated as orphan: %+v", res)
	}
	if read(t, legacy) != string(legacyBody) {
		t.Fatal("legacy state was rewritten")
	}
	if !strings.Contains(read(t, stateFileFor(t, repo)), `"config/run.sh"`) {
		t.Fatal("registrations were not carried into the new state")
	}
}

func TestLegacySharedCopyIsPreservedAsActiveNativeRule(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.md", "AGENTS.local.md", "AGENTS.override.md", ".claude/AGENTS.md")
	repo := resolvePath(filepath.Join(t.TempDir(), "repo"))
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\nAGENTS.local.md\nAGENTS.override.md\n.claude/AGENTS.md\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-q", "-m", "init")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "feature")
	sharedCopy := filepath.Join(linked, "AGENTS.md")
	write(t, sharedCopy, "legacy shared\n")
	recordGeneratedForTest(t, repo, sharedCopy)
	res := prepare(t, linked)
	if contains(res.Removed, sharedCopy) || !exists(sharedCopy) {
		t.Fatalf("legacy shared copy was removed: %+v", res)
	}
	if got := read(t, filepath.Join(linked, "AGENTS.override.md")); got != "legacy shared\n\nprivate body\n" {
		t.Fatalf("override = %q", got)
	}
	status, err := CheckRepository(context.Background(), linked, "all", CheckOptions{})
	if err != nil || !contains(status.Generated, sharedCopy) {
		t.Fatalf("legacy generated AGENTS.md not reported: %+v err=%v", status, err)
	}
}

func TestLegacyClaudeBridgeIsRemovedWhenOwned(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	bridge := filepath.Join(repo, "CLAUDE.md")
	write(t, bridge, "@AGENTS.md\n")
	recordGeneratedForTest(t, repo, bridge)
	res := prepare(t, repo)
	if len(res.Removed) != 1 || res.Removed[0] != bridge || exists(bridge) {
		t.Fatalf("legacy Claude bridge was not removed: %+v", res)
	}
}

func TestLegacyClaudeBridgeIsPreservedWhenReplacementBridgeIsSkipped(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	legacy := filepath.Join(repo, "CLAUDE.local.md")
	write(t, legacy, "@AGENTS.local.md\n")
	recordGeneratedForTest(t, repo, legacy)
	res := prepare(t, repo)
	if contains(res.Removed, legacy) || !exists(legacy) {
		t.Fatalf("legacy bridge was removed before replacement was ready: %+v", res)
	}
	var replacementSkip, preservedSkip bool
	for _, skip := range res.Skipped {
		if skip.Path == filepath.Join(repo, ".claude", "AGENTS.md") && strings.Contains(skip.Reason, "not git-ignored") {
			replacementSkip = true
		}
		if skip.Path == legacy && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			preservedSkip = true
		}
	}
	if !replacementSkip || !preservedSkip {
		t.Fatalf("missing replacement/preserve skips: %+v", res.Skipped)
	}
}

func TestLegacyClaudeBridgeIsPreservedWhenReplacementBridgeIsExcluded(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	replacement := filepath.Join(repo, ".claude", "CLAUDE.md")
	settings, _ := json.Marshal(map[string]any{"claudeMdExcludes": []string{replacement}})
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), string(settings))
	legacy := filepath.Join(repo, "CLAUDE.local.md")
	write(t, legacy, "@AGENTS.local.md\n")
	recordGeneratedForTest(t, repo, legacy)
	res := prepare(t, repo)
	if contains(res.Created, replacement) || exists(replacement) || contains(res.Removed, legacy) || !exists(legacy) {
		t.Fatalf("legacy bridge was not preserved with excluded replacement: %+v", res)
	}
	var preservedSkip bool
	for _, skip := range res.Skipped {
		if skip.Path == legacy && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			preservedSkip = true
		}
	}
	if !preservedSkip {
		t.Fatalf("missing preserve skip: %+v", res.Skipped)
	}
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(status.Problems, "\n")
	if !strings.Contains(problems, "replacement Claude bridge was not prepared; preserved") || strings.Contains(problems, "the next session start removes it") {
		t.Fatalf("status did not match prepare preservation: %+v", status.Problems)
	}
}

func TestLegacyClaudeBridgeIsPreservedWhenAgentsNativeLoadingIsDisabled(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "CLAUDE.md", "CLAUDE.local.md")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"claude-md"}}}}`)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	rootBridge := filepath.Join(repo, "CLAUDE.md")
	localBridge := filepath.Join(repo, "CLAUDE.local.md")
	write(t, rootBridge, "@AGENTS.md\n")
	write(t, localBridge, "@AGENTS.local.md\n")
	recordGeneratedForTest(t, repo, rootBridge)
	recordGeneratedForTest(t, repo, localBridge)
	res := prepare(t, repo)
	if contains(res.Created, filepath.Join(repo, ".claude", "AGENTS.md")) || contains(res.Removed, rootBridge) || contains(res.Removed, localBridge) || !exists(rootBridge) || !exists(localBridge) {
		t.Fatalf("legacy bridges were not preserved while AGENTS loading is disabled: %+v", res)
	}
}

func TestManagedClaudeCopyStillBlocksAgentFallback(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	linked := addWorktree(t, repo, "feature")
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, subdir)
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if !contains(res.Created, localBridge) || !contains(res.Created, managedCopy) || exists(agentsBridge) || !exists(localBridge) {
		t.Fatalf("local bridge was not selected while managed CLAUDE copy was created: %+v", res)
	}
	res = prepare(t, subdir)
	if contains(res.Removed, localBridge) || contains(res.Created, agentsBridge) || !exists(localBridge) || exists(agentsBridge) {
		t.Fatalf("managed CLAUDE copy was ignored during fallback planning: %+v", res)
	}

	repoWithMissingSource := newRepo(t)
	write(t, filepath.Join(repoWithMissingSource, "AGENTS.local.md"), "private body\n")
	linked = addWorktree(t, repoWithMissingSource, "missing-source")
	prepare(t, linked)
	sourceCopy := filepath.Join(repoWithMissingSource, "subdir", "CLAUDE.md")
	write(t, sourceCopy, "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repoWithMissingSource, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourceCopy); err != nil {
		t.Fatal(err)
	}
	subdir = filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsBridge = filepath.Join(linked, ".claude", "AGENTS.md")
	localBridge = filepath.Join(linked, ".claude", "CLAUDE.md")
	res = prepare(t, subdir)
	if contains(res.Removed, agentsBridge) || contains(res.Created, localBridge) || !exists(agentsBridge) || exists(localBridge) {
		t.Fatalf("missing managed CLAUDE source changed the active bridge: %+v", res)
	}

	linked = addWorktree(t, repo, "feature-with-bridge")
	subdir = filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	localBridge = filepath.Join(linked, ".claude", "CLAUDE.md")
	agentsBridge = filepath.Join(linked, ".claude", "AGENTS.md")
	write(t, localBridge, "@../AGENTS.local.md\n")
	recordGeneratedForTest(t, linked, localBridge)
	res = prepare(t, subdir)
	if contains(res.Removed, localBridge) || contains(res.Created, agentsBridge) || !exists(localBridge) || exists(agentsBridge) || !exists(filepath.Join(linked, "subdir", "CLAUDE.md")) {
		t.Fatalf("existing local bridge was replaced before managed CLAUDE copy was considered: %+v", res)
	}
}

func TestUnregisteredManagedClaudeCopyNoLongerBlocksAgentFallback(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	linked := addWorktree(t, repo, "unregister-claude-copy")
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	prepare(t, subdir)
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if !exists(localBridge) || !exists(managedCopy) || exists(agentsBridge) {
		t.Fatalf("initial managed copy setup did not select local bridge")
	}
	if _, err := RemoveLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, subdir)
	if !contains(res.Created, agentsBridge) || !contains(res.Removed, localBridge) || !contains(res.Removed, managedCopy) {
		t.Fatalf("unregistered managed copy still affected bridge plan: %+v", res)
	}
	if exists(localBridge) || exists(managedCopy) || !exists(agentsBridge) {
		t.Fatalf("unregistered managed copy left the wrong Claude bridge")
	}
}

func TestUnregisteredManagedClaudeCopyPreservedWhenReplacementBridgeSkipped(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/CLAUDE.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	linked := addWorktree(t, repo, "unregister-claude-copy-preserve")
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	prepare(t, subdir)
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if !exists(localBridge) || !exists(managedCopy) || exists(agentsBridge) {
		t.Fatalf("initial managed copy setup did not select local bridge")
	}
	if _, err := RemoveLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, subdir)
	if contains(res.Created, agentsBridge) || contains(res.Removed, localBridge) || contains(res.Removed, managedCopy) || !exists(localBridge) || !exists(managedCopy) || exists(agentsBridge) {
		t.Fatalf("failed replacement bridge removed part of the existing Claude connection: %+v", res)
	}
	var copyPreserved bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			copyPreserved = true
		}
	}
	if !copyPreserved {
		t.Fatalf("managed copy preserve skip missing: %+v", res.Skipped)
	}
}

func TestManagedClaudeCopyRequiresTargetPreparation(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	linked := addWorktree(t, repo, "target-not-ignored")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.local.md\nsubdir/CLAUDE.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, subdir)
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if !contains(res.Created, agentsBridge) || contains(res.Created, localBridge) || exists(localBridge) || exists(managedCopy) {
		t.Fatalf("unprepared managed CLAUDE copy selected local-only bridge: %+v", res)
	}
	var copySkipped bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy && strings.Contains(skip.Reason, "not git-ignored") {
			copySkipped = true
		}
	}
	if !copySkipped {
		t.Fatalf("missing managed copy skip: %+v", res.Skipped)
	}
}

func TestManagedClaudeCopyWriteFailurePreservesAgentsBridge(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "copy-write-fails")
	prepare(t, linked)
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(subdir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(subdir, 0o700) })
	res := prepare(t, subdir)
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if contains(res.Removed, agentsBridge) || contains(res.Created, localBridge) || !exists(agentsBridge) || exists(localBridge) || exists(managedCopy) {
		t.Fatalf("copy write failure changed active bridge: %+v", res)
	}
	var copySkipped, bridgeSkipped, removalSkipped bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy {
			copySkipped = true
		}
		if skip.Path == localBridge && strings.Contains(skip.Reason, "required Claude instruction file was not prepared") {
			bridgeSkipped = true
		}
		if skip.Path == agentsBridge && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			removalSkipped = true
		}
	}
	if !copySkipped || !bridgeSkipped || !removalSkipped {
		t.Fatalf("missing copy/bridge preservation skips: %+v", res.Skipped)
	}
}

func TestManagedClaudeCopyRollsBackWhenBridgeWriteFails(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "bridge-write-fails")
	prepare(t, linked)
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(linked, ".claude")
	if err := os.Chmod(claudeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claudeDir, 0o700) })
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := prepare(t, subdir)
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if exists(managedCopy) || exists(localBridge) || !exists(agentsBridge) || contains(res.Removed, agentsBridge) {
		t.Fatalf("bridge write failure left a CLAUDE blocker: %+v", res)
	}
	var rolledBack bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy && strings.Contains(skip.Reason, "rolled back") {
			rolledBack = true
		}
	}
	if !rolledBack {
		t.Fatalf("missing managed copy rollback skip: %+v", res.Skipped)
	}
}

func TestManagedClaudeCopyRollsBackWhenLocalCopyMissing(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "local-copy-missing")
	prepare(t, linked)
	localCopy := filepath.Join(linked, "AGENTS.local.md")
	if err := os.Remove(localCopy); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", ".claude/CLAUDE.md", "subdir/CLAUDE.md")
	write(t, filepath.Join(linked, ".gitignore"), "")
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := CheckRepository(context.Background(), subdir, "claude", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	problems := strings.Join(status.Problems, "\n")
	if strings.Contains(problems, localBridge+" is not prepared yet; the next session start creates it") || strings.Contains(problems, managedCopy+" is not prepared yet; the next session start creates it") {
		t.Fatalf("status predicted bridge creation without local copy: %+v", status.Problems)
	}
	res := prepare(t, subdir)
	if exists(localCopy) || exists(localBridge) || exists(managedCopy) {
		t.Fatalf("local-missing prepare left invalid Claude path: %+v", res)
	}
	var preserved bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			preserved = true
		}
	}
	if !preserved {
		t.Fatalf("missing managed copy preservation after local skip: %+v", res.Skipped)
	}
}

func TestExistingClaudeFileDoesNotDependOnPlannedCopy(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/CLAUDE.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	git(t, repo, "add", "CLAUDE.md")
	git(t, repo, "commit", "-q", "-m", "add claude bridge")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	linked := addWorktree(t, repo, "existing-claude")
	subdir := filepath.Join(linked, "subdir")
	if err := os.MkdirAll(subdir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(subdir, 0o700) })
	res := prepare(t, subdir)
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if !contains(res.Created, localBridge) || !exists(localBridge) || exists(managedCopy) {
		t.Fatalf("existing CLAUDE file did not get local bridge independently: %+v", res)
	}
}

func TestPartialManagedClaudeCopySuccessCreatesLocalBridge(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md", "subdir/deep/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "partial-copy")
	prepare(t, linked)
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	write(t, filepath.Join(repo, "subdir", "deep", "CLAUDE.md"), "@../../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md", "subdir/deep/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(linked, "subdir")
	deep := filepath.Join(subdir, "deep")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(deep, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(deep, 0o700) })
	res := prepare(t, deep)
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	if !contains(res.Created, filepath.Join(linked, "subdir", "CLAUDE.md")) || !contains(res.Created, localBridge) || contains(res.Removed, localBridge) || !exists(localBridge) {
		t.Fatalf("successful CLAUDE copy did not activate local bridge: %+v", res)
	}
	if !contains(res.Removed, agentsBridge) || exists(agentsBridge) || exists(filepath.Join(linked, "subdir", "deep", "CLAUDE.md")) {
		t.Fatalf("old bridge or failed copy state is wrong: %+v", res)
	}
}

func TestManagedClaudeCopyWaitsForClaudeBridgeTarget(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "bridge-target-skipped")
	prepare(t, linked)
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", "subdir/CLAUDE.md")
	subdir := filepath.Join(linked, "subdir")
	localBridge := filepath.Join(linked, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := CheckRepository(context.Background(), subdir, "claude", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(status.Problems, "\n")
	if strings.Contains(problems, localBridge+" is not prepared yet; the next session start creates it") {
		t.Fatalf("status predicted skipped bridge creation: %+v", status.Problems)
	}
	res := prepare(t, subdir)
	agentsBridge := filepath.Join(linked, ".claude", "AGENTS.md")
	managedCopy := filepath.Join(linked, "subdir", "CLAUDE.md")
	if exists(managedCopy) || exists(localBridge) || !exists(agentsBridge) || contains(res.Removed, agentsBridge) {
		t.Fatalf("managed CLAUDE copy was created without a usable local bridge: %+v", res)
	}
	var copySkipped bool
	for _, skip := range res.Skipped {
		if skip.Path == managedCopy && strings.Contains(skip.Reason, "replacement Claude bridge was not prepared") {
			copySkipped = true
		}
	}
	if !copySkipped {
		t.Fatalf("missing managed copy preservation skip: %+v", res.Skipped)
	}
}

func TestStatusSkipsBridgeWhenExcludedBridgeSkipsDependentCopy(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/AGENTS.md", "subdir/CLAUDE.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "subdir", "CLAUDE.md"), "@../AGENTS.md\n")
	if _, err := AddLocalFiles(context.Background(), repo, []string{"subdir/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(repo, ".claude", "CLAUDE.md")
	settings, _ := json.Marshal(map[string]any{"claudeMdExcludes": []string{replacement}})
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), string(settings))
	status, err := CheckRepository(context.Background(), filepath.Join(repo, "subdir"), "claude", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(status.Problems, "\n")
	if strings.Contains(problems, replacement+" is not prepared yet; the next session start creates it") || !strings.Contains(problems, "required Claude instruction file was not prepared; preserved") {
		t.Fatalf("status did not skip excluded dependent bridge: %+v", status.Problems)
	}
	res := prepare(t, filepath.Join(repo, "subdir"))
	if exists(replacement) {
		t.Fatalf("prepare created excluded bridge or dependent copy: %+v", res)
	}
}

func TestSkippedReplacementBridgeDoesNotRemoveLegacyBridge(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", ".claude/CLAUDE.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	git(t, repo, "add", "CLAUDE.md")
	git(t, repo, "commit", "-q", "-m", "add claude bridge")
	replacement := filepath.Join(repo, ".claude", "CLAUDE.md")
	legacy := filepath.Join(repo, "CLAUDE.local.md")
	write(t, replacement, "@../AGENTS.local.md\n")
	write(t, legacy, "@AGENTS.local.md\n")
	recordGeneratedForTest(t, repo, replacement)
	recordGeneratedForTest(t, repo, legacy)
	globalIgnore(t, "AGENTS.override.md", "AGENTS.local.md", "CLAUDE.local.md")
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(status.Problems, "\n")
	if !strings.Contains(problems, "replacement Claude bridge was not prepared; preserved") || strings.Contains(problems, legacy+" is a stale generated file; the next session start removes it") {
		t.Fatalf("status did not preserve skipped replacement: %+v", status.Problems)
	}
	res := prepare(t, repo)
	if contains(res.Removed, legacy) || !exists(legacy) {
		t.Fatalf("legacy bridge was removed after skipped replacement: %+v", res)
	}
}

func TestTrackedLegacyClaudeBridgeUsesClaudeLocalBridge(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	bridge := filepath.Join(repo, "CLAUDE.md")
	write(t, bridge, "@AGENTS.md\n")
	recordGeneratedForTest(t, repo, bridge)
	git(t, repo, "add", "-f", "CLAUDE.md")
	git(t, repo, "commit", "-q", "-m", "track claude bridge")
	res := prepare(t, repo)
	if contains(res.Removed, bridge) || !exists(bridge) {
		t.Fatalf("tracked legacy Claude bridge was removed: %+v", res)
	}
	if !exists(filepath.Join(repo, ".claude", "CLAUDE.md")) || exists(filepath.Join(repo, ".claude", "AGENTS.md")) {
		t.Fatalf("wrong Claude local bridge selected: %+v", res)
	}
}

func TestClaudeLinkedWorktreeDeliversOnLocalCopyChange(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "feature")
	code, stdout, _ := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body" || read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body\n" || read(t, filepath.Join(linked, ".claude", "AGENTS.md")) != "@../AGENTS.local.md\n" {
		t.Fatalf("startup after copy creation: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 2\n")
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "resume"})
	if code != 0 || stdout != "" || read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body 2\n" {
		t.Fatalf("resume delivered or did not refresh: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 3\n")
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body 3" || read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body 3\n" {
		t.Fatalf("startup after copy refresh: %d %q", code, stdout)
	}
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || stdout != "" {
		t.Fatalf("unchanged startup delivered: %d %q", code, stdout)
	}
}

func TestSourceErrorBlocksStatusAndPrepareAlike(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md")
	repo := newRepo(t)
	local := filepath.Join(repo, "AGENTS.local.md")
	write(t, local, "private body\n")
	prepare(t, repo)
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "AGENTS.md"), local); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status, err := CheckRepository(ctx, repo, "all", CheckOptions{})
	if err != nil || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "symlink") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	_, err = PrepareCheckout(ctx, repo)
	if err == nil || err.Error() != status.Problems[0] {
		t.Fatalf("prepare error %v differs from status problem %q", err, status.Problems[0])
	}
	if !exists(filepath.Join(repo, ".claude/AGENTS.md")) || !exists(filepath.Join(repo, "AGENTS.override.md")) {
		t.Fatal("generated files removed despite the source error")
	}
}

func recordGeneratedForTest(t *testing.T, repo, path string) {
	t.Helper()
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	err = withStateLock(r, func() error {
		state, err := readState(r)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := recordGeneratedFile(&state, path, data); err != nil {
			return err
		}
		return writeState(r, state)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeRemoveGuardsLegacyBridgeRecord(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", ".claude/AGENTS.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "legacy"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	bridge := filepath.Join(target, "CLAUDE.md")
	write(t, bridge, "@AGENTS.md\n")
	git(t, target, "add", "CLAUDE.md")
	git(t, target, "commit", "-q", "-m", "track legacy bridge")
	git(t, target, "rm", "-q", "--cached", "CLAUDE.md")
	git(t, target, "commit", "-q", "-m", "untrack bridge")
	write(t, filepath.Join(target, ".gitignore"), "AGENTS.local.md\nCLAUDE.md\n")
	git(t, target, "commit", "-q", "-am", "ignore bridge")
	recordGeneratedForTest(t, repo, bridge)
	write(t, bridge, "@AGENTS.md\n# edited\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) || !strings.Contains(stderr, "has user edits") {
		t.Fatalf("edited legacy bridge removed: %d %q", code, stderr)
	}
	if read(t, bridge) != "@AGENTS.md\n# edited\n" || !exists(filepath.Join(target, "AGENTS.override.md")) {
		t.Fatal("files changed by the refused removal")
	}
	write(t, bridge, "@AGENTS.md\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code != 0 || exists(target) {
		t.Fatalf("unchanged legacy bridge blocked removal: %d %q", code, stderr)
	}
}
