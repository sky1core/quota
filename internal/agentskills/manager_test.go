package agentskills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/config"
)

func testManager(t *testing.T, extras bool) *Manager {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "caller-codex"))
	cfg := config.Config{}
	if extras {
		cfg.ClaudeAccounts = []config.ClaudeAccount{{Key: "claude-2", ConfigDir: filepath.Join(home, "claude-extra")}}
		cfg.CodexAccounts = []config.CodexAccount{{Key: "codex-2", Home: filepath.Join(home, "codex-extra")}}
	}
	m, err := New(cfg, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func putFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func fixtureSkill(t *testing.T, name string) FetchedSkill {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	putFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: Synthetic example.\n---\nExample.\n", 0o644)
	putFile(t, filepath.Join(dir, "scripts", "example.sh"), "#!/bin/sh\nprintf example\n", 0o755)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return FetchedSkill{Name: name, Dir: dir}
}

func mustInstall(t *testing.T, m *Manager, skill FetchedSkill) []Result {
	t.Helper()
	results, err := m.Install(context.Background(), []FetchedSkill{skill})
	if err != nil {
		t.Fatalf("install: %v; %#v", err, results)
	}
	return results
}

func TestSharedSkillEditsAndBackupRemoval(t *testing.T) {
	m := testManager(t, true)
	skill := fixtureSkill(t, "example")
	results := mustInstall(t, m, skill)
	if len(results) != 5 || results[0].Status != "installed" {
		t.Fatalf("install results: %#v", results)
	}
	source := filepath.Join(m.sourceDir, "example")
	if _, err := os.Lstat(filepath.Join(os.Getenv("HOME"), ".config", "quota", "agent-skills", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("ownership state created: %v", err)
	}
	for _, result := range results[1:3] {
		info, err := os.Lstat(result.Path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 || result.Status != "linked" {
			t.Fatalf("target is not linked: %#v (%v)", result, err)
		}
		resolved, err := filepath.EvalSymlinks(result.Path)
		if err != nil || resolved != source {
			t.Fatalf("link resolves to %q (%v), want %q", resolved, err, source)
		}
	}
	for _, result := range results[3:] {
		if result.Target.Role != roleCodexDuplicate || result.Status != "missing" {
			t.Fatalf("Codex home changed: %#v", result)
		}
	}
	if strings.Join(results[0].Target.Accounts, ",") != "codex,codex-2" {
		t.Fatalf("Codex accounts: %#v", results[0].Target)
	}
	putFile(t, filepath.Join(results[1].Path, "SKILL.md"), "direct edit\n", 0o644)
	if got, err := os.ReadFile(filepath.Join(results[2].Path, "SKILL.md")); err != nil || string(got) != "direct edit\n" {
		t.Fatalf("edit not shared: %q (%v)", got, err)
	}
	listed, err := m.List()
	if err != nil || len(listed) != 5 || listed[0].RealPath != source || listed[1].Status != "linked" {
		t.Fatalf("list: %#v (%v)", listed, err)
	}
	removed, err := m.Remove(context.Background(), "example")
	if err != nil || len(removed) != 5 {
		t.Fatalf("remove: %#v (%v)", removed, err)
	}
	for _, result := range removed[:3] {
		if result.Status != "backed-up" || result.BackupPath == "" {
			t.Fatalf("missing backup: %#v", result)
		}
		if _, err := os.Lstat(result.Path); !os.IsNotExist(err) {
			t.Fatalf("path remains: %s (%v)", result.Path, err)
		}
		if _, err := os.Lstat(result.BackupPath); err != nil {
			t.Fatalf("backup missing: %s (%v)", result.BackupPath, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(removed[0].BackupPath, "SKILL.md")); err != nil || string(got) != "direct edit\n" {
		t.Fatalf("edited content lost: %q (%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(skill.Dir, "SKILL.md")); err != nil {
		t.Fatalf("input source removed: %v", err)
	}
}

func TestFailedRemoveRestoresMovedEntries(t *testing.T) {
	m := testManager(t, true)
	results := mustInstall(t, m, fixtureSkill(t, "example"))
	locked := results[2].Target.Dir
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	removed, err := m.Remove(context.Background(), "example")
	if err == nil || removed[2].Status != "failed" {
		t.Fatalf("remove from read-only target: %#v (%v)", removed, err)
	}
	for _, result := range removed[:2] {
		if result.Status != "restored" || result.BackupPath != "" {
			t.Fatalf("entry not restored: %#v", result)
		}
	}
	listed, err := m.List()
	if err != nil || listed[0].Status != "source" || listed[1].Status != "linked" || listed[2].Status != "linked" {
		t.Fatalf("state after failed remove: %#v (%v)", listed, err)
	}
}

func TestAccountsSharingSkillDirectoryUseOneTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"claude-extra", "claude-source"} {
		if err := os.MkdirAll(filepath.Join(home, account), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(shared, filepath.Join(home, "claude-extra", "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".agents/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".agents/skills"), filepath.Join(home, "claude-source", "skills")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Key: "claude-2", ConfigDir: filepath.Join(home, "claude-extra")},
		{Key: "claude-3", ConfigDir: filepath.Join(home, "claude-source")},
	}}
	m, err := New(cfg, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	results := mustInstall(t, m, fixtureSkill(t, "example"))
	if len(results) != 3 || strings.Join(results[0].Target.Accounts, ",") != "codex,claude-3" ||
		strings.Join(results[1].Target.Accounts, ",") != "claude,claude-2" || results[1].Status != "linked" {
		t.Fatalf("shared account targets: %#v", results)
	}
}

func TestDistinctCaseSensitiveAccountsReceiveEverySkill(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			upper, lower := filepath.Join(home, "Team"), filepath.Join(home, "team")
			for _, path := range []string{upper, lower} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			u, _ := os.Stat(upper)
			l, _ := os.Stat(lower)
			if os.SameFile(u, l) {
				t.Skip("requires a case-sensitive filesystem")
			}
			if nested {
				lower = filepath.Join(lower, "skills", "nested")
			}
			cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{
				{Key: "claude-2", ConfigDir: upper},
				{Key: "claude-3", ConfigDir: lower},
			}}
			m, err := New(cfg, "global", "")
			if err != nil {
				t.Fatal(err)
			}
			mustInstall(t, m, fixtureSkill(t, "example"))
			for _, account := range []string{upper, lower} {
				path := filepath.Join(account, "skills", "example")
				if _, err := os.ReadFile(filepath.Join(path, "SKILL.md")); err != nil {
					t.Fatalf("account did not receive skill: %s: %v", account, err)
				}
			}
			if _, err := m.Remove(context.Background(), "example"); err != nil {
				t.Fatal(err)
			}
			for _, account := range []string{upper, lower} {
				if _, err := os.Lstat(filepath.Join(account, "skills", "example")); !os.IsNotExist(err) {
					t.Fatalf("account skill remains after removal: %s: %v", account, err)
				}
			}
		})
	}
}

func TestCaseAliasesShareOneSkillTarget(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			upper, lower := filepath.Join(home, "Team"), filepath.Join(home, "team")
			if err := os.Mkdir(upper, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(lower); os.IsNotExist(err) {
				t.Skip("requires a case-insensitive filesystem")
			} else if err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.Mkdir(filepath.Join(upper, "skills"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Config{ClaudeAccounts: []config.ClaudeAccount{
				{Key: "claude-2", ConfigDir: upper},
				{Key: "claude-3", ConfigDir: lower},
			}}
			m, err := New(cfg, "global", "")
			if err != nil {
				t.Fatal(err)
			}
			results := mustInstall(t, m, fixtureSkill(t, "example"))
			if len(results) != 4 || strings.Join(results[2].Target.Accounts, ",") != "claude-2,claude-3" {
				t.Fatalf("same directory handled more than once: %#v", results)
			}
			if _, err := m.Remove(context.Background(), "example"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLinkConnectsSkillsCreatedAtCanonicalPath(t *testing.T) {
	m := testManager(t, true)
	putFile(t, filepath.Join(m.sourceDir, "direct", "SKILL.md"), "direct\n", 0o644)
	putFile(t, filepath.Join(m.sourceDir, "occupied", "SKILL.md"), "occupied\n", 0o644)
	putFile(t, filepath.Join(m.sourceDir, "README.md"), "not a skill\n", 0o644)
	occupied := filepath.Join(m.targets[2].Dir, "occupied", "SKILL.md")
	putFile(t, occupied, "account copy\n", 0o644)
	results, err := m.Link(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("occupied target not reported: %#v (%v)", results, err)
	}
	for _, result := range results {
		wantLinked := result.Target.Role == roleClaude && (result.Name == "direct" || result.Path != filepath.Dir(occupied))
		if wantLinked && result.Status != "linked" {
			t.Fatalf("missing link: %#v", result)
		}
	}
	if got, err := os.ReadFile(occupied); err != nil || string(got) != "account copy\n" {
		t.Fatalf("occupied target changed: %q (%v)", got, err)
	}
	results, err = m.Link(context.Background(), []string{"direct"})
	if err != nil || results[1].Status != "linked" || results[2].Status != "linked" {
		t.Fatalf("repeat link: %#v (%v)", results, err)
	}
}

func TestCodexHomeDuplicateBlocksInstallAndIsRemoved(t *testing.T) {
	m := testManager(t, false)
	codexSkills := m.targets[2].Dir
	putFile(t, filepath.Join(codexSkills, ".system", "bundled", "SKILL.md"), "bundled\n", 0o644)
	duplicate := filepath.Join(codexSkills, "example", "SKILL.md")
	putFile(t, duplicate, "old copy\n", 0o644)
	results, err := m.Install(context.Background(), []FetchedSkill{fixtureSkill(t, "example")})
	if err == nil || results[2].Status != "blocked" {
		t.Fatalf("Codex duplicate accepted: %#v (%v)", results, err)
	}
	if _, err := os.Lstat(filepath.Join(m.sourceDir, "example")); !os.IsNotExist(err) {
		t.Fatalf("source created despite duplicate: %v", err)
	}
	listed, err := m.List()
	if err != nil || len(listed) != 3 || listed[0].Name != "example" || listed[2].Status != "duplicate" {
		t.Fatalf("list: %#v (%v)", listed, err)
	}
	removed, err := m.Remove(context.Background(), "example")
	if err != nil || removed[2].Status != "backed-up" {
		t.Fatalf("remove duplicate: %#v (%v)", removed, err)
	}
	if got, err := os.ReadFile(filepath.Join(removed[2].BackupPath, "SKILL.md")); err != nil || string(got) != "old copy\n" {
		t.Fatalf("duplicate backup: %q (%v)", got, err)
	}
}

func TestInstallKeepsExistingLinkToCanonicalPath(t *testing.T) {
	m := testManager(t, false)
	path := filepath.Join(m.targets[1].Dir, "example")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(path), filepath.Join(m.sourceDir, "example"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, path); err != nil {
		t.Fatal(err)
	}
	if listed, err := m.List(); err != nil || listed[1].Status != "broken" {
		t.Fatalf("dangling link: %#v (%v)", listed, err)
	}
	results := mustInstall(t, m, fixtureSkill(t, "example"))
	if valid, err := isSkillDir(path); results[1].Status != "linked" || !valid || err != nil {
		t.Fatalf("existing link: %#v", results)
	}
}

func TestExistingTargetBlocksInstallWithoutChanges(t *testing.T) {
	m := testManager(t, false)
	skill := fixtureSkill(t, "example")
	existing := filepath.Join(m.targets[1].Dir, "example", "SKILL.md")
	putFile(t, existing, "local edit\n", 0o644)
	results, err := m.Install(context.Background(), []FetchedSkill{skill})
	if err == nil || results[1].Status != "blocked" {
		t.Fatalf("conflict accepted: %#v (%v)", results, err)
	}
	if _, err := os.Lstat(filepath.Join(m.sourceDir, "example")); !os.IsNotExist(err) {
		t.Fatalf("source created despite conflict: %v", err)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "local edit\n" {
		t.Fatalf("existing file changed: %q (%v)", got, err)
	}
}

func TestExistingSourceBlocksInstallEvenWithIdenticalFiles(t *testing.T) {
	m := testManager(t, false)
	skill := fixtureSkill(t, "example")
	mustInstall(t, m, skill)
	results, err := m.Install(context.Background(), []FetchedSkill{skill})
	if err == nil || results[0].Status != "blocked" {
		t.Fatalf("existing source accepted: %#v (%v)", results, err)
	}
}

func TestCancelledInstallReportsAllPendingTargets(t *testing.T) {
	m := testManager(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := m.Install(ctx, []FetchedSkill{fixtureSkill(t, "example")})
	if err == nil || len(results) != 3 {
		t.Fatalf("cancelled install: %#v (%v)", results, err)
	}
	for _, result := range results[:2] {
		if result.Status != "skipped" || result.Error == "" {
			t.Fatalf("unreported cancellation: %#v", result)
		}
	}
}

func TestInvalidCodexAccountBlocksSkillOperations(t *testing.T) {
	testManager(t, false)
	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "invalid", Home: filepath.Join(t.TempDir(), "codex")}}}
	if _, err := New(cfg, "global", ""); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid Codex account ignored: %v", err)
	}
}

func TestListReportsOccupiedInvalidName(t *testing.T) {
	m := testManager(t, false)
	putFile(t, filepath.Join(m.sourceDir, ".hidden", "SKILL.md"), "hidden\n", 0o644)
	results, err := m.List()
	if err != nil || len(results) != 3 || results[0].Name != ".hidden" {
		t.Fatalf("hidden source omitted: %#v (%v)", results, err)
	}
}

func TestRepoScopeUsesRepositoryPaths(t *testing.T) {
	m := testManager(t, false)
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	repoManager, err := New(config.Config{}, "repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	skill := fixtureSkill(t, "example")
	results := mustInstall(t, repoManager, skill)
	if len(results) != 3 || results[0].Path != filepath.Join(repo, ".agents/skills", "example") ||
		results[1].Path != filepath.Join(repo, ".claude", "skills", "example") {
		t.Fatalf("repo targets: %#v", results)
	}
	link, err := os.Readlink(results[1].Path)
	if err != nil || filepath.IsAbs(link) {
		t.Fatalf("repo link is not relative: %q (%v)", link, err)
	}
	if _, err := os.Lstat(filepath.Join(m.sourceDir, "example")); !os.IsNotExist(err) {
		t.Fatalf("global scope changed: %v", err)
	}
	duplicate := filepath.Join(repo, ".codex", "skills", "example", "SKILL.md")
	putFile(t, duplicate, "repo Codex copy\n", 0o644)
	if linked, err := repoManager.Link(context.Background(), nil); err == nil || linked[2].Status != "duplicate" {
		t.Fatalf("repo Codex duplicate not reported: %#v (%v)", linked, err)
	}
	removed, err := repoManager.Remove(context.Background(), skill.Name)
	if err != nil || !strings.HasPrefix(removed[0].BackupPath, repoManager.backupRoot) || removed[2].Status != "backed-up" {
		t.Fatalf("repo backup: %#v (%v)", removed, err)
	}
	if _, err := os.Lstat(filepath.Dir(duplicate)); !os.IsNotExist(err) {
		t.Fatalf("repo Codex duplicate remains: %v", err)
	}
}

func TestRemoveExternalLinkDoesNotFollowTarget(t *testing.T) {
	m := testManager(t, false)
	skill := fixtureSkill(t, "example")
	path := filepath.Join(m.targets[1].Dir, "example")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skill.Dir, path); err != nil {
		t.Fatal(err)
	}
	removed, err := m.Remove(context.Background(), "example")
	if err != nil || removed[1].Status != "backed-up" {
		t.Fatalf("remove external link: %#v (%v)", removed, err)
	}
	if _, err := os.Stat(filepath.Join(skill.Dir, "SKILL.md")); err != nil {
		t.Fatalf("external target removed: %v", err)
	}
	if info, err := os.Lstat(removed[1].BackupPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link backup missing: %v", err)
	}
}
