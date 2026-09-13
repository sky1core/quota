package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedIgnoreProtectionSurvivesExternalExcludeRefresh(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	const rel = "rules.local.txt"
	write(t, filepath.Join(repo, rel), "rules.local.txt\nAGENTS.local.md\nAGENTS.override.md\n")
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	write(t, exclude, rel+"\n")
	if err := SetupRepository(context.Background(), repo, "codex", "checkout", rel); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.excludesFile", filepath.Join(linked, rel))
	write(t, exclude, "")
	write(t, filepath.Join(repo, ".gitignore"), "")
	for _, w := range []string{repo, linked} {
		for _, name := range []string{localRule, codexRule} {
			git(t, w, "check-ignore", "-q", "--", name)
		}
	}
	write(t, filepath.Join(repo, rel), "rules.local.txt\n")
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	if regressionRead(t, filepath.Join(linked, rel)) != "rules.local.txt\n" {
		t.Fatal("exclude source was not refreshed")
	}
	for _, w := range []string{repo, linked} {
		for _, name := range []string{localRule, codexRule, rel} {
			if matched := git(t, w, "check-ignore", "-v", "--", name); !strings.Contains(matched, "info/exclude") {
				t.Fatalf("%s/%s relies on an external exclude file: %s", w, name, matched)
			}
		}
	}
}

func TestManagedClaudeCopyRequiresRecordedOwnership(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	path := filepath.Join(linked, localBridge)
	local, err := readRule(filepath.Join(repo, localRule))
	if err != nil {
		t.Fatal(err)
	}
	body := generatedLocal(local)
	write(t, path, body)
	if err := SetupRepository(context.Background(), repo, "all", "checkout"); err == nil || !strings.Contains(err.Error(), "ownership is not verified") {
		t.Fatalf("unrecorded copy was adopted: %v", err)
	}
	if regressionRead(t, path) != body || exists(filepath.Join(repo, ".git", "quota-instructions.json")) {
		t.Fatal("setup changed or adopted an unrecorded copy")
	}
	if err := UninstallRepository(context.Background(), repo, "all"); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("uninstall did not report the unrecorded copy: %v", err)
	}
	if regressionRead(t, path) != body {
		t.Fatal("uninstall removed an unrecorded copy")
	}
}

func TestManagedSettingsSymlinkUsesFutureContent(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		createCopy      bool
		wantError       bool
		linkedParent    bool
	}{
		{"invalid refresh", "4096", "1", true, true, false},
		{"valid repair", "1", "4096", true, false, false},
		{"missing target invalid", "1", "1", false, true, false},
		{"missing target valid", "4096", "4096", false, false, false},
		{"parent after directory link", "4096", "1", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rel = "config/native.toml"
			repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = "+tc.old+"\n")
			if tc.createCopy {
				if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			target := "../config/native.toml"
			if tc.linkedParent {
				if err := os.Mkdir(filepath.Join(linked, "config", "inner"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("config/inner", filepath.Join(linked, "alias")); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(linked, "native.toml"), "project_doc_max_bytes = 4096\n")
				target = "../alias/../native.toml"
			}
			if err := os.Symlink(target, filepath.Join(linked, ".codex", "config.toml")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(repo, rel), "project_doc_max_bytes = "+tc.next+"\n")
			err := SetupRepository(context.Background(), repo, "codex", "checkout", rel)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "over Codex project_doc_max_bytes") {
					t.Fatalf("future setting was not used: %v", err)
				}
				if exists(filepath.Join(linked, codexRule)) {
					t.Fatal("invalid future settings created native instructions")
				}
				if tc.createCopy && regressionRead(t, filepath.Join(linked, rel)) != "project_doc_max_bytes = "+tc.old+"\n" {
					t.Fatal("rejected settings refresh modified the old copy")
				}
			} else if err != nil {
				t.Fatalf("valid future settings rejected: %v", err)
			} else if regressionRead(t, filepath.Join(linked, rel)) != fmt.Sprintf("project_doc_max_bytes = %s\n", tc.next) {
				t.Fatal("future settings not copied")
			}
		})
	}
}

func TestManagedClaudeStatusChecksRecordedOwnership(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "all", "checkout"); err != nil {
		t.Fatal(err)
	}
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(resolvePath(linked), localBridge)
	for _, known := range []string{"", "different-digest"} {
		state, err := readState(r)
		if err != nil {
			t.Fatal(err)
		}
		if known == "" {
			delete(state.Generated, path)
		} else {
			state.Generated[path] = known
		}
		if err := writeState(r, state); err != nil {
			t.Fatal(err)
		}
		if code, out, errb := run(t, "", "check", "--runtime=claude", repo); code == 0 || !(strings.Contains(out+errb, "ownership") || strings.Contains(out+errb, "user edits")) {
			t.Fatalf("invalid ownership passed status: %d %q %q", code, out, errb)
		}
	}
}

func TestManagedPlanListsRemovalAndCommonIgnoreChanges(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	if err := SetupRepository(context.Background(), repo, "codex", "checkout"); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	write(t, exclude, "")
	for _, w := range []string{repo, linked} {
		write(t, filepath.Join(w, ".gitignore"), "AGENTS.local.md\nAGENTS.override.md\n")
	}
	paths, err := PlanRepository(context.Background(), repo, "codex")
	if err != nil || !contains(paths, "ignore patterns: "+exclude) {
		t.Fatalf("common exclude preparation missing from plan: %v %v", paths, err)
	}
	if regressionRead(t, exclude) != "" {
		t.Fatal("plan wrote common ignore patterns")
	}
	if err := os.Rename(filepath.Join(repo, localRule), filepath.Join(repo, "AGENTS.saved.md")); err != nil {
		t.Fatal(err)
	}
	paths, err = PlanRepository(context.Background(), repo, "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"remove managed local source copy: " + filepath.Join(resolvePath(linked), localRule), "remove native merged instructions: " + filepath.Join(resolvePath(linked), codexRule)} {
		if !contains(paths, expected) {
			t.Fatalf("removal missing from plan: %s; got %v", expected, paths)
		}
	}
	if !exists(filepath.Join(linked, localRule)) || !exists(filepath.Join(linked, codexRule)) {
		t.Fatal("plan removed generated files")
	}
}

func TestManagedSettingsRejectInvalidIntermediatePaths(t *testing.T) {
	for _, component := range []string{"regular-file", "missing-directory"} {
		t.Run(component, func(t *testing.T) {
			repo, linked := plannedSettingsFixture(t, "config/native.toml", "project_doc_max_bytes = 4096\n")
			if err := SetupRepository(context.Background(), repo, "claude", "checkout", "config/native.toml"); err != nil {
				t.Fatal(err)
			}
			if component == "regular-file" {
				write(t, filepath.Join(linked, component), "ordinary file\n")
			}
			if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../"+component+"/../config/native.toml", filepath.Join(linked, ".codex", "config.toml")); err != nil {
				t.Fatal(err)
			}
			if err := SetupRepository(context.Background(), repo, "codex", ""); err == nil {
				t.Fatal("filesystem-invalid settings path was accepted")
			}
			if exists(filepath.Join(repo, codexRule)) || exists(filepath.Join(linked, codexRule)) {
				t.Fatal("invalid settings path changed native instruction files")
			}
		})
	}
}

func TestManagedSettingsCaseAliases(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "case-probe")
	write(t, probe, "probe")
	actual, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Stat(filepath.Join(filepath.Dir(probe), "CASE-PROBE"))
	if os.IsNotExist(err) {
		t.Skip("requires a case-insensitive filesystem")
	}
	if err != nil || !os.SameFile(actual, alias) {
		t.Fatalf("case alias probe: %v", err)
	}
	for _, tc := range []struct {
		name, old, next, rel, target string
		createCopy, wantError        bool
	}{
		{"invalid refresh", "4096", "1", "config/native.toml", "../CONFIG/NATIVE.toml", true, true},
		{"valid repair", "1", "4096", "config/native.toml", "../CONFIG/NATIVE.toml", true, false},
		{"missing target valid", "4096", "4096", "config/nested/native.toml", "../CONFIG/NESTED/NATIVE.toml", false, false},
		{"missing target invalid", "1", "1", "config/nested/native.toml", "../CONFIG/NESTED/NATIVE.toml", false, true},
		{"registered uppercase", "4096", "1", "CONFIG/NATIVE.toml", "../config/native.toml", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, linked := plannedSettingsFixture(t, tc.rel, "project_doc_max_bytes = "+tc.old+"\n")
			if tc.createCopy {
				if err := SetupRepository(context.Background(), repo, "claude", "checkout", tc.rel); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target, filepath.Join(linked, ".codex", "config.toml")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(repo, tc.rel), "project_doc_max_bytes = "+tc.next+"\n")
			_, planErr := PlanRepositoryWithPolicy(context.Background(), repo, "codex", "checkout", tc.rel)
			setupErr := SetupRepository(context.Background(), repo, "codex", "checkout", tc.rel)
			if tc.wantError {
				for _, err := range []error{planErr, setupErr} {
					if err == nil || !strings.Contains(err.Error(), "over Codex project_doc_max_bytes") {
						t.Fatalf("case alias missed future budget: %v", err)
					}
				}
				if exists(filepath.Join(linked, codexRule)) {
					t.Fatal("invalid future settings changed native instructions")
				}
				if tc.createCopy && regressionRead(t, filepath.Join(linked, tc.rel)) != "project_doc_max_bytes = "+tc.old+"\n" {
					t.Fatal("rejected refresh changed the managed copy")
				}
			} else if planErr != nil || setupErr != nil {
				t.Fatalf("valid case alias rejected: plan=%v setup=%v", planErr, setupErr)
			} else if regressionRead(t, filepath.Join(linked, tc.rel)) != "project_doc_max_bytes = "+tc.next+"\n" {
				t.Fatal("future settings not applied")
			}
		})
	}
}

func TestManagedSettingsHardlinkRemainsIndependent(t *testing.T) {
	const rel = "config/native.toml"
	repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = 4096\n")
	if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(linked, "config", "reference.toml")
	if err := os.Link(filepath.Join(linked, rel), reference); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../config/reference.toml", filepath.Join(linked, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, rel), "project_doc_max_bytes = 1\n")
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatalf("separate hardlink was treated as the replacement target: %v", err)
	}
	if regressionRead(t, reference) != "project_doc_max_bytes = 4096\n" || regressionRead(t, filepath.Join(linked, rel)) != "project_doc_max_bytes = 1\n" {
		t.Fatal("atomic replacement did not preserve the independent hardlink")
	}
}

func TestManagedSettingsUnicodeAliases(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		wantError       bool
	}{
		{"reject invalid refresh", "4096", "1", true},
		{"repair valid source", "1", "4096", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rel = "config/café.toml"
			repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = "+tc.old+"\n")
			if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(linked, "config", "café.toml")
			left, err := os.Lstat(filepath.Join(linked, rel))
			if err != nil {
				t.Fatal(err)
			}
			right, err := os.Lstat(alias)
			if os.IsNotExist(err) {
				t.Skip("filesystem does not alias NFC and NFD names")
			}
			if err != nil || !os.SameFile(left, right) {
				t.Fatalf("normalization alias probe: %v", err)
			}
			if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../config/café.toml", filepath.Join(linked, ".codex", "config.toml")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(repo, rel), "project_doc_max_bytes = "+tc.next+"\n")
			err = SetupRepository(context.Background(), repo, "codex", "")
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "over Codex project_doc_max_bytes") {
					t.Fatalf("Unicode alias missed future settings: %v", err)
				}
				if regressionRead(t, filepath.Join(linked, rel)) != "project_doc_max_bytes = "+tc.old+"\n" || exists(filepath.Join(linked, codexRule)) {
					t.Fatal("rejected Unicode refresh changed managed files")
				}
			} else if err != nil {
				t.Fatalf("Unicode alias repair failed: %v", err)
			}
		})
	}
}

func TestManagedSettingsUnresolvedFutureAliasRejectsBeforeWrites(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			rel, body, directory, config := "config/café.toml", "project_doc_max_bytes = 1\n", ".codex", "config.toml"
			target := "../config/café.toml"
			if agent == "claude" {
				rel, body, directory, config = "config/café.json", `{"disableAllHooks":true}`, ".claude", "settings.json"
				target = "../config/café.json"
			}
			repo, linked := plannedSettingsFixture(t, rel, body)
			if err := os.Mkdir(filepath.Join(linked, directory), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(linked, directory, config)); err != nil {
				t.Fatal(err)
			}
			if err := SetupRepository(context.Background(), repo, agent, "checkout", rel); err == nil {
				t.Fatal("unresolved future settings alias was treated as verified")
			}
			for _, path := range []string{filepath.Join(linked, rel), filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(linked, localBridge)} {
				if exists(path) {
					t.Fatalf("unresolved future alias wrote %s", path)
				}
			}
		})
	}
}

func TestManagedSettingsFutureDirectoryMakesAliasEffective(t *testing.T) {
	const rel = "config/new/placeholder"
	repo, linked := plannedSettingsFixture(t, rel, "local data\n")
	if err := os.MkdirAll(filepath.Join(linked, "config", "native"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(linked, "config", "native", "config.toml"), "project_doc_max_bytes = 1\n")
	if err := os.Symlink("config/new/../native", filepath.Join(linked, ".codex")); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "checkout", rel); err == nil || !strings.Contains(err.Error(), "over Codex project_doc_max_bytes") {
		t.Fatalf("newly effective alias missed existing settings: %v", err)
	}
	if exists(filepath.Join(linked, rel)) || exists(filepath.Join(linked, localRule)) || exists(filepath.Join(linked, codexRule)) {
		t.Fatal("rejected effective settings changed managed files")
	}
}

func TestManagedSettingsUnresolvedParentAliasRejectsBeforeWrites(t *testing.T) {
	const rel = "config/café/settings.json"
	repo, linked := plannedSettingsFixture(t, rel, `{"disableAllHooks":true}`)
	if err := os.Symlink("config/café", filepath.Join(linked, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err == nil || !strings.Contains(err.Error(), "cannot resolve planned settings symlink") {
		t.Fatalf("unresolved parent alias was treated as absent: %v", err)
	}
	if exists(filepath.Join(linked, rel)) || exists(filepath.Join(linked, localRule)) || exists(filepath.Join(linked, localBridge)) {
		t.Fatal("unresolved parent alias changed managed files")
	}
}

func TestManagedSettingsResolvedDirectoryAllowsAbsentOptionalFile(t *testing.T) {
	const rel = "config/settings.json"
	repo, linked := plannedSettingsFixture(t, rel, `{}`)
	if err := os.Symlink("config", filepath.Join(linked, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err != nil {
		t.Fatalf("absent optional settings.local.json rejected: %v", err)
	}
	if regressionRead(t, filepath.Join(linked, rel)) != `{}` {
		t.Fatal("resolved directory settings were not copied")
	}
}

func TestManagedSettingsAliasTargetRemovalRejectsBeforeWrites(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	write(t, filepath.Join(repo, localRule), "project_doc_max_bytes = 4096\n")
	if err := SetupRepository(context.Background(), repo, "codex", "checkout"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../AGENTS.local.md", filepath.Join(linked, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", resolvePath(linked)))
	if err := os.Rename(filepath.Join(repo, localRule), filepath.Join(repo, "AGENTS.saved.md")); err != nil {
		t.Fatal(err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", ""); err == nil || !strings.Contains(err.Error(), "will be removed") {
		t.Fatalf("alias to a removed target was treated as absent settings: %v", err)
	}
	for _, path := range []string{filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(repo, codexRule)} {
		if !exists(path) {
			t.Fatalf("invalid future alias removed %s", path)
		}
	}
}

func TestManagedSettingsRejectFutureDirectoryAsSettingsFile(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			const rel = "config/settings-folder/child.txt"
			repo, linked := plannedSettingsFixture(t, rel, "local data\n")
			directory, config := ".codex", "config.toml"
			if agent == "claude" {
				directory, config = ".claude", "settings.json"
			}
			if err := os.Mkdir(filepath.Join(linked, directory), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../config/settings-folder", filepath.Join(linked, directory, config)); err != nil {
				t.Fatal(err)
			}
			if err := SetupRepository(context.Background(), repo, agent, "checkout", rel); err == nil || !strings.Contains(err.Error(), "planned as a directory") {
				t.Fatalf("future directory accepted as settings: %v", err)
			}
			if exists(filepath.Join(linked, rel)) || exists(filepath.Join(linked, localRule)) {
				t.Fatal("invalid future settings type changed managed files")
			}
		})
	}
}

func TestManagedSettingsReadsFuturePrimarySharedCopy(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "seed.txt"), "seed")
	git(t, repo, "add", "seed.txt")
	seedCommit(t, repo)
	linked := resolvePath(filepath.Join(t.TempDir(), "linked"))
	git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	write(t, filepath.Join(repo, sharedRule), "{}")
	write(t, filepath.Join(repo, localRule), "")
	if err := SetupRepository(context.Background(), repo, "codex", "primary"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(linked, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(linked, ".claude", "settings.json")
	if err := os.Symlink("../AGENTS.md", settings); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, sharedRule), `{"disableAllHooks":true}`)
	planned, err := PlannedClaudeSettings(context.Background(), repo, "all", "primary")
	if err != nil || planned[settings]["disableAllHooks"] != true {
		t.Fatalf("primary policy missing from planned settings: %v %v", planned, err)
	}
	if err := SetupRepository(context.Background(), repo, "all", ""); err == nil || !strings.Contains(err.Error(), "disableAllHooks is true") {
		t.Fatalf("future primary settings not checked: %v", err)
	}
	if regressionRead(t, filepath.Join(linked, sharedRule)) != "{}" || regressionRead(t, filepath.Join(linked, codexRule)) != "{}\n\n" {
		t.Fatal("invalid primary settings changed generated files")
	}
}

func TestManagedSettingsReadsFutureIgnoreContents(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	write(t, exclude, "# empty rules\n")
	if err := os.Mkdir(filepath.Join(linked, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exclude, filepath.Join(linked, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", resolvePath(linked)))
	if err := SetupRepository(context.Background(), repo, "codex", "checkout"); err == nil || !strings.Contains(err.Error(), "could not parse") {
		t.Fatalf("future ignore contents were not checked as settings: %v", err)
	}
	if regressionRead(t, exclude) != "# empty rules\n" || exists(filepath.Join(linked, localRule)) || exists(filepath.Join(linked, codexRule)) {
		t.Fatal("future invalid settings changed repository files")
	}
}
