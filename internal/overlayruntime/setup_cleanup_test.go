package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRemovesOwnedPrimaryLocalBridgeWithoutSource(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	ctx := context.Background()
	if err := SetupRepository(ctx, repo, "all", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, localRule)); err != nil {
		t.Fatal(err)
	}
	before := snapshotInstructionFiles(t, repo, linked)
	if _, err := PlanRepositoryWithPolicy(ctx, repo, "all", ""); err != nil {
		t.Fatal(err)
	}
	assertInstructionSnapshot(t, before)
	for i := 0; i < 2; i++ {
		if err := SetupRepository(ctx, repo, "all", ""); err != nil {
			t.Fatal(err)
		}
		state, err := ReadRepositoryState(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Join(repo, localBridge), filepath.Join(repo, codexRule), filepath.Join(linked, localRule), filepath.Join(linked, localBridge), filepath.Join(linked, codexRule)} {
			if exists(path) || state.Generated[path] != "" {
				t.Fatalf("obsolete local instructions remain: %s", path)
			}
			if _, recorded := state.GeneratedModes[path]; recorded {
				t.Fatalf("obsolete mode remains: %s", path)
			}
		}
	}
}

func TestSetupPreflightRejectsNegatedWorktreeIgnores(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "refresh"}[configured], func(t *testing.T) {
			repo, linked := regressionLinkedRepo(t)
			if configured {
				if err := SetupRepository(context.Background(), repo, "all", ""); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(linked, ".gitignore"), "!AGENTS.override.md\n")
			write(t, filepath.Join(repo, localRule), "updated private instructions\n")
			before := snapshotInstructionFiles(t, repo, linked)
			if _, err := PlanRepositoryWithPolicy(context.Background(), repo, "all", ""); err == nil || !strings.Contains(err.Error(), "still not ignored") {
				t.Fatalf("plan accepted conflicting ignore: %v", err)
			}
			assertInstructionSnapshot(t, before)
			if err := SetupRepository(context.Background(), repo, "all", ""); err == nil || !strings.Contains(err.Error(), "still not ignored") {
				t.Fatalf("setup accepted conflicting ignore: %v", err)
			}
			assertInstructionSnapshot(t, before)
		})
	}
}

func TestSetupPreflightUsesPlannedIgnorePrecedence(t *testing.T) {
	for _, mode := range []string{"primary", "global", "common", "nested"} {
		t.Run(mode, func(t *testing.T) {
			repo, linked := regressionLinkedRepo(t)
			var extra []string
			switch mode {
			case "primary":
				write(t, filepath.Join(repo, ".gitignore"), "!AGENTS.override.md\n")
			case "global":
				path := filepath.Join(t.TempDir(), "ignore")
				write(t, path, "!AGENTS.override.md\n")
				git(t, repo, "config", "core.excludesFile", path)
			case "common":
				write(t, filepath.Join(repo, ".git", "info", "exclude"), "AGENTS.override.md\n!AGENTS.override.md\n")
			case "nested":
				extra = []string{"config/file.env"}
				for _, worktree := range []string{repo, linked} {
					if err := os.Mkdir(filepath.Join(worktree, "config"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				write(t, filepath.Join(repo, ".git", "info", "exclude"), "/config/file.env\n")
				write(t, filepath.Join(repo, "config", "file.env"), "private value\n")
				write(t, filepath.Join(linked, "config", ".gitignore"), "!file.env\n")
			}
			before := snapshotInstructionFiles(t, repo, linked)
			_, planErr := PlanRepositoryWithPolicy(context.Background(), repo, "all", "", extra...)
			assertInstructionSnapshot(t, before)
			setupErr := SetupRepository(context.Background(), repo, "all", "", extra...)
			if mode == "common" || mode == "nested" {
				if planErr == nil || setupErr == nil || !strings.Contains(planErr.Error(), "still not ignored") || !strings.Contains(setupErr.Error(), "still not ignored") {
					t.Fatalf("ineffective ignore accepted: plan=%v setup=%v", planErr, setupErr)
				}
				assertInstructionSnapshot(t, before)
			} else if planErr != nil || setupErr != nil {
				t.Fatalf("effective planned ignore rejected: plan=%v setup=%v", planErr, setupErr)
			}
		})
	}
}

func TestSetupReportsChangedFilesWhenStateSaveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permission refusal requires an unprivileged user")
	}
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	if err := SetupRepository(context.Background(), repo, "codex", ""); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(repo, ".git", "quota-instructions.json")
	before := regressionRead(t, statePath)
	gitDir := filepath.Join(repo, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(gitDir, info.Mode().Perm()); err != nil {
			t.Error(err)
		}
	})
	regressionChmod(t, gitDir, 0o500)
	write(t, filepath.Join(repo, localRule), "changed private\n")
	err = SetupRepository(context.Background(), repo, "codex", "")
	merged := filepath.Join(repo, codexRule)
	if err == nil || !strings.Contains(err.Error(), "changed: "+merged) || !strings.Contains(err.Error(), "quota-instructions.json") {
		t.Fatalf("partial write omitted from error: %v", err)
	}
	if regressionRead(t, statePath) != before || !strings.Contains(regressionRead(t, merged), "changed private") {
		t.Fatal("test did not reach the partial state-save failure")
	}
}

func TestSetupPreservesTrackedSharedSourceDeletion(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	path := filepath.Join(linked, sharedRule)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	before := snapshotInstructionFiles(t, repo, linked)
	for _, plan := range []bool{true, false} {
		var err error
		if plan {
			_, err = PlanRepositoryWithPolicy(context.Background(), repo, "all", "primary")
		} else {
			err = SetupRepository(context.Background(), repo, "all", "primary")
		}
		if err == nil || !strings.Contains(err.Error(), "tracked") {
			t.Fatalf("tracked deletion was not preserved: plan=%t err=%v", plan, err)
		}
		assertInstructionSnapshot(t, before)
	}
}

func TestSetupRejectsOversizedClaudeGeneratedCopy(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	write(t, filepath.Join(repo, localRule), strings.Repeat("x", maxRuleBytes))
	before := snapshotInstructionFiles(t, repo, linked)
	for _, plan := range []bool{true, false} {
		var err error
		if plan {
			_, err = PlanRepositoryWithPolicy(context.Background(), repo, "claude", "")
		} else {
			err = SetupRepository(context.Background(), repo, "claude", "")
		}
		if err == nil || !strings.Contains(err.Error(), "file size limit") {
			t.Fatalf("oversized generated copy accepted: plan=%t err=%v", plan, err)
		}
		assertInstructionSnapshot(t, before)
	}
	write(t, filepath.Join(repo, localRule), strings.Repeat("x", maxRuleBytes-len(generatedLocal(""))))
	if err := SetupRepository(context.Background(), repo, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if len(regressionRead(t, filepath.Join(linked, localBridge))) != maxRuleBytes {
		t.Fatal("exact-size generated copy was not preserved")
	}
}
