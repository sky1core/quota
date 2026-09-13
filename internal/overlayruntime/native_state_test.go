package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryDisabledHooksStayDisabled(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	if err := UninstallRepository(context.Background(), repo, "all"); err == nil || !strings.Contains(err.Error(), "tracked native bridge") {
		t.Fatalf("tracked bridge preservation not reported: %v", err)
	}
	for _, dir := range []string{repo, linked} {
		for _, policy := range []string{"claude-session", "codex-session", "codex-subagent"} {
			event, input := "SessionStart", `{"cwd":`+quote(dir)+`}`
			if policy == "codex-session" {
				input = `{"cwd":` + quote(dir) + `,"source":"startup"}`
			}
			if policy == "codex-subagent" {
				event = "SubagentStart"
			}
			code, out, errb := run(t, input, "json", event, "x", "y", dir, policy)
			if code != 0 || out != "" {
				t.Fatalf("disabled hook: %d %s %s", code, out, errb)
			}
		}
	}
	if exists(filepath.Join(linked, localBridge)) {
		t.Fatal("disabled hook recreated local copy")
	}
	if err := SetupRepository(context.Background(), repo, "claude", ""); err != nil {
		t.Fatal(err)
	}
	state, err := ReadRepositoryState(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if state.Disabled["claude"] || !state.Disabled["codex"] {
		t.Fatalf("wrong provider re-enabled: %+v", state)
	}
	if !exists(filepath.Join(linked, localBridge)) {
		t.Fatal("explicit setup did not restore generated copy")
	}
}
func TestDisabledWorktreeCreateStillReturnsOrdinaryCheckout(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "# shared\n")
	git(t, repo, "add", sharedRule)
	seedCommit(t, repo)
	if err := UpdateRepositoryState(context.Background(), repo, func(s *RepositoryState) error { s.Disabled["claude"] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, localRule), "\xffinvalid source must not be read")
	t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
	code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"disabled"}`, "claude-worktree-create")
	if code != 0 {
		t.Fatalf("disabled create: %d %s %s", code, out, errb)
	}
	target := strings.TrimSpace(out)
	if !exists(filepath.Join(target, sharedRule)) {
		t.Fatal("ordinary checkout missing")
	}
	if exists(filepath.Join(target, localBridge)) {
		t.Fatal("disabled create copied private rules")
	}
	if code, out, errb = run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove"); code != 0 {
		t.Fatalf("disabled remove: %d %s %s", code, out, errb)
	}
}
func TestMalformedRepositoryStateFailsBeforeDelivery(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "# shared\n")
	write(t, filepath.Join(repo, localRule), "private sentinel\n")
	p := filepath.Join(repo, ".git", "quota-instructions.json")
	for _, body := range []string{`{`, `null`, `{"disabled":{},"shared_source":"guess"}`, `{"disabled":{},"shared_source":"checkout"} null`, `{"disabled":{"other":true},"shared_source":"checkout"}`} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"source":"startup"}`, "json", "SessionStart", "x", "y", repo, "codex-session")
		if code != 1 || strings.Contains(out+errb, "private sentinel") {
			t.Fatalf("unsafe state delivered: %d %s %s", code, out, errb)
		}
	}
}
func TestCodexNativeMergeIncludesBothSources(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared sentinel\n")
	write(t, filepath.Join(repo, localRule), "private sentinel\n")
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	body := regressionRead(t, filepath.Join(repo, codexRule))
	if strings.Count(body, "shared sentinel") != 1 || strings.Count(body, "private sentinel") != 1 {
		t.Fatalf("native merge: %q", body)
	}
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		policy := "codex-session"
		if event == "SubagentStart" {
			policy = "codex-subagent"
		}
		code, out, errb := run(t, `{"cwd":`+quote(repo)+`}`, "json", event, "x", "y", repo, policy)
		if code != 0 || out != "" {
			t.Fatalf("duplicate channel: %d %q %q", code, out, errb)
		}
	}
}
func TestPrimarySharedSourcePolicyAndUserEdits(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.txt"), "tracked\n")
	git(t, repo, "add", "tracked.txt")
	seedCommit(t, repo)
	write(t, filepath.Join(repo, sharedRule), "primary first\n")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	if _, err := PlanRepositoryWithPolicy(context.Background(), linked, "codex", "primary"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(linked, sharedRule)) {
		t.Fatal("dry-run created shared source")
	}
	if err := SetupRepository(context.Background(), linked, "codex", "primary"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(linked, sharedRule)
	if regressionRead(t, target) != "primary first\n" {
		t.Fatal("primary source not copied")
	}
	write(t, filepath.Join(repo, sharedRule), "primary second\n")
	if err := SetupRepository(context.Background(), linked, "codex", ""); err != nil {
		t.Fatal(err)
	}
	if regressionRead(t, target) != "primary second\n" {
		t.Fatal("primary source not refreshed")
	}
	write(t, target, "user edits\n")
	if err := SetupRepository(context.Background(), linked, "codex", ""); err == nil {
		t.Fatal("overwrote edited primary copy")
	}
	if regressionRead(t, target) != "user edits\n" {
		t.Fatal("user edits lost")
	}
}
func TestRepositoryStateConcurrentUpdates(t *testing.T) {
	repo := newRepo(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, agent := range []string{"claude", "codex"} {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			errs <- UpdateRepositoryState(context.Background(), repo, func(s *RepositoryState) error { s.Disabled[agent] = true; return nil })
		}(agent)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := ReadRepositoryState(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Disabled["claude"] || !s.Disabled["codex"] {
		t.Fatalf("concurrent update lost: %+v", s)
	}
}
func TestUninstallPreservesExistingUserBridge(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "# shared\n")
	write(t, filepath.Join(repo, sharedBridge), "@AGENTS.md\n")
	if err := SetupRepository(context.Background(), repo, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if err := UninstallRepository(context.Background(), repo, "claude"); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("ownership not reported: %v", err)
	}
	if regressionRead(t, filepath.Join(repo, sharedBridge)) != "@AGENTS.md\n" {
		t.Fatal("user bridge removed")
	}
}

func TestWorktreeSharedSourcePolicy(t *testing.T) {
	for _, policy := range []string{"checkout", "primary"} {
		t.Run(policy, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "tracked.txt"), "tracked\n")
			git(t, repo, "add", "tracked.txt")
			seedCommit(t, repo)
			write(t, filepath.Join(repo, sharedRule), "shared primary\n")
			if err := SetupRepository(context.Background(), repo, "claude", policy); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR", t.TempDir())
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"name":"shared-policy"}`, "claude-worktree-create")
			if policy == "checkout" {
				if code != 1 || out != "" || !strings.Contains(errb, "has no AGENTS.md") {
					t.Fatalf("checkout must refuse absent source: %d %s %s", code, out, errb)
				}
				if refs := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/agents-overlay/shared-policy"); refs != "" {
					t.Fatalf("failed create left branch: %s", refs)
				}
				return
			}
			if code != 0 {
				t.Fatalf("primary create: %d %s %s", code, out, errb)
			}
			target := strings.TrimSpace(out)
			if regressionRead(t, filepath.Join(target, sharedRule)) != "shared primary\n" {
				t.Fatal("primary source missing")
			}
			if regressionRead(t, filepath.Join(target, sharedBridge)) != "@AGENTS.md\n" {
				t.Fatal("bridge missing")
			}
			code, out, errb = run(t, `{"worktree_path":`+quote(target)+`}`, "claude-worktree-remove")
			if code != 0 {
				t.Fatalf("primary remove: %d %s %s", code, out, errb)
			}
			if !exists(filepath.Join(repo, sharedRule)) {
				t.Fatal("primary source removed")
			}
		})
	}
}
func TestPlanRejectsBlockedSettingsWithoutWrites(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "# shared\n")
	write(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), `{"disableAllHooks":true}`)
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	before := regressionRead(t, exclude)
	if _, err := PlanRepository(context.Background(), repo, "claude"); err == nil {
		t.Fatal("blocked settings accepted")
	}
	if err := SetupRepository(context.Background(), repo, "claude", ""); err == nil {
		t.Fatal("blocked setup accepted")
	}
	if exists(filepath.Join(repo, sharedBridge)) || exists(filepath.Join(repo, ".gitignore")) || exists(filepath.Join(repo, ".git", "quota-instructions.json")) {
		t.Fatal("blocked setup mutated repository")
	}
	if regressionRead(t, exclude) != before {
		t.Fatal("blocked setup changed ignore")
	}
}

func TestSetupRefusesIndependentWorktreeLocalSourceBeforeWrites(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	write(t, filepath.Join(linked, localRule), "independent private instructions\n")
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	before := regressionRead(t, exclude)
	for _, agent := range []string{"all", "claude", "codex"} {
		if _, err := PlanRepository(context.Background(), linked, agent); err == nil {
			t.Fatalf("%s plan accepted stray local source", agent)
		}
		if err := SetupRepository(context.Background(), linked, agent, ""); err == nil {
			t.Fatalf("%s setup accepted stray local source", agent)
		}
	}
	for _, p := range []string{filepath.Join(repo, ".gitignore"), filepath.Join(repo, localBridge), filepath.Join(linked, localBridge), filepath.Join(repo, ".git", "quota-instructions.json")} {
		if exists(p) {
			t.Fatalf("unsafe setup wrote %s", p)
		}
	}
	if regressionRead(t, exclude) != before {
		t.Fatal("unsafe setup changed ignore")
	}
	if regressionRead(t, filepath.Join(linked, localRule)) != "independent private instructions\n" {
		t.Fatal("stray source changed")
	}
}
func TestAtomicWritePreservesEditsSinceSourceRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.md")
	write(t, path, "old source\n")
	before, err := readRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, "external edit\n")
	if err := atomicWrite(path, []byte("replacement\n"), false, before); err == nil {
		t.Fatal("stale source snapshot overwrote external edit")
	}
	if regressionRead(t, path) != "external edit\n" {
		t.Fatal("external edit lost")
	}
}
