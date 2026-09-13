package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstructionsRejectsGenericExecutionInputs(t *testing.T) {
	for _, args := range [][]string{
		{"setup", "--spec=arbitrary.json"},
		{"setup", "--command=echo"},
		{"setup", "--local-file="},
		{"status", "--local-file=example"},
		{"setup", "--agent=all", "--agent=codex"},
		{"status", "--shared-source=primary"},
		{"uninstall"},
		{"uninstall", "--scope=everything"},
		{"verify", "--timeout=0"},
		{"_hook", "--agent=claude", "--event=SubagentStart"},
		{"_hook", "--agent=codex", "--event=WorktreeCreate"},
		{"_hook", "--agent=codex", "--event=SessionStart", "arbitrary.md"},
		{"_hook", "--agent=codex", "--event=SessionStart", "--command=echo"},
	} {
		var out, stderr bytes.Buffer
		if code := runAgentInstructions(args, nil, &out, &stderr); code != 2 {
			t.Errorf("%q: exit=%d output=%q errors=%q", args, code, out.String(), stderr.String())
		}
	}
}

func TestInstructionsStandaloneBinaryNeedsNoPythonOrSpec(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin '한글'")
	home := filepath.Join(dir, "home")
	repo := filepath.Join(dir, "repo '한글'\nline")
	for _, path := range []string{binDir, home, repo} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(binDir, "quota-cli")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for name, target := range map[string]string{"git": git, "sh": "/bin/sh"} {
		if err := os.Symlink(target, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + home, "PATH=" + binDir, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "CODEX_HOME=" + filepath.Join(home, ".codex")}
	call := func(input string, want int, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env, cmd.Dir, cmd.Stdin = env, repo, strings.NewReader(input)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("%q: %v", args, err)
			}
			code = exit.ExitCode()
		}
		if code != want {
			t.Fatalf("%q: exit=%d want=%d stdout=%s stderr=%s", args, code, want, out.String(), stderr.String())
		}
		return out.Bytes()
	}
	call("", 0, git, "init", "-q")
	for name, body := range map[string]string{"AGENTS.md": "shared rule marker\n", "AGENTS.local.md": "private rule marker\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(home, ".claude", "settings.json")
	if err := os.Mkdir(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	const original = `{"keep":{"n":9007199254740993},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"printf harmless"}]}]}}`
	if err := os.WriteFile(config, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	projectConfig := filepath.Join(repo, ".claude", "settings.json")
	if err := os.Mkdir(filepath.Dir(projectConfig), 0700); err != nil {
		t.Fatal(err)
	}
	conflict, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart CLAUDE.md CLAUDE.local.md . claude-session`}}}}}})
	if err := os.WriteFile(projectConfig, conflict, 0600); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{}, {"--dry-run"}} {
		args := append([]string{binary, "agent", "instructions", "setup", repo, "--agent=claude", "--json"}, extra...)
		out := call("", 1, args...)
		if !bytes.Contains(out, []byte("conflicts with the fixed account installation")) {
			t.Fatalf("project hook conflict not diagnosed: %s", out)
		}
		if got, _ := os.ReadFile(config); string(got) != original {
			t.Fatal("conflicting project hook allowed account mutation")
		}
		for _, path := range []string{filepath.Join(repo, "CLAUDE.md"), filepath.Join(repo, ".git", "quota-instructions.json")} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("conflicting project hook allowed repository mutation: %s", path)
			}
		}
	}
	if err := os.WriteFile(projectConfig, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	call("", 0, binary, "agent", "instructions", "setup", repo, "--agent=claude", "--dry-run")
	if got, _ := os.ReadFile(config); string(got) != original {
		t.Fatal("dry-run modified account")
	}
	if _, err := os.Lstat(filepath.Join(repo, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("dry-run created bridge")
	}
	call("", 0, binary, "agent", "instructions", "setup", repo, "--agent=claude")
	call("", 0, binary, "agent", "instructions", "status", "--agent=claude", repo)
	before, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	backupsBefore, _ := filepath.Glob(config + ".bak.*")
	call("", 0, binary, "agent", "instructions", "setup", "--agent=claude", repo)
	after, _ := os.Stat(config)
	backupsAfter, _ := filepath.Glob(config + ".bak.*")
	if !before.ModTime().Equal(after.ModTime()) || len(backupsBefore) != len(backupsAfter) {
		t.Fatal("repeat setup changed account or created backup")
	}
	contents, _ := os.ReadFile(config)
	if !bytes.Contains(contents, []byte("9007199254740993")) || !bytes.Contains(contents, []byte("printf harmless")) {
		t.Fatalf("user configuration not preserved: %s", contents)
	}
	for name, want := range map[string]string{"CLAUDE.md": "@AGENTS.md\n", "CLAUDE.local.md": "@AGENTS.local.md\n"} {
		if got, err := os.ReadFile(filepath.Join(repo, name)); err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", name, got, err)
		}
	}
	payload, _ := json.Marshal(map[string]string{"cwd": repo, "session_id": "test-session", "source": "startup"})
	readHookCommand := func(path, event string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		groups := result.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("unexpected %s hook count: %s", event, data)
		}
		return groups[0].Hooks[0].Command
	}
	if out := call(string(payload), 0, "/bin/sh", "-c", readHookCommand(config, "SessionStart")); len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("Claude duplicated native rules: %s", out)
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.local.md"} {
		if err := os.Rename(filepath.Join(repo, name), filepath.Join(repo, name+".saved")); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"status", "verify"} {
		out := call("", 1, binary, "agent", "instructions", operation, repo, "--agent=claude", "--json")
		var result instructionsReport
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Agents) != 1 || result.Agents[0].State != "blocked" || result.Agents[0].Delivery != "not-verified" || !strings.Contains(result.Agents[0].Repository, "instruction sources are missing") {
			t.Fatalf("missing sources did not block %s before delivery: %s", operation, out)
		}
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.local.md"} {
		if err := os.Rename(filepath.Join(repo, name+".saved"), filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"first.local.json", "second.local.json"} {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(`{"value":"example"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("*.local.json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(repo, ".git", "quota-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	allResult := call("", 1, binary, "agent", "instructions", "setup", repo, "--local-file=first.local.json", "--agent=all", "--local-file", "second.local.json", "--local-file=first.local.json", "--json")
	var missing instructionsReport
	if err := json.Unmarshal(allResult, &missing); err != nil || !strings.Contains(missing.Error, "start Codex native inspection") || missing.Applied != nil {
		t.Fatalf("missing native CLI was not rejected before writes: %s %v", allResult, err)
	}
	if current, err := os.ReadFile(filepath.Join(repo, ".git", "quota-instructions.json")); err != nil || !bytes.Equal(current, stateBefore) {
		t.Fatal("missing native CLI changed repository state")
	}
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.override.md")); !os.IsNotExist(err) {
		t.Fatal("missing native CLI created merged instructions")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("remaining native setup checks require real Codex CLI")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if node, err := exec.LookPath("node"); err == nil {
		if err := os.Symlink(node, filepath.Join(binDir, "node")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(codex, filepath.Join(binDir, "codex")); err != nil {
		t.Fatal(err)
	}
	allResult = call("", 0, binary, "agent", "instructions", "setup", repo, "--local-file=first.local.json", "--agent=all", "--local-file", "second.local.json", "--local-file=first.local.json", "--json")
	stateBytes, err := os.ReadFile(filepath.Join(repo, ".git", "quota-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		LocalFiles []string `json:"local_files"`
	}
	if err := json.Unmarshal(stateBytes, &state); err != nil || strings.Join(state.LocalFiles, ",") != "first.local.json,second.local.json" {
		t.Fatalf("repeatable local files were not registered: %+v %v", state, err)
	}
	var all struct {
		Agents []struct{ Agent, State string } `json:"agents"`
		Error  string                          `json:"error"`
	}
	if err := json.Unmarshal(allResult, &all); err != nil {
		t.Fatal(err)
	}
	if all.Error != "" || len(all.Agents) != 2 || all.Agents[0].State != "configured" || all.Agents[1].State != "configured" {
		t.Fatalf("native CLI setup failed: %s", allResult)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatal("native Codex setup installed instruction hooks")
	}
	merged, err := os.ReadFile(filepath.Join(repo, "AGENTS.override.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(merged), "shared rule marker") != 1 || strings.Count(string(merged), "private rule marker") != 1 {
		t.Fatalf("wrong native instruction content: %q", merged)
	}
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		if out := call(string(payload), 0, binary, "agent", "instructions", "_hook", "--agent=codex", "--event="+event); len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("legacy hook duplicated native instructions: %s", out)
		}
	}
	call("", 0, binary, "agent", "instructions", "uninstall", repo, "--agent=codex", "--scope=repository")
	if out := call(string(payload), 0, binary, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"); len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("disabled repository still injected: %s", out)
	}
	call("", 0, binary, "agent", "instructions", "uninstall", "--scope=account", "--agent=claude")
	contents, _ = os.ReadFile(config)
	if bytes.Contains(contents, []byte("instructions")) || !bytes.Contains(contents, []byte("printf harmless")) {
		t.Fatalf("uninstall ownership: %s", contents)
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.local.md"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("source lost: %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "quota", "agent-overlay.json")); !os.IsNotExist(err) {
		t.Fatal("dedicated setup generated a generic spec")
	}
}
