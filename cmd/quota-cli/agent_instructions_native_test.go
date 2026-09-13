package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/agentinstructions"
	"github.com/sky1core/quota/internal/overlayruntime"
)

type nativeSetupFixture struct {
	repo, linked, home, account, bin, codex string
}

func newNativeSetupFixture(t *testing.T, linked bool) nativeSetupFixture {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("real Codex CLI is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := nativeSetupFixture{repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), codex: codex}
	f.account = filepath.Join(f.home, ".codex")
	for _, path := range []string{f.repo, f.account, f.bin} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODEX_HOME", f.account)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.home, ".claude"))
	t.Setenv("PATH", f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	writeNativeSetupFile(t, filepath.Join(f.repo, "AGENTS.md"), "shared\n")
	writeNativeSetupFile(t, filepath.Join(f.repo, "AGENTS.local.md"), "private\n")
	writeNativeSetupFile(t, filepath.Join(f.repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/.codex/config.toml\n")
	if linked {
		git("-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
		f.linked = filepath.Join(root, "linked")
		git("worktree", "add", "-q", "--detach", f.linked, "HEAD")
		writeNativeSetupFile(t, filepath.Join(f.linked, "AGENTS.md"), "linked\n")
	}
	config := ""
	for _, repo := range []string{f.repo, f.linked} {
		if repo != "" {
			config += "[projects." + strconv.Quote(repo) + "]\ntrust_level = 'trusted'\n"
		}
	}
	writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), config)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hooks := map[string]any{}
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		command := agenthooks.ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=" + event})
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}
	}
	raw, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	writeNativeSetupFile(t, filepath.Join(f.account, "hooks.json"), string(raw))
	f.wrapper(t, "", "")
	return f
}

func nativeSetupQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func writeNativeSetupFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f nativeSetupFixture) wrapper(t *testing.T, setting, directory string) {
	t.Helper()
	command := "exec " + nativeSetupQuote(f.codex)
	body := "#!/bin/sh\n"
	if setting != "" {
		if directory != "" {
			body += "if [ \"$PWD\" = " + nativeSetupQuote(directory) + " ]; then\n"
		}
		body += command + " -c " + nativeSetupQuote(setting) + " \"$@\"\n"
		if directory != "" {
			body += "fi\n"
		}
	}
	body += command + " \"$@\"\n"
	path := filepath.Join(f.bin, "codex")
	writeNativeSetupFile(t, path, body)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func (f nativeSetupFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, root := range []string{f.repo, f.linked, f.account, filepath.Join(f.home, ".claude")} {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if root == f.account && path != root && entry.Name() != "config.toml" && entry.Name() != "hooks.json" && !strings.Contains(entry.Name(), ".bak.") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = fmt.Sprintf("%s:%d:%d:%s", info.Mode(), info.Size(), info.ModTime().UnixNano(), body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func callNativeSetup(t *testing.T, directory string, want int, options ...string) instructionsReport {
	t.Helper()
	args := append([]string{"setup", directory, "--agent=codex", "--json"}, options...)
	var stdout, stderr bytes.Buffer
	code := runAgentInstructions(args, nil, &stdout, &stderr)
	var report instructionsReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || code != want {
		t.Fatalf("%v: exit=%d want=%d err=%v stdout=%s stderr=%s", args, code, want, err, &stdout, &stderr)
	}
	return report
}

func TestInstructionsNativePreflightRejectsBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name, setting, issue string
		linked, child, grow  bool
	}{
		{name: "budget", setting: "project_doc_max_bytes=1", issue: "over effective Codex"},
		{name: "discovery", setting: "project_root_markers=[]", issue: "project_root_markers"},
		{name: "fallback", setting: `project_doc_fallback_filenames=["EXAMPLE.md"]`, issue: "project_doc_fallback_filenames"},
		{name: "linked budget", setting: "project_doc_max_bytes=1", issue: "over effective Codex", linked: true},
		{name: "invocation config", setting: "project_doc_max_bytes=1", issue: "over effective Codex", child: true},
		{name: "future merged bytes", setting: "project_doc_max_bytes=64", issue: "over effective Codex", grow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeSetupFixture(t, tc.linked)
			directory, overrideDir := f.repo, ""
			if tc.linked {
				overrideDir = f.linked
			}
			if tc.child {
				directory = filepath.Join(f.repo, "child")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				overrideDir = directory
			}
			if tc.grow {
				callNativeSetup(t, f.repo, 0)
				writeNativeSetupFile(t, filepath.Join(f.repo, "AGENTS.local.md"), strings.Repeat("한", 30))
			}
			f.wrapper(t, tc.setting, overrideDir)
			before := f.snapshot(t)
			for _, options := range [][]string{{"--dry-run"}, nil} {
				report := callNativeSetup(t, directory, 1, options...)
				if !strings.Contains(report.Error, tc.issue) || report.Applied != nil {
					t.Fatalf("wrong preflight failure: %+v", report)
				}
				if !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatal("native preflight failure changed repository or account files")
				}
			}
		})
	}
}

func TestInstructionsNativePreflightAllowsOwnedHookRemoval(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	f.wrapper(t, "features.hooks=false", "")
	before := f.snapshot(t)
	callNativeSetup(t, f.repo, 0, "--dry-run")
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("dry-run changed source, managed or account files")
	}
	callNativeSetup(t, f.repo, 0)
	raw, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
	if err != nil || bytes.Contains(raw, []byte("instructions _hook")) {
		t.Fatalf("owned hooks were not removed: %s %v", raw, err)
	}
	before = f.snapshot(t)
	callNativeSetup(t, f.repo, 0)
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("repeat setup rewrote source, managed or account files")
	}
}

func TestInstructionsNativePreflightUsesPlannedConfig(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("alias=%t", alias), func(t *testing.T) {
			f := newNativeSetupFixture(t, true)
			rel := ".codex/config.toml"
			if alias {
				rel = "config/native.toml"
			}
			source, copy := filepath.Join(f.repo, rel), filepath.Join(f.linked, rel)
			writeNativeSetupFile(t, filepath.Join(f.repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+rel+"\n")
			writeNativeSetupFile(t, source, "project_doc_max_bytes = 1\n")
			if err := overlayruntime.SetupRepository(context.Background(), f.repo, "claude", "checkout", rel); err != nil {
				t.Fatal(err)
			}
			if alias {
				if err := os.Mkdir(filepath.Join(f.linked, ".codex"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../"+rel, filepath.Join(f.linked, ".codex", "config.toml")); err != nil {
					t.Fatal(err)
				}
			}
			writeNativeSetupFile(t, source, "project_doc_max_bytes = 4096\n")
			before := f.snapshot(t)
			callNativeSetup(t, f.repo, 0, "--dry-run")
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("planned repair dry-run changed files")
			}
			f.wrapper(t, "project_doc_max_bytes=1", f.linked)
			for _, options := range [][]string{{"--dry-run"}, nil} {
				report := callNativeSetup(t, f.repo, 1, options...)
				if !strings.Contains(report.Error, "over effective Codex") || !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatalf("planned config concealed higher-priority override: %+v", report)
				}
			}
			f.wrapper(t, "", "")
			callNativeSetup(t, f.repo, 0)
			if data, err := os.ReadFile(copy); err != nil || string(data) != "project_doc_max_bytes = 4096\n" {
				t.Fatalf("planned config was not applied: %q %v", data, err)
			}
		})
	}
}

func TestInstructionsNativePreflightRejectsUncheckablePlannedLayer(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			f := newNativeSetupFixture(t, true)
			const rel = ".codex/config.toml"
			source := filepath.Join(f.repo, rel)
			writeNativeSetupFile(t, source, "project_doc_max_bytes = 4096\n")
			if !missing {
				if err := overlayruntime.SetupRepository(context.Background(), f.repo, "claude", "checkout", rel); err != nil {
					t.Fatal(err)
				}
				writeNativeSetupFile(t, source, "project_doc_max_bytes = 4096\n[projects."+strconv.Quote(f.linked)+"]\ntrust_level = 'trusted'\n")
			}
			before := f.snapshot(t)
			for _, options := range [][]string{{"--local-file=" + rel, "--dry-run"}, {"--local-file=" + rel}} {
				report := callNativeSetup(t, f.repo, 1, options...)
				if !strings.Contains(report.Error, "cannot reliably check planned Codex config") || !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatalf("uncheckable future layer was not rejected before writes: %+v", report)
				}
			}
		})
	}
}

func TestInstructionsNativePreflightAllPreservesBothAccounts(t *testing.T) {
	f := newNativeSetupFixture(t, true)
	f.wrapper(t, "project_doc_max_bytes=1", f.linked)
	before := f.snapshot(t)
	for _, dryRun := range []bool{true, false} {
		args := []string{"setup", f.repo, "--agent=all", "--json"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		var stdout, stderr bytes.Buffer
		code := runAgentInstructions(args, nil, &stdout, &stderr)
		var report instructionsReport
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || code != 1 || !strings.Contains(report.Error, "over effective Codex") || report.Applied != nil {
			t.Fatalf("all setup did not fail native preflight: exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("native preflight failure wrote all-agent setup files")
		}
	}
}

func TestInstructionsNativePreflightEffectiveConfigAndConsistency(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "quota-cli")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, out)
	}
	for _, name := range []string{"grown source", "same size source", "untrusted", "unrelated quoted path", "untrusted child", "larger default budget", "larger explicit budget", "bare primary", "disabled project layer", "trusted override", "nonexistent home"} {
		t.Run(name, func(t *testing.T) {
			f := newNativeSetupFixture(t, false)
			directory := f.repo
			local := filepath.Join(f.repo, "AGENTS.local.md")
			hooks := map[string]any{}
			for _, event := range []string{"SessionStart", "SubagentStart"} {
				command := agenthooks.ShellQuote([]string{binary, "agent", "instructions", "_hook", "--agent=codex", "--event=" + event})
				hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}
			}
			raw, err := json.Marshal(map[string]any{"hooks": hooks})
			if err != nil {
				t.Fatal(err)
			}
			writeNativeSetupFile(t, filepath.Join(f.account, "hooks.json"), string(raw))
			want, issue := 1, ""
			switch name {
			case "grown source", "same size source":
				f.wrapper(t, "project_doc_max_bytes=128", "")
				body := strings.Repeat("x", 256)
				if name == "same size source" {
					body = "changed\n"
				}
				wrapperPath := filepath.Join(f.bin, "codex")
				wrapper, err := os.ReadFile(wrapperPath)
				if err != nil {
					t.Fatal(err)
				}
				mutation := "printf '%s' " + nativeSetupQuote(body) + " > " + nativeSetupQuote(local) + "\n"
				writeNativeSetupFile(t, wrapperPath, strings.Replace(string(wrapper), "#!/bin/sh\n", "#!/bin/sh\n"+mutation, 1))
				issue = "inputs changed after validation"
			case "untrusted", "unrelated quoted path", "untrusted child":
				if name == "untrusted child" {
					directory = filepath.Join(f.repo, "child")
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				trustPath := directory
				if name == "unrelated quoted path" {
					want = 0
					trustPath = strconv.Quote(directory)
				}
				f.wrapper(t, "projects."+trustPath+".trust_level='untrusted'", "")
				if want != 0 {
					issue = "explicitly untrusted in effective Codex config"
				}
			case "larger default budget", "larger explicit budget":
				writeNativeSetupFile(t, local, strings.Repeat("한", 14000))
				if name == "larger explicit budget" {
					configPath := filepath.Join(f.account, "config.toml")
					config, err := os.ReadFile(configPath)
					if err != nil {
						t.Fatal(err)
					}
					writeNativeSetupFile(t, configPath, "project_doc_max_bytes = 128\n"+string(config))
				}
				f.wrapper(t, "project_doc_max_bytes=65536", "")
				want = 0
			case "disabled project layer":
				writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "")
				writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "config.toml"), "model = 'example'\n")
				want = 0
			case "trusted override":
				writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "[projects."+strconv.Quote(f.repo)+"]\ntrust_level='untrusted'\n")
				f.wrapper(t, "projects."+f.repo+".trust_level='trusted'", "")
				want = 0
			case "bare primary":
				git := func(dir string, args ...string) {
					t.Helper()
					cmd := exec.Command("git", args...)
					cmd.Dir = dir
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git %v: %v: %s", args, err, out)
					}
				}
				git(f.repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
				bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
				git(f.repo, "clone", "--bare", "-q", f.repo, bare)
				f.repo, f.linked = bare, filepath.Join(filepath.Dir(f.repo), "checkout")
				git(bare, "worktree", "add", "-q", "--detach", f.linked, "HEAD")
				directory = f.linked
				local = filepath.Join(bare, "AGENTS.local.md")
				writeNativeSetupFile(t, filepath.Join(bare, "AGENTS.md"), "shared\n")
				writeNativeSetupFile(t, local, "private\n")
				writeNativeSetupFile(t, filepath.Join(bare, "private.bin"), "\x00\xff")
				writeNativeSetupFile(t, filepath.Join(bare, "info", "exclude"), "/private.bin\n")
				writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "[projects."+strconv.Quote(f.linked)+"]\ntrust_level='trusted'\n")
				want = 0
			case "nonexistent home":
				t.Setenv("CODEX_HOME", filepath.Join(f.home, "missing"))
				issue = "Codex config preflight"
			}
			for _, phase := range []string{"dry-run", "setup", "status", "repeat"} {
				if want != 0 && (phase == "status" || phase == "repeat") {
					continue
				}
				if phase == "setup" && (name == "grown source" || name == "same size source") {
					writeNativeSetupFile(t, local, "private\n")
				}
				before := f.snapshot(t)
				operation := "setup"
				if phase == "status" {
					operation = "status"
				}
				args := []string{"agent", "instructions", operation, directory, "--agent=codex", "--json"}
				if name == "bare primary" && operation == "setup" {
					args = append(args, "--shared-source=primary", "--local-file=private.bin")
				}
				if phase == "dry-run" {
					args = append(args, "--dry-run")
				}
				cmd := exec.Command(binary, args...)
				cmd.Dir = directory
				out, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				var report instructionsReport
				if jsonErr := json.Unmarshal(out, &report); jsonErr != nil || code != want || !strings.Contains(report.Error, issue) {
					t.Fatalf("%s: exit=%d want=%d parse=%v output=%s", phase, code, want, jsonErr, out)
				}
				after := f.snapshot(t)
				if want != 0 {
					if report.Applied != nil {
						t.Fatalf("account apply ran after failure: %+v", report)
					}
					if name == "grown source" || name == "same size source" {
						delete(before, local)
						delete(after, local)
					}
				}
				if want != 0 || phase != "setup" {
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("%s changed source, managed, or account files", phase)
					}
				}
				if name == "nonexistent home" {
					if _, err := os.Stat(os.Getenv("CODEX_HOME")); !os.IsNotExist(err) {
						t.Fatalf("missing CODEX_HOME was created: %v", err)
					}
				}
				if name == "disabled project layer" && phase == "status" {
					disabled := false
					for _, layer := range report.Agents[0].Native.ConfigLayers {
						disabled = disabled || layer.DisabledReason != nil && *layer.DisabledReason != ""
					}
					if !disabled {
						t.Fatal("fixture did not exercise disabled-layer informational diagnostics")
					}
				}
				if want == 0 && phase == "setup" {
					shared, err := os.ReadFile(filepath.Join(f.repo, "AGENTS.md"))
					if err != nil {
						t.Fatal(err)
					}
					private, err := os.ReadFile(local)
					if err != nil {
						t.Fatal(err)
					}
					merged, err := os.ReadFile(filepath.Join(directory, "AGENTS.override.md"))
					if err != nil || !bytes.Equal(merged, append(append(shared, '\n'), private...)) {
						t.Fatalf("wrong applied native document: %v", err)
					}
					if name == "bare primary" {
						copy, err := os.ReadFile(filepath.Join(f.linked, "private.bin"))
						if err != nil || !bytes.Equal(copy, []byte{0, 255}) {
							t.Fatalf("binary local file changed: %v", err)
						}
					}
					hooks, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
					if err != nil || bytes.Contains(hooks, []byte("instructions _hook")) {
						t.Fatalf("owned hooks were not removed: %s %v", hooks, err)
					}
				}
				t.Logf("%s: CLI exit=%d", phase, code)
			}
		})
	}
}

func TestInstructionsNativeValidatedPlanRejectsChangedInputs(t *testing.T) {
	for _, changed := range []string{"source", "account", "ignore"} {
		t.Run(changed, func(t *testing.T) {
			f := newNativeSetupFixture(t, false)
			hooksPath := filepath.Join(f.account, "hooks.json")
			plan, _, err := overlayruntime.PlanNativeCodexRepository(context.Background(), f.repo, "codex", "", []string{hooksPath}, []string{hooksPath})
			if err != nil {
				t.Fatal(err)
			}
			validated, err := agentinstructions.PreflightNativeCodex(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.repo, "AGENTS.local.md")
			body := "changed\n"
			if changed == "account" {
				path, body = hooksPath, "{}\n"
			}
			if changed == "ignore" {
				path, body = filepath.Join(f.repo, ".git", "info", "exclude"), "/AGENTS.local.md\n"
			}
			writeNativeSetupFile(t, path, body)
			before := f.snapshot(t)
			applied := false
			err = validated.Apply(context.Background(), func() error { applied = true; return nil })
			if err == nil || !strings.Contains(err.Error(), "inputs changed after validation") || applied {
				t.Fatalf("changed plan applied: err=%v account=%t", err, applied)
			}
			after := f.snapshot(t)
			delete(after, filepath.Join(f.repo, ".git", "quota-instructions.lock"))
			if !reflect.DeepEqual(before, after) {
				t.Fatal("consistency failure mutated repository or account files")
			}
		})
	}
}

func TestInstructionsNativeDisabledProjectSettings(t *testing.T) {
	for _, scope := range []string{"root", "linked", "child"} {
		t.Run(scope, func(t *testing.T) {
			f := newNativeSetupFixture(t, scope == "linked")
			writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "")
			directory := f.repo
			if scope == "linked" {
				directory = f.linked
			} else if scope == "child" {
				directory = filepath.Join(f.repo, "child")
			}
			configPath := filepath.Join(directory, ".codex", "config.toml")
			writeNativeSetupFile(t, configPath, "[features]\nhooks = 'invalid'\n")
			hooks, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeNativeSetupFile(t, filepath.Join(directory, ".codex", "hooks.json"), string(hooks))
			native, err := agentinstructions.InspectNativeCodexConfig(context.Background(), directory)
			if err != nil || native.State != "configured" {
				t.Fatalf("native: %+v %v", native, err)
			}
			disabled := false
			for _, layer := range native.ConfigLayers {
				if layer.Name.DotCodexFolder == filepath.Dir(configPath) {
					disabled = layer.DisabledReason != nil && *layer.DisabledReason != ""
				}
			}
			if !disabled {
				t.Fatal("fixture project config was not disabled by native discovery")
			}
			before := f.snapshot(t)
			callNativeSetup(t, directory, 0, "--dry-run")
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("dry-run changed files")
			}
			callNativeSetup(t, directory, 0)
			remaining, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
			if err != nil || bytes.Contains(remaining, []byte("instructions _hook")) {
				t.Fatalf("owned account hooks remain: %s %v", remaining, err)
			}
			projectHooks, err := os.ReadFile(filepath.Join(directory, ".codex", "hooks.json"))
			if err != nil || !bytes.Equal(projectHooks, hooks) {
				t.Fatalf("disabled hooks changed: %s %v", projectHooks, err)
			}
			callNativeStatus(t, directory, "configured")
		})
	}
}

func callNativeStatus(t *testing.T, directory, want string) instructionsReport {
	t.Helper()
	var out, stderr bytes.Buffer
	code := runAgentInstructions([]string{"status", directory, "--agent=codex", "--json"}, nil, &out, &stderr)
	var report instructionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	wantCode := 1
	if want == "configured" {
		wantCode = 0
	}
	if code != wantCode || len(report.Agents) != 1 || report.Agents[0].State != want {
		t.Fatalf("status: exit=%d want=%s output=%s stderr=%s", code, want, &out, &stderr)
	}
	return report
}

func TestInstructionsNativeEffectiveFeatureOverride(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "config.toml"), "[features]\nhooks = 'invalid'\n")
	f.wrapper(t, "features.hooks=true", "")
	native, err := agentinstructions.InspectNativeCodexConfig(context.Background(), f.repo)
	if err != nil || native.State != "configured" {
		t.Fatalf("native: %+v %v", native, err)
	}
	before := f.snapshot(t)
	callNativeSetup(t, f.repo, 0, "--dry-run")
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("dry-run changed files")
	}
	callNativeSetup(t, f.repo, 0)
	callNativeStatus(t, f.repo, "configured")
}

func TestInstructionsNativeDisabledOversizedHooks(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "")
	writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "config.toml"), "")
	hooksPath := filepath.Join(f.repo, ".codex", "hooks.json")
	hooks := "{}" + strings.Repeat(" ", 8<<20)
	writeNativeSetupFile(t, hooksPath, hooks)
	native, err := agentinstructions.InspectNativeCodexConfig(context.Background(), f.repo)
	if err != nil || native.State != "configured" {
		t.Fatalf("native: %+v %v", native, err)
	}
	before := f.snapshot(t)
	callNativeSetup(t, f.repo, 0, "--dry-run")
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("dry-run changed files")
	}
	callNativeSetup(t, f.repo, 0)
	callNativeStatus(t, f.repo, "configured")
	after, err := os.ReadFile(hooksPath)
	if err != nil || string(after) != hooks {
		t.Fatal("disabled hooks changed")
	}
}

func TestInstructionsNativeActiveOversizedHooksRejectBeforeWrites(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "config.toml"), "")
	writeNativeSetupFile(t, filepath.Join(f.repo, ".codex", "hooks.json"), "{}"+strings.Repeat(" ", 8<<20))
	before := f.snapshot(t)
	for _, options := range [][]string{{"--dry-run"}, nil} {
		report := callNativeSetup(t, f.repo, 1, options...)
		if !strings.Contains(report.Error, "exceeds 8388608-byte file size limit") {
			t.Fatalf("unexpected rejection: %+v", report)
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("active settings error changed files")
		}
	}
}

func TestInstructionsNativeUnavailableStatus(t *testing.T) {
	f := newNativeSetupFixture(t, false)
	callNativeSetup(t, f.repo, 0)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(unavailable, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", unavailable)
	report := callNativeStatus(t, f.repo, "unknown")
	if report.Agents[0].Native == nil || report.Agents[0].Native.State != "unknown" || len(report.Agents[0].Issues) != 1 {
		t.Fatalf("unavailable native inspection was repeated or misclassified: %+v", report.Agents[0])
	}
}

func TestInstructionsNativeStatusDiscoveryFailures(t *testing.T) {
	for _, failure := range []string{"initial config", "linked config", "later child config", "incompatible config", "repository"} {
		t.Run(failure, func(t *testing.T) {
			f := newNativeSetupFixture(t, failure == "linked config")
			directory := f.repo
			if failure == "later child config" {
				directory = filepath.Join(f.repo, "child")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			callNativeSetup(t, directory, 0)
			want := "unknown"
			calls := filepath.Join(f.home, "native-calls")
			prefix := "printf '%s\\n' \"$PWD\" >> " + nativeSetupQuote(calls) + "\n"
			switch failure {
			case "initial config":
				f.wrapper(t, "project_doc_max_bytes='invalid'", "")
			case "linked config":
				f.wrapper(t, "project_doc_max_bytes='invalid'", f.linked)
			case "later child config":
				marker := filepath.Join(f.home, "child-inspected")
				prefix += "if [ \"$PWD\" = " + nativeSetupQuote(directory) + " ]; then\n" +
					"if [ -f " + nativeSetupQuote(marker) + " ]; then\nexec " + nativeSetupQuote(f.codex) + " -c \"project_doc_max_bytes='invalid'\" \"$@\"\nfi\n" +
					": > " + nativeSetupQuote(marker) + "\nfi\n"
			case "incompatible config":
				f.wrapper(t, "project_root_markers=[]", "")
				want = "blocked"
			case "repository":
				writeNativeSetupFile(t, filepath.Join(f.repo, "AGENTS.override.md"), "unmanaged\n")
				want = "blocked"
			}
			wrapperPath := filepath.Join(f.bin, "codex")
			body, err := os.ReadFile(wrapperPath)
			if err != nil {
				t.Fatal(err)
			}
			writeNativeSetupFile(t, wrapperPath, strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\n"+prefix, 1))
			report := callNativeStatus(t, directory, want)
			if want == "unknown" && (report.Agents[0].Native == nil || report.Agents[0].Native.State != "unknown") {
				t.Fatalf("native state disagrees: %+v", report.Agents[0])
			}
			invocations, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "initial config" && string(invocations) != directory+"\n" {
				t.Fatalf("repeated failed native inspection: %s", invocations)
			}
			if failure == "later child config" && strings.Count(string(invocations), directory+"\n") != 2 {
				t.Fatalf("did not exercise later selected-directory failure: %s", invocations)
			}
		})
	}
}

func TestInstructionsNativeActiveProjectHooks(t *testing.T) {
	for _, scope := range []string{"root", "child", "inherited config alias"} {
		t.Run(scope, func(t *testing.T) {
			f := newNativeSetupFixture(t, scope == "inherited config alias")
			directory := f.repo
			var options []string
			if scope == "child" {
				directory = filepath.Join(f.repo, "child")
			}
			var conflictPath string
			if scope == "inherited config alias" {
				const rel = "config/native.toml"
				writeNativeSetupFile(t, filepath.Join(f.repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+rel+"\n")
				source := filepath.Join(f.repo, rel)
				writeNativeSetupFile(t, source, "project_doc_max_bytes=32768\n")
				if err := overlayruntime.SetupRepository(context.Background(), f.repo, "claude", "checkout", rel); err != nil {
					t.Fatal(err)
				}
				conflictPath = filepath.Join(f.repo, ".codex", "config.toml")
				if err := os.MkdirAll(filepath.Dir(conflictPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../"+rel, conflictPath); err != nil {
					t.Fatal(err)
				}
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				command := agenthooks.ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"})
				writeNativeSetupFile(t, source, fmt.Sprintf("project_doc_max_bytes=32768\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype='command'\ncommand=%q\n", command))
				options = append(options, "--local-file="+rel)
			} else {
				writeNativeSetupFile(t, filepath.Join(f.account, "config.toml"), "")
				f.wrapper(t, "projects."+f.repo+".trust_level='trusted'", "")
				conflictPath = filepath.Join(directory, ".codex", "hooks.json")
				hooks, err := os.ReadFile(filepath.Join(f.account, "hooks.json"))
				if err != nil {
					t.Fatal(err)
				}
				writeNativeSetupFile(t, conflictPath, string(hooks))
			}
			before := f.snapshot(t)
			for _, dryRun := range []bool{true, false} {
				args := append([]string(nil), options...)
				if dryRun {
					args = append(args, "--dry-run")
				}
				report := callNativeSetup(t, directory, 1, args...)
				if !strings.Contains(report.Error, conflictPath) || !strings.Contains(report.Error, "conflicts with native instruction delivery") || report.Applied != nil {
					t.Fatalf("wrong active hook failure: %+v", report)
				}
				if !reflect.DeepEqual(before, f.snapshot(t)) {
					t.Fatal("active hook conflict changed files")
				}
			}
		})
	}
}
