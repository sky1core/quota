package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstructionsWorktreePreservesSharedBridgeIgnorePolicy(t *testing.T) {
	for _, userIgnored := range []bool{false, true} {
		name := "public shared bridge"
		if userIgnored {
			name = "user ignored shared bridge"
		}
		t.Run(name, func(t *testing.T) {
			f := newNativeSetupFixture(t, true)
			setupNativeWorktree(t, f, "claude")
			if userIgnored {
				p := filepath.Join(f.repo, ".git", "info", "exclude")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				writeNativeSetupFile(t, p, string(b)+"\nCLAUDE.md\n")
			}
			check := func() {
				t.Helper()
				cmd := exec.Command("git", "check-ignore", "CLAUDE.md")
				cmd.Dir = f.repo
				out, err := cmd.CombinedOutput()
				if err != nil {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
						t.Fatalf("check-ignore: %v: %s", err, out)
					}
				}
				if (err == nil) != userIgnored {
					t.Fatalf("shared bridge ignored=%t, want=%t", err == nil, userIgnored)
				}
			}
			check()
			target := callInstructionsWorktreeHook(t, "WorktreeCreate", map[string]string{"cwd": f.repo, "name": "shared-ignore"}, 0)
			check()
			callInstructionsWorktreeHook(t, "WorktreeRemove", map[string]string{"worktree_path": target}, 0)
			check()
		})
	}
}

func TestInstructionsRepositoryUninstallDryRunChecksConflicts(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "all"} {
		for _, change := range []string{"none", "content", "mode"} {
			t.Run(agent+"/"+change, func(t *testing.T) {
				f := newNativeSetupFixture(t, true)
				setupNativeWorktree(t, f, agent)
				rel := "CLAUDE.local.md"
				if agent == "codex" {
					rel = "AGENTS.override.md"
				}
				path := filepath.Join(f.repo, rel)
				want := ""
				switch change {
				case "content":
					writeNativeSetupFile(t, path, "user changes\n")
					want = "preserved"
				case "mode":
					if err := os.Chmod(path, 0700); err != nil {
						t.Fatal(err)
					}
					want = "permissions changed"
				}
				before := f.snapshot(t)
				args := []string{"uninstall", f.repo, "--scope=repository", "--agent=" + agent}
				var dryOut, dryErr, applyOut, applyErr bytes.Buffer
				dryCode := runAgentInstructions(append(append([]string{}, args...), "--dry-run"), nil, &dryOut, &dryErr)
				if !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatal("uninstall dry-run changed repository or account files")
				}
				applyCode := runAgentInstructions(args, nil, &applyOut, &applyErr)
				wantCode := 0
				if change != "none" {
					wantCode = 1
				}
				if dryCode != wantCode || applyCode != wantCode || !strings.Contains(dryOut.String()+dryErr.String(), want) || !strings.Contains(applyOut.String()+applyErr.String(), want) {
					t.Fatalf("dry-run exit=%d output=%s%s; apply exit=%d output=%s%s; want exit=%d reason=%q", dryCode, &dryOut, &dryErr, applyCode, &applyOut, &applyErr, wantCode, want)
				}
				if strings.Contains(dryOut.String()+dryErr.String(), "repository disabled;") {
					t.Fatal("dry-run falsely reported a state change")
				}
			})
		}
	}
}

func TestInstructionsRepositoryUninstallDryRunWithoutSetup(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	before := f.snapshot(t)
	var stdout, stderr bytes.Buffer
	if code := runAgentInstructions([]string{"uninstall", f.repo, "--scope=repository", "--agent=all", "--dry-run"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("dry-run exit=%d: %s%s", code, &stdout, &stderr)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("uninstall dry-run changed an unconfigured repository")
	}
}
