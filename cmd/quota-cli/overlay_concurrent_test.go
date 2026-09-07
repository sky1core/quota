package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConcurrentApplyProcess(t *testing.T) {
	side := os.Getenv("QUOTA_TEST_APPLY_SIDE")
	if side == "" {
		return
	}
	var args []string
	switch side {
	case "hooks":
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		args = []string{"apply", "--runtime=claude", "--policy-dir", os.Getenv("QUOTA_TEST_POLICY_DIR"), "--binary", binary}
		if code := runAgentHooks(args, io.Discard, os.Stderr); code != 0 {
			t.Fatalf("hooks apply exit=%d", code)
		}
	case "overlay":
		args = []string{"apply", "--runtime=claude", "--spec", os.Getenv("QUOTA_TEST_SPEC")}
		if code := runAgentOverlay(args, io.Discard, os.Stderr); code != 0 {
			t.Fatalf("overlay apply exit=%d", code)
		}
	default:
		t.Fatalf("unknown helper side %q", side)
	}
}

func TestConcurrentHooksAndOverlayPreserveBothUpdates(t *testing.T) {
	home := t.TempDir()
	settingsDir := filepath.Join(home, "claude")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", settingsDir)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(home, "spec.json")
	if err := os.WriteFile(specPath, []byte(`{"version":1,"claude":{"hooks":{"SessionStart":[{"command":"/opt/example-overlay session"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(home, "policies")
	if code := runAgentHooks([]string{"init", "--policy-dir", policyDir}, io.Discard, os.Stderr); code != 0 {
		t.Fatalf("init exit=%d", code)
	}
	t.Setenv("QUOTA_TEST_SPEC", specPath)
	t.Setenv("QUOTA_TEST_POLICY_DIR", policyDir)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	initial := map[string]any{"large": json.Number("9007199254740993"), "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/unrelated audit"}}}}}}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for _, side := range []string{"hooks", "overlay"} {
			cmd := exec.Command(binary, "-test.run=^TestConcurrentApplyProcess$")
			cmd.Env = append(os.Environ(), "QUOTA_TEST_APPLY_SIDE="+side)
			output := new(bytes.Buffer)
			cmd.Stdout = output
			cmd.Stderr = output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			commands = append(commands, cmd)
			outputs = append(outputs, output)
		}
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("round %d: %v\n%s", round, err, outputs[i])
			}
		}
		saved, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		decoder := json.NewDecoder(bytes.NewReader(saved))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			t.Fatal(err)
		}
		hooks := root["hooks"].(map[string]any)
		if hooks["PreToolUse"] == nil || !bytes.Contains(saved, []byte("/opt/example-overlay session")) {
			t.Fatalf("lost update: %s", saved)
		}
		if !bytes.Contains(saved, []byte("/opt/unrelated audit")) || root["large"] != json.Number("9007199254740993") {
			t.Fatalf("user configuration changed: %s", saved)
		}
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(settingsPath)
	backupsBefore, _ := filepath.Glob(settingsPath + ".bak.*")
	for _, side := range []string{"hooks", "overlay"} {
		cmd := exec.Command(binary, "-test.run=^TestConcurrentApplyProcess$")
		cmd.Env = append(os.Environ(), "QUOTA_TEST_APPLY_SIDE="+side)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, output)
		}
	}
	after, _ := os.ReadFile(settingsPath)
	statAfter, _ := os.Stat(settingsPath)
	backupsAfter, _ := filepath.Glob(settingsPath + ".bak.*")
	if !bytes.Equal(before, after) || !statBefore.ModTime().Equal(statAfter.ModTime()) || len(backupsBefore) != len(backupsAfter) {
		t.Fatal("reapplying changed bytes, mtime or backups")
	}
}
