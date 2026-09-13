package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func plannedSettingsWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, path, body)
}

func plannedSettingsFixture(t *testing.T, rel, body string) (string, string) {
	t.Helper()
	repo, linked := regressionLinkedRepo(t)
	linked = resolvePath(linked)
	write(t, filepath.Join(linked, sharedRule), strings.Repeat("linked shared instruction\n", 16))
	plannedSettingsWrite(t, filepath.Join(repo, rel), body)
	write(t, filepath.Join(repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+rel+"\n")
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", linked))
	return repo, linked
}

func TestPlannedSettingsRejectBeforeWriting(t *testing.T) {
	for _, tc := range []struct{ name, agent, rel, body, reason string }{
		{"budget", "codex", ".codex/config.toml", "project_doc_max_bytes = 100\n", "over Codex project_doc_max_bytes"},
		{"discovery", "codex", ".codex/config.toml", "project_root_markers = []\n", "project_root_markers"},
		{"fallback names", "codex", ".codex/config.toml", "project_doc_fallback_filenames = ['OTHER.md']\n", "project_doc_fallback_filenames"},
		{"malformed TOML", "codex", ".codex/config.toml", "[invalid", "could not parse"},
		{"invalid feature", "codex", ".codex/config.toml", "[features]\nhooks = 'invalid'\n", "features.hooks"},
		{"invalid UTF-8", "codex", ".codex/config.toml", "\xff", "non-UTF-8 settings"},
		{"Codex JSON instruction hook", "codex", ".codex/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"quota-cli agent instructions _hook --agent=codex --event=SessionStart"}]}]}}`, "conflicts with native instruction delivery"},
		{"Codex TOML instruction hook", "codex", ".codex/config.toml", "[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = 'quota-cli agent instructions _hook --agent=codex --event=SessionStart'\n", "conflicts with native instruction delivery"},
		{"Codex malformed hooks JSON", "codex", ".codex/hooks.json", `{} {}`, "trailing content"},
		{"Claude hooks", "claude", ".claude/settings.local.json", `{"disableAllHooks":true}`, "disableAllHooks is true"},
		{"Claude JSON", "claude", ".claude/settings.local.json", `{} {}`, "trailing content"},
		{"all agents", "all", ".codex/config.toml", "project_doc_max_bytes = 100\n", "over Codex project_doc_max_bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, linked := plannedSettingsFixture(t, tc.rel, tc.body)
			beforeIgnore := regressionRead(t, filepath.Join(repo, ".git", "info", "exclude"))
			for _, operation := range []string{"plan", "setup"} {
				var err error
				if operation == "plan" {
					var paths []string
					paths, err = PlanRepositoryWithPolicy(context.Background(), repo, tc.agent, "checkout", tc.rel)
					if len(paths) != 0 {
						t.Fatalf("rejected plan returned paths: %v", paths)
					}
				} else {
					err = SetupRepository(context.Background(), repo, tc.agent, "checkout", tc.rel)
				}
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("%s: expected %q, got %v", operation, tc.reason, err)
				}
				for _, path := range []string{filepath.Join(linked, tc.rel), filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(repo, codexRule), filepath.Join(repo, ".git", "quota-instructions.json")} {
					if exists(path) {
						t.Fatalf("%s wrote %s before rejection", operation, path)
					}
				}
				if got := regressionRead(t, filepath.Join(repo, tc.rel)); got != tc.body {
					t.Fatalf("%s changed source: %q", operation, got)
				}
				if got := regressionRead(t, filepath.Join(repo, ".git", "info", "exclude")); got != beforeIgnore {
					t.Fatalf("%s changed ignore rules", operation)
				}
			}
		})
	}
}

func TestPlannedSettingsClaudeCopiedExclusion(t *testing.T) {
	const rel = ".claude/settings.json"
	repo, linked := plannedSettingsFixture(t, rel, `{}`)
	body := fmt.Sprintf(`{"claudeMdExcludes":[%q]}`, filepath.ToSlash(filepath.Join(linked, sharedBridge)))
	write(t, filepath.Join(repo, rel), body)
	if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "claude", "checkout", rel); err == nil || !strings.Contains(err.Error(), "claudeMdExcludes matches") {
		t.Fatalf("copied exclusion accepted: %v", err)
	}
	if err := SetupRepository(context.Background(), repo, "claude", "checkout", rel); err == nil || !strings.Contains(err.Error(), "claudeMdExcludes matches") {
		t.Fatalf("copied exclusion setup accepted: %v", err)
	}
	if exists(filepath.Join(linked, rel)) || exists(filepath.Join(linked, localBridge)) {
		t.Fatal("rejected exclusion wrote generated files")
	}
}

func TestPlannedCodexHooksDoNotTreatHardlinksAsAccountMigration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := agenthooks.ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"})
	body := fmt.Sprintf(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q}]}]}}`, command)
	repo, _ := plannedSettingsFixture(t, ".codex/hooks.json", body)
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", repo))
	if err := os.Link(filepath.Join(repo, ".codex", "hooks.json"), filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "codex", "checkout"); err == nil || !strings.Contains(err.Error(), "conflicts with native instruction delivery") {
		t.Fatalf("separate hardlink treated as an account settings replacement: %v", err)
	}
	if regressionRead(t, filepath.Join(repo, ".codex", "hooks.json")) != body {
		t.Fatal("project hook was changed")
	}
}

func TestRepositoryPreparationRequiresAccountHookRemovalPlan(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared rules\n")
	write(t, filepath.Join(repo, localRule), "local rules\n")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := agenthooks.ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"})
	accountHooks := filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json")
	write(t, accountHooks, fmt.Sprintf(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q}]}]}}`, command))
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", repo))
	if err := os.Mkdir(filepath.Join(repo, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(accountHooks, filepath.Join(repo, ".codex", "hooks.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "codex", "checkout"); err == nil || !strings.Contains(err.Error(), "conflicts with native instruction delivery") {
		t.Fatalf("preparation accepted an account hook without a removal plan: %v", err)
	}
	if err := SetupRepository(context.Background(), repo, "codex", "checkout"); err == nil || !strings.Contains(err.Error(), "conflicts with native instruction delivery") {
		t.Fatalf("repository setup accepted an account hook without a removal plan: %v", err)
	}
	if exists(filepath.Join(repo, codexRule)) || exists(filepath.Join(repo, ".git", "quota-instructions.json")) {
		t.Fatal("rejected preparation changed managed files")
	}
}

func TestPlannedSettingsRefreshUsesFutureBody(t *testing.T) {
	for _, tc := range []struct{ name, agent, rel, old, next string }{
		{"Codex budget", "codex", ".codex/config.toml", "project_doc_max_bytes = 100\n", "project_doc_max_bytes = 4096\n"},
		{"Codex JSON instruction hook", "codex", ".codex/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"quota-cli agent instructions _hook --agent=codex --event=SessionStart"}]}]}}`, `{"hooks":{"state":{"example":{"enabled":true,"trusted_hash":"example"}},"SessionStart":[{"hooks":[{"type":"command","command":"printf ready"}]}]}}`},
		{"Codex TOML instruction hook", "codex", ".codex/config.toml", "[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = 'quota-cli agent instructions _hook --agent=codex --event=SessionStart'\n", "[hooks.state.example]\nenabled = true\ntrusted_hash = 'example'\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = 'printf ready'\n"},
		{"Claude hooks", "claude", ".claude/settings.local.json", `{"disableAllHooks":true}`, `{"disableAllHooks":false}`},
		{"Claude exclusion", "claude", ".claude/settings.json", `{"claudeMdExcludes":["**/CLAUDE.md"]}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, linked := plannedSettingsFixture(t, tc.rel, tc.old)
			otherAgent := "codex"
			if tc.agent == "codex" {
				otherAgent = "claude"
			}
			if err := SetupRepository(context.Background(), repo, otherAgent, "checkout", tc.rel); err != nil {
				t.Fatalf("unselected agent settings blocked copy: %v", err)
			}
			write(t, filepath.Join(repo, tc.rel), tc.next)
			if _, err := PlanRepositoryWithPolicy(context.Background(), repo, tc.agent, ""); err != nil {
				t.Fatalf("future valid settings rejected: %v", err)
			}
			if got := regressionRead(t, filepath.Join(linked, tc.rel)); got != tc.old {
				t.Fatalf("plan wrote new settings: %q", got)
			}
			if err := SetupRepository(context.Background(), repo, tc.agent, ""); err != nil {
				t.Fatal(err)
			}
			if got := regressionRead(t, filepath.Join(linked, tc.rel)); got != tc.next {
				t.Fatalf("settings not refreshed: %q", got)
			}
		})
	}
}

func TestPlannedSettingsRejectRefreshAndPreserveUserEdits(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprint(edited), func(t *testing.T) {
			const rel = ".codex/config.toml"
			repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = 4096\n")
			if err := SetupRepository(context.Background(), repo, "codex", "checkout", rel); err != nil {
				t.Fatal(err)
			}
			if edited {
				write(t, filepath.Join(linked, rel), "project_doc_max_bytes = 8192\n")
			}
			before := map[string]string{}
			for _, path := range []string{filepath.Join(linked, rel), filepath.Join(linked, localRule), filepath.Join(linked, codexRule), filepath.Join(repo, codexRule), filepath.Join(repo, ".git", "quota-instructions.json")} {
				before[path] = regressionRead(t, path)
			}
			write(t, filepath.Join(repo, rel), "project_doc_max_bytes = 100\n")
			write(t, filepath.Join(repo, localRule), "updated local source\n")
			reason := "over Codex project_doc_max_bytes"
			if edited {
				reason = "user edits or unknown ownership"
			}
			if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "codex", ""); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("refresh plan: %v", err)
			}
			if err := SetupRepository(context.Background(), repo, "codex", ""); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("refresh setup: %v", err)
			}
			for path, body := range before {
				if got := regressionRead(t, path); got != body {
					t.Fatalf("rejected refresh changed %s", path)
				}
			}
			if regressionRead(t, filepath.Join(repo, rel)) != "project_doc_max_bytes = 100\n" || regressionRead(t, filepath.Join(repo, localRule)) != "updated local source\n" {
				t.Fatal("rejected refresh changed sources")
			}
		})
	}
}

func TestPlannedSettingsCodexTrustScope(t *testing.T) {
	for _, trust := range []string{"", "untrusted"} {
		t.Run(trust, func(t *testing.T) {
			const rel = ".codex/config.toml"
			repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = 1\n")
			user := ""
			if trust != "" {
				user = fmt.Sprintf("[projects.%q]\ntrust_level = %q\n", linked, trust)
			}
			write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), user)
			_, err := PlanRepositoryWithPolicy(context.Background(), repo, "codex", "checkout", rel)
			if trust == "untrusted" {
				if err == nil || !strings.Contains(err.Error(), "is untrusted") {
					t.Fatalf("explicit distrust ignored: %v", err)
				}
			} else if err != nil {
				t.Fatalf("untrusted project layer evaluated without trust: %v", err)
			}
		})
	}
}

func TestPlannedSettingsWorktreeDifferentBase(t *testing.T) {
	const rel = ".codex/config.toml"
	repo, linked := plannedSettingsFixture(t, rel, "project_doc_max_bytes = 4096\n")
	git(t, linked, "add", sharedRule)
	seedCommit(t, linked)
	base := git(t, linked, "rev-parse", "HEAD")
	git(t, linked, "branch", "long-instructions", base)
	if err := SetupRepository(context.Background(), repo, "codex", "checkout", rel); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF", "long-instructions")
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	repoDir, err := worktreeRepoDir(r)
	if err != nil {
		t.Fatal(err)
	}
	target := resolvePath(filepath.Join(repoDir, "budget-check"))
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n[projects.%q]\ntrust_level = 'trusted'\n", repo, target))
	write(t, filepath.Join(repo, rel), "project_doc_max_bytes = 100\n")
	before := regressionRead(t, statePath(r))
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"budget-check"}`, "claude-worktree-create")
	if code != 1 || out != "" || !strings.Contains(errb, "over Codex project_doc_max_bytes") {
		t.Fatalf("create returned success or wrong failure: %d %q %q", code, out, errb)
	}
	if exists(filepath.Join(target, rel)) || exists(filepath.Join(target, codexRule)) || exists(filepath.Join(target, localRule)) {
		t.Fatal("rejected worktree contains generated copies")
	}
	if regressionRead(t, statePath(r)) != before || regressionRead(t, filepath.Join(repo, rel)) != "project_doc_max_bytes = 100\n" {
		t.Fatal("rejected worktree changed state or source")
	}
}

func TestPlannedSettingsUsesPlannedPrimarySharedSource(t *testing.T) {
	const rel = ".codex/config.toml"
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "example\n")
	git(t, repo, "add", "tracked.txt")
	seedCommit(t, repo)
	linked := resolvePath(filepath.Join(t.TempDir(), "linked"))
	git(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	write(t, filepath.Join(repo, sharedRule), strings.Repeat("shared instruction\n", 32))
	write(t, filepath.Join(repo, localRule), "local\n")
	write(t, filepath.Join(repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+rel+"\n")
	plannedSettingsWrite(t, filepath.Join(repo, rel), "project_doc_max_bytes = 4096\n")
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", linked))
	if err := SetupRepository(context.Background(), repo, "codex", "primary", rel); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, sharedRule), "short shared\n")
	write(t, filepath.Join(repo, rel), "project_doc_max_bytes = 100\n")
	before := regressionRead(t, filepath.Join(linked, sharedRule))
	if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "codex", ""); err != nil {
		t.Fatalf("planned shorter shared source rejected: %v", err)
	}
	if regressionRead(t, filepath.Join(linked, sharedRule)) != before {
		t.Fatal("plan changed shared copy")
	}
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	if regressionRead(t, filepath.Join(linked, codexRule)) != "short shared\n\nlocal\n" || regressionRead(t, filepath.Join(linked, rel)) != "project_doc_max_bytes = 100\n" {
		t.Fatal("planned shared source or settings not applied")
	}
	write(t, filepath.Join(repo, sharedRule), strings.Repeat("longer shared\n", 32))
	if err := SetupRepository(context.Background(), repo, "codex", ""); err == nil || !strings.Contains(err.Error(), "over Codex project_doc_max_bytes") {
		t.Fatalf("planned larger primary source accepted: %v", err)
	}
	if regressionRead(t, filepath.Join(linked, sharedRule)) != "short shared\n" {
		t.Fatal("failed shared refresh changed copy")
	}
}

func TestPlannedSettingsEmptyAndRemovedFilesDoNotReadDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "project_doc_max_bytes = 1\n")
	for _, file := range []plannedSettingsFile{{Data: []byte{}}, {Remove: true}} {
		planned := map[string]plannedSettingsFile{path: file}
		data, err := readSettingsFile(path, planned)
		if file.Remove {
			if !os.IsNotExist(err) || settingsFileExists(path, planned) {
				t.Fatalf("removed file read from disk: %q %v", data, err)
			}
		} else if err != nil || len(data) != 0 || !settingsFileExists(path, planned) {
			t.Fatalf("empty future body replaced with disk body: %q %v", data, err)
		}
	}
	if regressionRead(t, path) != "project_doc_max_bytes = 1\n" {
		t.Fatal("planned read changed disk file")
	}
}
