package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

type nativeHookFixture struct {
	TrustStatus string   `json:"trustStatus"`
	Enabled     *bool    `json:"enabled"`
	Source      string   `json:"source"`
	SourcePath  string   `json:"sourcePath"`
	Mode        string   `json:"mode"`
	Warnings    []string `json:"warnings"`
}

func installNativeHookFixture(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n[ \"$1\" = app-server ] || exit 91\nexec " + agenthooks.ShellQuote([]string{executable}) + " -test.run=^TestAgentHooksNativeFixtureProcess$\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("QUOTA_NATIVE_HOOK_FIXTURE", "1")
	return dir
}

func setNativeHookFixture(t *testing.T, codexHome string, fixture nativeHookFixture) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "native-fixture.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentHooksNativeFixtureProcess(t *testing.T) {
	if os.Getenv("QUOTA_NATIVE_HOOK_FIXTURE") != "1" {
		return
	}
	if err := serveNativeHookFixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func serveNativeHookFixture() error {
	home := os.Getenv("CODEX_HOME")
	fixture := nativeHookFixture{TrustStatus: "trusted", Source: "user"}
	if data, err := os.ReadFile(filepath.Join(home, "native-fixture.json")); err == nil {
		if err := json.Unmarshal(data, &fixture); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if fixture.Mode == "unavailable" {
		return fmt.Errorf("native fixture unavailable")
	}
	if fixture.Warnings == nil {
		fixture.Warnings = []string{}
	}
	reader := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)
	for reader.Scan() {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cwds []string `json:"cwds"`
			} `json:"params"`
		}
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			return err
		}
		if request.ID == nil {
			continue
		}
		switch request.Method {
		case "initialize":
			if err := writer.Encode(map[string]any{"id": *request.ID, "result": map[string]any{}}); err != nil {
				return err
			}
		case "hooks/list":
			if len(request.Params.Cwds) != 1 {
				return fmt.Errorf("expected one cwd")
			}
			if fixture.Mode == "rpc-error" {
				return writer.Encode(map[string]any{"id": *request.ID, "error": map[string]any{"code": -32601}})
			}
			if fixture.Mode == "malformed" {
				return writer.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"data": []any{map[string]any{"cwd": request.Params.Cwds[0]}}}})
			}
			hooks := []any{}
			if fixture.Mode != "no-native-hook" {
				path := filepath.Join(home, "hooks.json")
				root, err := agenthooks.ReadJSONObject(path)
				if err != nil {
					return err
				}
				groups := root["hooks"].(map[string]any)["PreToolUse"].([]any)
				entry := groups[len(groups)-1].(map[string]any)
				entries := entry["hooks"].([]any)
				command := entries[0].(map[string]any)["command"].(string)
				enabled := true
				if fixture.Enabled != nil {
					enabled = *fixture.Enabled
				}
				trust := fixture.TrustStatus
				if trust == "" {
					trust = "trusted"
				}
				source := fixture.Source
				if source == "" {
					source = "user"
				}
				sourcePath := fixture.SourcePath
				if sourcePath == "" {
					sourcePath = path
				}
				hook := map[string]any{"eventName": "preToolUse", "handlerType": "command", "command": command, "source": source, "sourcePath": sourcePath, "enabled": enabled, "trustStatus": trust, "matcher": "Bash", "async": false, "key": "fixture-key", "currentHash": "fixture-hash", "isManaged": false}
				if fixture.Mode == "missing-metadata" {
					delete(hook, "currentHash")
				}
				hooks = append(hooks, hook)
			}
			return writer.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"data": []any{map[string]any{"cwd": request.Params.Cwds[0], "hooks": hooks, "errors": []any{}, "warnings": fixture.Warnings}}}})
		default:
			return fmt.Errorf("unexpected RPC method %q", request.Method)
		}
	}
	return reader.Err()
}

func TestAgentHooksCodexNativeActivation(t *testing.T) {
	boolPtr := func(value bool) *bool { return &value }
	for _, tc := range []struct {
		name       string
		fixture    nativeHookFixture
		activation string
		applyCode  int
		warnings   bool
	}{
		{"trusted", nativeHookFixture{TrustStatus: "trusted"}, "ready", 0, false},
		{"managed", nativeHookFixture{TrustStatus: "managed"}, "ready", 0, false},
		{"untrusted", nativeHookFixture{TrustStatus: "untrusted"}, "needs-trust", 1, false},
		{"modified", nativeHookFixture{TrustStatus: "modified"}, "needs-trust", 1, false},
		{"disabled", nativeHookFixture{TrustStatus: "trusted", Enabled: boolPtr(false)}, "blocked", 1, false},
		{"no-native-hook", nativeHookFixture{Mode: "no-native-hook"}, "blocked", 1, false},
		{"unavailable", nativeHookFixture{Mode: "unavailable"}, "unknown", 1, false},
		{"rpc-error", nativeHookFixture{Mode: "rpc-error"}, "unknown", 1, false},
		{"malformed", nativeHookFixture{Mode: "malformed"}, "unknown", 1, false},
		{"missing-metadata", nativeHookFixture{Mode: "missing-metadata"}, "unknown", 1, false},
		{"unknown-trust", nativeHookFixture{TrustStatus: "future-status"}, "unknown", 1, false},
		{"wrong-source-path", nativeHookFixture{SourcePath: "/nonexistent/native-hooks.json"}, "unknown", 1, false},
		{"warnings-only", nativeHookFixture{Warnings: []string{"fixture warning"}}, "ready", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			installNativeHookFixture(t)
			codexHome := filepath.Join(home, ".codex")
			setNativeHookFixture(t, codexHome, tc.fixture)
			configPath := filepath.Join(codexHome, "config.toml")
			configBefore := []byte("model = 'fixture-model'\n")
			if err := os.WriteFile(configPath, configBefore, 0o600); err != nil {
				t.Fatal(err)
			}
			policyDir := filepath.Join(home, "policies")
			policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
				t.Fatal(err)
			}
			binary := writeTestExecutable(t, filepath.Join(home, "bin", "quota-cli"))
			args := []string{"--policy-dir", policyDir, "--runtime", "codex", "--binary", binary, "--json"}
			for _, operation := range []string{"apply", "plan", "doctor"} {
				var stdout, stderr bytes.Buffer
				code := runAgentHooks(append([]string{operation}, args...), &stdout, &stderr)
				wantCode := tc.applyCode
				if operation == "plan" {
					wantCode = 0
				}
				if code != wantCode {
					t.Fatalf("%s code=%d want=%d stdout=%s stderr=%s", operation, code, wantCode, &stdout, &stderr)
				}
				var report struct {
					Hooks []agenthooks.HookPlan `json:"hooks"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				if len(report.Hooks) != 1 || report.Hooks[0].Activation != tc.activation {
					t.Fatalf("%s report=%s", operation, &stdout)
				}
				if tc.warnings && !strings.Contains(stdout.String(), "fixture warning") {
					t.Fatalf("%s warning missing: %s", operation, &stdout)
				}
			}
			configAfter, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(configAfter, configBefore) {
				t.Fatalf("Codex settings changed during diagnostics: before=%q after=%q err=%v", configBefore, configAfter, err)
			}
		})
	}
}

func TestAgentHooksApplyCancelledBeforeWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	policyDir := filepath.Join(home, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	binary := writeTestExecutable(t, filepath.Join(home, "bin", "quota-cli"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := agentHooksApply(ctx, []string{"--policy-dir", policyDir, "--runtime", "all", "--binary", binary, "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("apply code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	for _, path := range []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancelled apply wrote %s: %v", path, err)
		}
	}
}

func TestAgentHooksCodexNativePreservesStoredCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installNativeHookFixture(t)
	policyDir := filepath.Join(home, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	binary := writeTestExecutable(t, filepath.Join(home, "bin", "quota-cli"))
	path := filepath.Join(home, ".codex", "hooks.json")
	if _, err := agenthooks.ApplyPath("codex", path, binary, policyDir); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("%q  agent hooks eval --runtime codex --policy-dir=%q", binary, policyDir)
	for _, duplicate := range []bool{false, true} {
		hooks := []any{map[string]any{"type": "command", "command": command}}
		if duplicate {
			hooks = append(hooks, map[string]any{"type": "command", "command": agenthooks.HookCommand("codex", binary, policyDir)})
		}
		content, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": hooks}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, operation := range []string{"plan", "doctor"} {
			var stdout, stderr bytes.Buffer
			code := runAgentHooks([]string{operation, "--runtime=codex", "--policy-dir", policyDir, "--json"}, &stdout, &stderr)
			wantCode, wantActivation := 0, "ready"
			if duplicate {
				wantActivation = "blocked"
				if operation == "doctor" {
					wantCode = 1
				}
			}
			var report struct {
				Hooks []agenthooks.HookPlan `json:"hooks"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if code != wantCode || len(report.Hooks) != 1 || report.Hooks[0].Activation != wantActivation || report.Hooks[0].Command != command {
				t.Fatalf("duplicate=%v %s code=%d stdout=%s stderr=%s", duplicate, operation, code, &stdout, &stderr)
			}
		}
	}
}
