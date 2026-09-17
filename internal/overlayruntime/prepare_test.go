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
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "AGENTS.md"), "# shared rules\n")
	write(t, filepath.Join(dir, "CLAUDE.md"), "@AGENTS.md\n")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	before := gitTree(t, repo)
	res := prepare(t, repo)
	bridge, override := filepath.Join(repo, "CLAUDE.local.md"), filepath.Join(repo, "AGENTS.override.md")
	if !res.LocalPresent || res.LocalBody != "private body" || len(res.Skipped) != 0 || !res.Changed(bridge) || !res.Changed(override) || len(res.Created) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, bridge) != "@AGENTS.local.md\n" || read(t, override) != "# shared rules\n\nprivate body\n" {
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
		if exists(skip.Path) || !strings.Contains(skip.Reason, "not git-ignored") || !strings.Contains(skip.Reason, `"`+filepath.Base(skip.Path)+`"`) || !strings.Contains(skip.Reason, "global git ignore") {
			t.Fatalf("skip = %+v", skip)
		}
	}
	globalIgnore(t, "AGENTS.override.md")
	res = prepare(t, repo)
	if len(res.Created) != 1 || len(res.Skipped) != 1 || filepath.Base(res.Created[0]) != "AGENTS.override.md" || filepath.Base(res.Skipped[0].Path) != "CLAUDE.local.md" {
		t.Fatalf("partial result = %+v", res)
	}
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	res = prepare(t, repo)
	if len(res.Created) != 1 || len(res.Skipped) != 0 || filepath.Base(res.Created[0]) != "CLAUDE.local.md" {
		t.Fatalf("completed result = %+v", res)
	}
	globalIgnore(t, "CLAUDE.local.md")
	res = prepare(t, repo)
	if len(res.Created) != 0 || len(res.Updated) != 0 || len(res.Skipped) != 1 || filepath.Base(res.Skipped[0].Path) != "AGENTS.override.md" || !strings.Contains(res.Skipped[0].Reason, "not git-ignored") || !exists(res.Skipped[0].Path) {
		t.Fatalf("ignore drift result = %+v", res)
	}
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || len(status.Problems) == 0 || !strings.Contains(strings.Join(status.Problems, "\n"), res.Skipped[0].Path+": "+res.Skipped[0].Reason) {
		t.Fatalf("status after ignore drift = %+v, %v", status, err)
	}
}

func TestPreparePreservesUserFilesAndEditedGeneratedFiles(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	bridge, override := filepath.Join(repo, "CLAUDE.local.md"), filepath.Join(repo, "AGENTS.override.md")
	write(t, override, "# shared rules\n\nprivate body\n")
	write(t, bridge, "@AGENTS.local.md\r\n")
	res := prepare(t, repo)
	if len(res.Created) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Path != override || !strings.Contains(res.Skipped[0].Reason, "not quota-generated") {
		t.Fatalf("identical user override adopted: %+v", res)
	}
	if read(t, override) != "# shared rules\n\nprivate body\n" || read(t, bridge) != "@AGENTS.local.md\r\n" {
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
			globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
			r.applyAction(repo, remove, &state, &res)
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	if exists(filepath.Join(repo, "CLAUDE.local.md")) || exists(filepath.Join(repo, "AGENTS.override.md")) {
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	if read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body\n" || read(t, filepath.Join(linked, "AGENTS.override.md")) != "# linked rules\n\nprivate body\n" || read(t, filepath.Join(linked, "CLAUDE.local.md")) != "@AGENTS.local.md\n" || read(t, filepath.Join(linked, "config", "run.sh")) != "#!/bin/sh\n" {
		t.Fatal("copies differ from sources")
	}
	assertMode(t, filepath.Join(linked, "AGENTS.local.md"), 0o600)
	assertMode(t, filepath.Join(linked, "config", "run.sh"), 0o700)
	if exists(filepath.Join(repo, "CLAUDE.local.md")) {
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
	for _, bad := range []string{".gitignore", "config/.gitignore", "AGENTS.override.md", "CLAUDE.local.md", "claude.local.md", "/etc/passwd", "../outside", "config/../config/app.json", "config/missing.json", "tracked.txt", "AGENTS.md"} {
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "resume"})
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume delivered: %d %q %q", code, stdout, stderr)
	}
	if !exists(filepath.Join(repo, "CLAUDE.local.md")) {
		t.Fatal("resume did not prepare the checkout")
	}
	if err := os.Remove(filepath.Join(repo, "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body" {
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.Contains(context, "CLAUDE_CODE_DISABLE_CLAUDE_MDS") || strings.Contains(context, "private body") {
		t.Fatalf("disabled native channel leaked body: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSessionStartDoesNotBypassNativeExclusions(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"claudeMdExcludes":["**/CLAUDE.local.md"]}`)
	code, stdout, stderr := hook(t, "claude", "SessionStart", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "CLAUDE.local.md") || !strings.Contains(context, "will not load") {
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

func TestCodexStartupAfterLocalRemovalDeliversCurrentSharedRule(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
		globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
		repo := newRepo(t)
		write(t, filepath.Join(repo, ".gitignore"), "AGENTS.override.md\nCLAUDE.local.md\n")
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
		if code != 0 || stderr != "" || strings.Contains(context, "private body") || !strings.Contains(context, "CLAUDE.local.md") || !strings.Contains(context, "not git-ignored") {
			t.Fatalf("partial Claude preparation delivered: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestWorktreeCreateAndRemoveHooks(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	if read(t, filepath.Join(target, "AGENTS.local.md")) != "private body\n" || read(t, filepath.Join(target, "CLAUDE.local.md")) != "@AGENTS.local.md\n" || read(t, filepath.Join(target, "AGENTS.override.md")) != "# shared rules\n\nprivate body\n" {
		t.Fatal("worktree not prepared")
	}
	if code, _, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "task-1"}); code == 0 {
		t.Fatalf("duplicate name accepted: %q", stderr)
	}
	write(t, filepath.Join(target, "notes.txt"), "user data\n")
	if code, _, stderr := hook(t, "claude", "WorktreeRemove", map[string]any{"worktree_path": target}); code == 0 || !exists(target) || !strings.Contains(stderr, "untracked files: notes.txt") {
		t.Fatalf("untracked user file removed: %q", stderr)
	}
	if !exists(filepath.Join(target, "AGENTS.override.md")) || !exists(filepath.Join(target, "CLAUDE.local.md")) || !exists(filepath.Join(target, "AGENTS.local.md")) {
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
	if !exists(filepath.Join(target, "CLAUDE.local.md")) || !exists(filepath.Join(target, "AGENTS.local.md")) {
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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

func TestImportsSharedUsesMarkdownVisibleText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "plain visible import", body: "See @AGENTS.md for shared rules.\n", want: true},
		{name: "backup path is not import", body: "@AGENTS.md.backup\n", want: false},
		{name: "fenced code", body: "```text\n@AGENTS.md\n```\n", want: false},
		{name: "nested fence text", body: "````text\n```\n@AGENTS.md\n````\n", want: false},
		{name: "blockquote fence", body: "> ~~~text\n> @AGENTS.md\n> ~~~\n", want: false},
		{name: "list item fence", body: "- ~~~text\n  @AGENTS.md\n  ~~~\n", want: false},
		{name: "list item blockquote fence", body: "- > ~~~\n  > @AGENTS.md\n  > ~~~\n", want: false},
		{name: "inline code", body: "`See @AGENTS.md for shared rules`\n", want: false},
		{name: "mixed width code span", body: "``before ``` inside ` @AGENTS.md end``\n", want: false},
		{name: "indented code", body: "    @AGENTS.md\n", want: false},
		{name: "blockquote indented code", body: ">     @AGENTS.md\n", want: false},
		{name: "space tab indented code", body: " \t@AGENTS.md\n", want: false},
		{name: "html comment", body: "<!--\n@AGENTS.md\n-->\n", want: false},
		{name: "fence container ends before visible import", body: "> ~~~\n> sample\n\n@AGENTS.md\n", want: true},
		{name: "invalid fence info exposes import", body: "```example `literal`\n@AGENTS.md\n", want: true},
		{name: "unmatched code span marker is text", body: "A lone ` marker\n\n@AGENTS.md\n", want: true},
		{name: "comment backtick does not hide later import", body: "<!-- ` -->\n@AGENTS.md\n", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := importsShared([]byte(tc.body)); got != tc.want {
				t.Fatalf("importsShared() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCheckRepositoryUsesEffectiveCodexBudgetAndBlocksMissingClaudeImport(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	ctx := context.Background()
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)
	if err := os.Remove(filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	status, err := CheckRepository(ctx, repo, "claude", CheckOptions{})
	if err != nil || len(status.Problems) != 1 || len(status.Warnings) != 0 || !strings.Contains(status.Problems[0], "CLAUDE.md is missing") {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "# notes only\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || len(status.Warnings) != 0 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("status = %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "```text\n@AGENTS.md\n```\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("code block import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "````text\n```\n@AGENTS.md\n````\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("nested code block import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "> ~~~text\n> @AGENTS.md\n> ~~~\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("blockquote code block import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "~~~text\n> ~~~\n@AGENTS.md\n~~~\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("quoted line closed top-level code block: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "- ~~~text\n  @AGENTS.md\n  ~~~\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("list code block import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "`See @AGENTS.md for shared rules`\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("inline code import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "`opening code span\n@AGENTS.md\nclosing code span`\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("multiline code span import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "    @AGENTS.md\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("indented code import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "<!--\n@AGENTS.md\n-->\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("comment import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md.backup\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "does not import @AGENTS.md") {
		t.Fatalf("backup path import was accepted: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "See @AGENTS.md for shared rules.\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("inline import was rejected: %+v", status)
	}
	if status, _ = CheckRepository(ctx, repo, "codex", CheckOptions{}); len(status.Problems) != 0 {
		t.Fatalf("Codex status blocked by the Claude bridge: %+v", status)
	}
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 0 || len(status.Warnings) != 0 {
		t.Fatalf("status = %+v", status)
	}
	write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"claudeMdExcludes":["**/CLAUDE.md"]}`)
	status, _ = CheckRepository(ctx, repo, "claude", CheckOptions{})
	if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "CLAUDE.md") || !strings.Contains(status.Problems[0], "will not load") {
		t.Fatalf("shared bridge exclusion was not reported: %+v", status)
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

func TestCheckRepositoryChecksSharedAndOverrideCodexBudgets(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	prepare(t, repo)

	status, err := CheckRepository(context.Background(), repo, "all", CheckOptions{})
	if err != nil || len(status.Problems) != 0 {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	if !contains(status.Generated, filepath.Join(repo, "AGENTS.override.md")) || !contains(status.Generated, filepath.Join(repo, "CLAUDE.local.md")) {
		t.Fatalf("generated files not reported: %+v", status.Generated)
	}
}

func TestCodexSessionStartReportsDocumentBudgetProblem(t *testing.T) {
	home := testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	if !exists(filepath.Join(target, "AGENTS.override.md")) || exists(filepath.Join(target, "CLAUDE.local.md")) {
		t.Fatalf("unexpected files in %s", target)
	}
	if !strings.Contains(stderr, "CLAUDE.local.md") || !strings.Contains(stderr, `"CLAUDE.local.md"`) || !strings.Contains(stderr, "not git-ignored") {
		t.Fatalf("skip reason not reported: %q", stderr)
	}
}

func TestTrackedGeneratedFilesAreNeverRemoved(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	if !exists(filepath.Join(repo, "AGENTS.override.md")) || exists(filepath.Join(repo, "CLAUDE.local.md")) {
		t.Fatal("tracked generated file removed or untracked one kept")
	}
	status, _ := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if len(status.Problems) != 1 || status.Problems[0] != res.SkipReasons()[0] {
		t.Fatalf("status = %+v", status)
	}
}

func TestSharedSourceErrorBlocksOverrideRemoval(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	globalIgnore(t, "AGENTS.md", "AGENTS.local.md", "AGENTS.override.md", "CLAUDE.local.md")
	repo := resolvePath(filepath.Join(t.TempDir(), "repo"))
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\nAGENTS.local.md\nAGENTS.override.md\nCLAUDE.local.md\n")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	git(t, repo, "add", ".gitignore", "CLAUDE.md")
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
	legacyBridge := filepath.Join(linked, "CLAUDE.md")
	recordGeneratedForTest(t, linked, legacyBridge)
	status, err = CheckRepository(context.Background(), linked, "claude", CheckOptions{})
	if err != nil || !contains(status.Generated, legacyBridge) {
		t.Fatalf("legacy generated CLAUDE.md not reported: %+v err=%v", status, err)
	}
}

func TestClaudeLinkedWorktreeDeliversOnLocalCopyChange(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	linked := addWorktree(t, repo, "feature")
	code, stdout, _ := hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body" {
		t.Fatalf("startup after copy creation: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 2\n")
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "resume"})
	if code != 0 || stdout != "" || read(t, filepath.Join(linked, "AGENTS.local.md")) != "private body 2\n" {
		t.Fatalf("resume delivered or did not refresh: %d %q", code, stdout)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body 3\n")
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "private body 3" {
		t.Fatalf("startup after copy refresh: %d %q", code, stdout)
	}
	code, stdout, _ = hook(t, "claude", "SessionStart", map[string]any{"cwd": linked, "source": "startup"})
	if code != 0 || stdout != "" {
		t.Fatalf("unchanged startup delivered: %d %q", code, stdout)
	}
}

func TestSourceErrorBlocksStatusAndPrepareAlike(t *testing.T) {
	testHome(t)
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md")
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
	if !exists(filepath.Join(repo, "CLAUDE.local.md")) || !exists(filepath.Join(repo, "AGENTS.override.md")) {
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
	globalIgnore(t, "AGENTS.override.md", "CLAUDE.local.md", "AGENTS.local.md")
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	code, stdout, stderr := hook(t, "claude", "WorktreeCreate", map[string]any{"cwd": repo, "name": "legacy"})
	if code != 0 {
		t.Fatalf("create: %d %q %q", code, stdout, stderr)
	}
	target := strings.TrimSpace(stdout)
	bridge := filepath.Join(target, "CLAUDE.md")
	git(t, target, "rm", "-q", "--cached", "CLAUDE.md")
	git(t, target, "commit", "-q", "-m", "untrack bridge")
	write(t, filepath.Join(target, ".gitignore"), "AGENTS.local.md\nCLAUDE.md\n")
	git(t, target, "commit", "-q", "-am", "ignore bridge")
	write(t, bridge, "@AGENTS.md\n")
	recordGeneratedForTest(t, repo, bridge)
	if res := prepare(t, target); len(res.Removed) != 0 || len(res.Skipped) != 0 || !exists(bridge) {
		t.Fatalf("legacy bridge treated as orphan: %+v", res)
	}
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
