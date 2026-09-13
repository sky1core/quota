package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inspectedTestFile struct {
	present bool
	data    string
	info    os.FileInfo
}

func snapshotInstructionFiles(t *testing.T, repo, linked string) map[string]inspectedTestFile {
	t.Helper()
	paths := []string{filepath.Join(repo, ".git", "info", "exclude"), filepath.Join(repo, ".git", "quota-instructions.json")}
	for _, dir := range []string{repo, linked} {
		for _, name := range []string{sharedRule, localRule, sharedBridge, localBridge, ".gitignore"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	snapshot := map[string]inspectedTestFile{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			snapshot[path] = inspectedTestFile{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		snapshot[path] = inspectedTestFile{true, regressionRead(t, path), info}
	}
	return snapshot
}
func assertInstructionSnapshot(t *testing.T, snapshot map[string]inspectedTestFile) {
	t.Helper()
	for path, before := range snapshot {
		after, err := os.Lstat(path)
		if !before.present {
			if !os.IsNotExist(err) {
				t.Fatalf("rejected operation created %s: %v", path, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if regressionRead(t, path) != before.data || !os.SameFile(before.info, after) || before.info.Mode() != after.Mode() || !before.info.ModTime().Equal(after.ModTime()) {
			t.Fatalf("rejected operation modified %s", path)
		}
	}
}
func untrackedSharedWorktrees(t *testing.T) (string, string) {
	t.Helper()
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "fixture\n")
	git(t, repo, "add", "tracked.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, sharedRule), strings.Repeat("shared rules\n", 8))
	linked := resolvePath(filepath.Join(t.TempDir(), "linked"))
	git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	return repo, linked
}
func TestPreflightAndSetupRejectPlannedInstructionConflicts(t *testing.T) {
	cases := []struct {
		name, agent, policy, reason string
		prepare                     func(*testing.T, string, string)
	}{
		{"future shared bridge", "claude", "primary", "contains content beyond", func(t *testing.T, repo, linked string) {
			write(t, filepath.Join(linked, sharedBridge), "user-owned instructions\n")
		}},
		{"unverified private copy", "claude", "primary", "ownership is not verified", func(t *testing.T, repo, linked string) {
			write(t, filepath.Join(repo, localRule), "current private rules\n")
			write(t, filepath.Join(linked, localBridge), generatedLocal("different private rules"))
		}},
		{"future Codex budget", "codex", "primary", "project_doc_max_bytes", func(t *testing.T, repo, linked string) {
			write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", linked))
			dir := filepath.Join(linked, ".codex")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "config.toml"), "project_doc_max_bytes = 8\n")
		}},
		{"future Claude exclusion", "claude", "primary", "claudeMdExcludes", func(t *testing.T, repo, linked string) {
			dir := filepath.Join(linked, ".claude")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "settings.json"), fmt.Sprintf(`{"claudeMdExcludes":[%q]}`, filepath.Join(linked, sharedBridge)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, linked := untrackedSharedWorktrees(t)
			tc.prepare(t, repo, linked)
			snapshot := snapshotInstructionFiles(t, repo, linked)
			if _, err := PlanRepositoryWithPolicy(context.Background(), repo, tc.agent, tc.policy); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("plan result: %v, want %s", err, tc.reason)
			}
			assertInstructionSnapshot(t, snapshot)
			if err := SetupRepository(context.Background(), repo, tc.agent, tc.policy); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("setup result: %v, want %s", err, tc.reason)
			}
			assertInstructionSnapshot(t, snapshot)
		})
	}
}
func TestCheckRejectsMissingSourcesAndDanglingBridges(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "# shared\n")
	write(t, filepath.Join(repo, localRule), "# private\n")
	regressionSetup(t, repo)
	for _, name := range []string{sharedRule, localRule} {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []string{"claude", "codex", "all"} {
		code, out, errb := run(t, "", "check", "--runtime="+agent, repo)
		if code != 1 || !strings.Contains(out+errb, "instruction sources are missing") {
			t.Fatalf("missing sources accepted for %s: %d %s %s", agent, code, out, errb)
		}
		if agent != "codex" && strings.Count(out, "dangling bridge") != 2 {
			t.Fatalf("dangling bridges omitted: %s", out)
		}
	}
	unrelated := newRepo(t)
	for _, policy := range []string{"claude-session", "codex-session"} {
		input := `{"cwd":` + quote(unrelated) + `}`
		if policy == "codex-session" {
			input = `{"cwd":` + quote(unrelated) + `,"source":"startup"}`
		}
		code, out, errb := run(t, input, "json", "SessionStart", "x", "y", unrelated, policy)
		if code != 0 || out != "" {
			t.Fatalf("unrelated repository hook changed: %d %s %s", code, out, errb)
		}
	}
}
