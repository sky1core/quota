package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/overlayruntime"
)

func runSharedCleanupSetup(t *testing.T, f nativeSetupFixture, policy string, dry bool, wantCode int) string {
	t.Helper()
	args := []string{"setup", f.repo, "--agent=claude"}
	if policy != "" {
		args = append(args, "--shared-source="+policy)
	}
	if dry {
		args = append(args, "--dry-run")
	}
	var out, errOut bytes.Buffer
	code := runAgentInstructions(args, nil, &out, &errOut)
	if code != wantCode {
		t.Fatalf("setup dry=%t policy=%q exit=%d want=%d: %s%s", dry, policy, code, wantCode, &out, &errOut)
	}
	return out.String() + errOut.String()
}

func assertSharedBridgeNotIgnored(t *testing.T, repo string) {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "CLAUDE.md")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("public bridge ignore policy changed: %v: %s", err, out)
	}
}

func TestInstructionsSetupCleansObsoleteSharedBridge(t *testing.T) {
	for _, policy := range []string{"", "checkout", "primary"} {
		t.Run("policy="+policy, func(t *testing.T) {
			f := newNativeSetupFixture(t, false)
			runSharedCleanupSetup(t, f, policy, false, 0)
			assertSharedBridgeNotIgnored(t, f.repo)
			bridge := filepath.Join(f.repo, "CLAUDE.md")
			state, err := overlayruntime.ReadRepositoryState(context.Background(), f.repo)
			if err != nil {
				t.Fatal(err)
			}
			if state.Generated[bridge] == "" {
				t.Fatal("setup did not record shared bridge ownership")
			}
			if err := os.Rename(filepath.Join(f.repo, "AGENTS.md"), filepath.Join(f.repo, "original-shared.md")); err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			out := runSharedCleanupSetup(t, f, "", true, 0)
			if !strings.Contains(out, "remove native shared bridge: "+bridge) {
				t.Fatalf("shared bridge removal missing from plan: %s", out)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("dry-run changed repository or account files")
			}
			runSharedCleanupSetup(t, f, "", false, 0)
			if _, err := os.Lstat(bridge); !os.IsNotExist(err) {
				t.Fatalf("obsolete shared bridge remains: %v", err)
			}
			state, err = overlayruntime.ReadRepositoryState(context.Background(), f.repo)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := state.Generated[bridge]; ok {
				t.Fatal("removed bridge ownership remains")
			}
			if _, ok := state.GeneratedModes[bridge]; ok {
				t.Fatal("removed bridge mode remains")
			}
			after := f.snapshot(t)
			for _, rel := range []string{"original-shared.md", "AGENTS.local.md", "CLAUDE.local.md", ".gitignore", ".git/info/exclude"} {
				path := filepath.Join(f.repo, rel)
				if before[path] == "" || before[path] != after[path] {
					t.Fatalf("unrelated source, bridge or ignore file changed: %s", rel)
				}
			}
			assertSharedBridgeNotIgnored(t, f.repo)
			runSharedCleanupSetup(t, f, "", false, 0)
			if !reflect.DeepEqual(after, f.snapshot(t)) {
				t.Fatal("repeated setup rewrote repository or account files")
			}
		})
	}
}

func TestInstructionsSetupPreservesObsoleteSharedBridgeConflicts(t *testing.T) {
	for _, policy := range []string{"checkout", "primary"} {
		for _, change := range []string{"user file", "content", "mode", "unknown ownership", "tracked"} {
			t.Run(policy+"/"+change, func(t *testing.T) {
				f := newNativeSetupFixture(t, false)
				bridge := filepath.Join(f.repo, "CLAUDE.md")
				if change == "user file" {
					writeNativeSetupFile(t, bridge, "@AGENTS.md\n")
				}
				runSharedCleanupSetup(t, f, policy, false, 0)
				switch change {
				case "content":
					writeNativeSetupFile(t, bridge, "@AGENTS.md\nuser changes\n")
				case "mode":
					info, err := os.Stat(bridge)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(bridge, info.Mode().Perm()^0o100); err != nil {
						t.Fatal(err)
					}
				case "unknown ownership":
					if err := overlayruntime.UpdateRepositoryState(context.Background(), f.repo, func(state *overlayruntime.RepositoryState) error {
						delete(state.Generated, bridge)
						delete(state.GeneratedModes, bridge)
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				case "tracked":
					nativeWorktreeGit(t, f.repo, "add", "CLAUDE.md")
				}
				if err := os.Rename(filepath.Join(f.repo, "AGENTS.md"), filepath.Join(f.repo, "original-shared.md")); err != nil {
					t.Fatal(err)
				}
				before := f.snapshot(t)
				wantReason := "unknown ownership"
				switch change {
				case "content":
					wantReason = "user edits"
				case "mode":
					wantReason = "permissions changed"
				case "tracked":
					wantReason = "is tracked"
				}
				for _, dry := range []bool{true, false} {
					out := runSharedCleanupSetup(t, f, "", dry, 1)
					if !strings.Contains(out, wantReason) {
						t.Fatalf("conflict reason %q missing: %s", wantReason, out)
					}
					if !reflect.DeepEqual(before, f.snapshot(t)) {
						t.Fatalf("conflicting setup dry=%t changed repository or account files", dry)
					}
				}
			})
		}
	}
}

func TestInstructionsSharedCleanupUsesPlannedSettings(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	runSharedCleanupSetup(t, f, "", false, 0)
	writeNativeSetupFile(t, filepath.Join(f.repo, ".claude", "settings.local.json"), `{"claudeMdExcludes":["**/CLAUDE.md"]}`)
	if err := os.Rename(filepath.Join(f.repo, "AGENTS.md"), filepath.Join(f.repo, "original-shared.md")); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	runSharedCleanupSetup(t, f, "", true, 0)
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("planned settings check changed repository or account files")
	}
	runSharedCleanupSetup(t, f, "", false, 0)
	if _, err := os.Lstat(filepath.Join(f.repo, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("obsolete shared bridge remains: %v", err)
	}
}

func TestInstructionsLocalOnlyPreservesUserSharedInstructions(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	if err := os.Rename(filepath.Join(f.repo, "AGENTS.md"), filepath.Join(f.repo, "original-shared.md")); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(f.repo, "CLAUDE.md")
	writeNativeSetupFile(t, bridge, "User instructions independent of AGENTS.md.\n")
	before := f.snapshot(t)
	runSharedCleanupSetup(t, f, "checkout", true, 0)
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("dry-run changed repository or account files")
	}
	runSharedCleanupSetup(t, f, "checkout", false, 0)
	if before[bridge] != f.snapshot(t)[bridge] {
		t.Fatal("user shared instructions changed")
	}
}
