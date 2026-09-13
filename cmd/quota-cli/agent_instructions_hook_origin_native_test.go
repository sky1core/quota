package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/agentinstructions"
	"github.com/sky1core/quota/internal/overlayruntime"
)

func TestInstructionsNativeHookOrigins(t *testing.T) {
	f := newNativeSetupFixture(t, true)
	f.wrapper(t, "features.hooks=true", "")
	writeNativeSetupFile(t, filepath.Join(f.account, "hooks.json"), "{}")
	body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo example"}]}]}}`
	for _, root := range []string{f.repo, f.linked} {
		for _, rel := range []string{"", "child"} {
			writeNativeSetupFile(t, filepath.Join(root, rel, ".codex", "config.toml"), "")
			writeNativeSetupFile(t, filepath.Join(root, rel, ".codex", "hooks.json"), body)
		}
	}
	for _, directory := range []string{f.repo, f.linked, filepath.Join(f.linked, "child")} {
		report, err := agentinstructions.InspectNativeCodex(context.Background(), directory, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{filepath.Join(f.repo, ".codex", "hooks.json")}
		if directory == filepath.Join(f.linked, "child") {
			want = append(want, filepath.Join(f.repo, "child", ".codex", "hooks.json"))
		}
		assertNativeHookPaths(t, report, want)
	}
	linkedConfig := filepath.Join(f.linked, ".codex", "config.toml")
	writeNativeSetupFile(t, linkedConfig, "[[hooks.SubagentStart]]\n[[hooks.SubagentStart.hooks]]\ntype='command'\ncommand='echo inline-example'\n")
	inline, err := agentinstructions.InspectNativeCodex(context.Background(), f.linked, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeHookPaths(t, inline, []string{filepath.Join(f.repo, ".codex", "hooks.json")})
	primaryConfig := filepath.Join(f.repo, ".codex", "config.toml")
	writeNativeSetupFile(t, primaryConfig, "[[hooks.SubagentStart]]\n[[hooks.SubagentStart.hooks]]\ntype='command'\ncommand='echo primary-inline-example'\n")
	inline, err = agentinstructions.InspectNativeCodex(context.Background(), f.linked, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeHookPaths(t, inline, []string{primaryConfig, filepath.Join(f.repo, ".codex", "hooks.json")})
	bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
	nativeWorktreeGit(t, f.repo, "clone", "--bare", "-q", f.repo, bare)
	checkout := filepath.Join(filepath.Dir(f.repo), "bare-checkout")
	nativeWorktreeGit(t, bare, "worktree", "add", "-q", "--detach", checkout, "HEAD")
	for _, root := range []string{bare, checkout} {
		writeNativeSetupFile(t, filepath.Join(root, ".codex", "config.toml"), "")
		writeNativeSetupFile(t, filepath.Join(root, ".codex", "hooks.json"), body)
	}
	writeNativeSetupFile(t, filepath.Join(f.bin, "codex"), "#!/bin/sh\nexec "+nativeSetupQuote(f.codex)+" -c features.hooks=true -c \"projects.$PWD.trust_level='trusted'\" \"$@\"\n")
	report, err := agentinstructions.InspectNativeCodex(context.Background(), checkout, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeHookPaths(t, report, []string{filepath.Join(checkout, ".codex", "hooks.json")})
}

func assertNativeHookPaths(t *testing.T, report agentinstructions.NativeReport, want []string) {
	t.Helper()
	var got []string
	for _, hook := range report.Hooks {
		if hook.Command != "" {
			t.Fatal("native report exposed an unmatched command")
		}
		got = append(got, hook.SourcePath)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native hook origins = %v, want %v", got, want)
	}
}

func trustNativeHookFixture(t *testing.T, f nativeSetupFixture) {
	t.Helper()
	writeNativeSetupFile(t, filepath.Join(f.bin, "codex"), "#!/bin/sh\nexec "+nativeSetupQuote(f.codex)+" -c project_doc_max_bytes=65536 -c features.hooks=true -c \"projects.$PWD.trust_level='trusted'\" \"$@\"\n")
}

func TestInstructionsNativeWorktreeHookOrigin(t *testing.T) {
	for _, format := range []string{"json", "toml"} {
		for _, mode := range []string{"inherited conflict", "ignored linked hook", "no target layer"} {
			t.Run(format+" "+mode, func(t *testing.T) {
				f := newNativeSetupFixture(t, true)
				hooks, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
				if err != nil {
					t.Fatal(err)
				}
				hookFile := "hooks.json"
				if format == "toml" {
					hookFile = "config.toml"
					hooks = nativeOwnedHookTOML(t)
				}
				trustNativeHookFixture(t, f)
				setupNativeWorktree(t, f, "all")
				if mode != "no target layer" {
					writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "config.toml"), "")
					nativeWorktreeGit(t, f.repo, "add", "-f", ".codex/config.toml")
					if mode == "ignored linked hook" {
						writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", hookFile), string(hooks))
						nativeWorktreeGit(t, f.repo, "add", ".codex/"+hookFile)
					}
					nativeWorktreeGit(t, f.repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "fixture native hook files")
				}
				primaryHooks := string(hooks)
				if mode == "ignored linked hook" {
					primaryHooks = nativeCommandHookBody(format, "echo unrelated-private-example")
				}
				writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", hookFile), primaryHooks)
				before := nativeWorktreeExistingSnapshot(t, f, true)
				worktrees := nativeWorktreeGit(t, f.repo, "worktree", "list", "--porcelain")
				branches := nativeWorktreeGit(t, f.repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
				want := 0
				if mode == "inherited conflict" {
					want = 1
				}
				result := callInstructionsWorktreeHook(t, "WorktreeCreate", map[string]string{"cwd": f.repo, "name": "hook-origin"}, want)
				if want != 0 {
					if !strings.Contains(result, filepath.Join(f.repo, ".codex", hookFile)) || !strings.Contains(result, "conflicts with native instruction delivery") || strings.Contains(result, "rollback incomplete") {
						t.Fatalf("wrong inherited hook refusal: %s", result)
					}
					if !reflect.DeepEqual(before, nativeWorktreeExistingSnapshot(t, f, true)) || worktrees != nativeWorktreeGit(t, f.repo, "worktree", "list", "--porcelain") || branches != nativeWorktreeGit(t, f.repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads") {
						t.Fatal("inherited hook rejection changed existing files or left a worktree/branch")
					}
					matches, err := filepath.Glob(filepath.Join(f.home, "worktrees", "*", "hook-origin"))
					if err != nil || len(matches) != 0 {
						t.Fatalf("rejection left target files: %v %v", matches, err)
					}
					return
				}
				native, err := agentinstructions.InspectNativeCodex(context.Background(), result, nil)
				if err != nil || native.State != "configured" {
					t.Fatalf("created worktree native inspection: %v %+v", err, native)
				}
				if mode == "ignored linked hook" {
					assertNativeHookPaths(t, native, []string{filepath.Join(f.repo, ".codex", hookFile)})
					body, err := os.ReadFile(filepath.Join(result, ".codex", hookFile))
					if err != nil || !bytes.Equal(body, hooks) {
						t.Fatalf("ignored linked hook was changed: %v", err)
					}
				} else if len(native.Hooks) != 0 {
					t.Fatal("absent project layer unexpectedly inherited hooks")
				}
				callInstructionsWorktreeHook(t, "WorktreeRemove", map[string]string{"worktree_path": result}, 0)
			})
		}
	}
}

func TestInstructionsNativePlannedHookOrigin(t *testing.T) {
	for _, format := range []string{"json", "toml"} {
		for _, tc := range []struct{ current, future string }{
			{"empty", "conflict"}, {"unrelated", "conflict"}, {"conflict", "unrelated"}, {"empty", "unrelated"},
		} {
			t.Run(format+" "+tc.current+" to "+tc.future, func(t *testing.T) {
				f := newNativeSetupFixture(t, false)
				hooks, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
				if err != nil {
					t.Fatal(err)
				}
				nativeWorktreeGit(t, f.repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
				bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
				nativeWorktreeGit(t, f.repo, "clone", "--bare", "-q", f.repo, bare)
				f.repo, f.linked = bare, filepath.Join(filepath.Dir(f.repo), "checkout")
				nativeWorktreeGit(t, bare, "worktree", "add", "-q", "--detach", f.linked, "HEAD")
				writeNativeSetupFile(t, filepath.Join(bare, "AGENTS.md"), "shared\n")
				writeNativeSetupFile(t, filepath.Join(bare, "AGENTS.local.md"), "private\n")
				hookFile := "hooks.json"
				if format == "toml" {
					hookFile = "config.toml"
					hooks = nativeOwnedHookTOML(t)
				}
				rel := "hook-files/" + hookFile
				writeNativeSetupFile(t, filepath.Join(bare, "info", "exclude"), "/"+rel+"\n")
				current := "{}"
				if format == "toml" {
					current = ""
				}
				if tc.current == "unrelated" {
					current = nativeCommandHookBody(format, "echo unrelated-private-current")
				} else if tc.current == "conflict" {
					current = string(hooks)
				}
				writeNativeSetupFile(t, filepath.Join(bare, rel), current)
				if err := overlayruntime.SetupRepository(context.Background(), f.linked, "claude", "primary", rel); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(f.linked, ".codex"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../"+rel, filepath.Join(f.linked, ".codex", hookFile)); err != nil {
					t.Fatal(err)
				}
				trustNativeHookFixture(t, f)
				native, err := agentinstructions.InspectNativeCodex(context.Background(), f.linked, nil)
				if err != nil {
					t.Fatal(err)
				}
				projectHook := false
				for _, hook := range native.Hooks {
					if hook.Source == "project" {
						projectHook = true
					}
				}
				if projectHook != (tc.current != "empty") {
					t.Fatal("native discovery did not reflect the current hook source")
				}
				future := string(hooks)
				want := 1
				if tc.future == "unrelated" {
					future = nativeCommandHookBody(format, "echo unrelated-private-future")
					want = 0
				}
				writeNativeSetupFile(t, filepath.Join(bare, rel), future)
				before := f.snapshot(t)
				report := callNativeSetup(t, f.linked, want, "--dry-run")
				if want != 0 && !strings.Contains(report.Error, "conflicts with native instruction delivery") {
					t.Fatalf("future hook was not checked: %+v", report)
				}
				if !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatal("planned hook dry-run changed files")
				}
				callNativeSetup(t, f.linked, want)
				if want != 0 {
					if !reflect.DeepEqual(before, f.snapshot(t)) {
						t.Fatal("rejected future hook changed files")
					}
				} else {
					body, err := os.ReadFile(filepath.Join(f.linked, ".codex", hookFile))
					if err != nil || string(body) != future {
						t.Fatalf("checked future body was not applied: %q %v", body, err)
					}
				}
			})
		}
	}
}

func TestInstructionsNativeHookRemovalPreservesUnrelatedCommands(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	trustNativeHookFixture(t, f)
	path := filepath.Join(f.account, "hooks.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var hooks map[string]any
	if err := json.Unmarshal(body, &hooks); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.home, "hook-must-not-run")
	command := "touch " + nativeSetupQuote(marker)
	group := hooks["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)
	group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": command})
	body, err = json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	writeNativeSetupFile(t, path, string(body))
	before := f.snapshot(t)
	callNativeSetup(t, f.repo, 0, "--dry-run")
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("hook migration dry-run changed files")
	}
	callNativeSetup(t, f.repo, 0)
	body, err = os.ReadFile(path)
	if err != nil || bytes.Contains(body, []byte("instructions _hook")) || !bytes.Contains(body, []byte(command)) {
		t.Fatal("migration did not preserve the unrelated command while removing owned hooks")
	}
	native, err := agentinstructions.InspectNativeCodex(context.Background(), f.repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeHookPaths(t, native, []string{path})
	body, err = json.Marshal(native)
	if err != nil || bytes.Contains(body, []byte(marker)) {
		t.Fatal("native report exposed an unrelated command")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("inspection executed a hook: %v", err)
	}
	before = f.snapshot(t)
	callNativeSetup(t, f.repo, 0)
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("repeated setup changed migrated files")
	}
}

func TestInstructionsNativeNewHookLayerRefusedBeforeWrites(t *testing.T) {
	f := newNativeSetupFixture(t, true)
	trustNativeHookFixture(t, f)
	const rel = ".codex/hooks.json"
	writeNativeSetupFile(t, filepath.Join(f.repo, rel), "{}")
	writeNativeSetupFile(t, filepath.Join(f.repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+rel+"\n")
	before := f.snapshot(t)
	for _, options := range [][]string{{"--local-file=" + rel, "--dry-run"}, {"--local-file=" + rel}} {
		report := callNativeSetup(t, f.repo, 1, options...)
		if !strings.Contains(report.Error, "creates a project layer absent from native discovery") || !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatalf("unknown future hook origin was not refused before writes: %+v", report)
		}
	}
}

func nativeOwnedHookTOML(t *testing.T) []byte {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := agenthooks.ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"})
	return []byte(nativeCommandHookBody("toml", command))
}

func nativeCommandHookBody(format, command string) string {
	if format == "toml" {
		return fmt.Sprintf("[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype='command'\ncommand=%q\n", command)
	}
	return fmt.Sprintf(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q}]}]}}`, command)
}
