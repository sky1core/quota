package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func TestAgentHooksInitListVerifyEval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	policyDir := filepath.Join(home, "policies")

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"init", "--policy-dir", policyDir, "--preset", agenthooks.PresetGitHubHistoryGuard}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "github-history-guard.json") {
		t.Fatalf("init stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runAgentHooks([]string{"list", "--policy-dir", policyDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "github-history-guard enabled=true") {
		t.Fatalf("list stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "groups=3") {
		t.Fatalf("list stdout missing groups: %q", stdout.String())
	}
	for _, want := range []string{
		"group remote-code-ref-mutation:",
		"group local-system-secret-safety:",
		"group github-collaboration-metadata:",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("list stdout missing %q: %q", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runAgentHooks([]string{"verify", "--policy-dir", policyDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), "FAIL") {
		t.Fatalf("verify stdout contains failure:\n%s", stdout.String())
	}
	for _, want := range []string{
		"github-history-guard/remote-code-ref-mutation/deny git push",
		"github-history-guard/remote-code-ref-mutation/deny git bisect run",
		"github-history-guard/remote-code-ref-mutation/deny gh pr close delete branch",
		"github-history-guard/remote-code-ref-mutation/deny gh repo create",
		"github-history-guard/remote-code-ref-mutation/deny gh workflow run",
		"github-history-guard/remote-code-ref-mutation/deny gh agent task create",
		"github-history-guard/remote-code-ref-mutation/deny gh codespace ssh",
		"github-history-guard/remote-code-ref-mutation/deny gh pr merge",
		"github-history-guard/local-system-secret-safety/deny rm",
		"github-history-guard/local-system-secret-safety/deny sudo",
		"github-history-guard/local-system-secret-safety/deny killall",
		"github-history-guard/local-system-secret-safety/allow kill single pid",
		"github-history-guard/local-system-secret-safety/deny gh auth token",
		"github-history-guard/github-collaboration-metadata/allow pr close without branch delete",
		"github-history-guard/github-collaboration-metadata/allow pr comment",
		"github-history-guard/github-collaboration-metadata/allow issue edit",
		"github-history-guard/github-collaboration-metadata/allow issue view",
		"github-history-guard/github-collaboration-metadata/allow gh stack link ints",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("verify stdout missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = agentHooksEval([]string{"--policy-dir", policyDir, "--command", "git push origin main"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("eval code = %d want 2 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "git push") {
		t.Fatalf("eval stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = agentHooksEval([]string{"--policy-dir", policyDir, "--command", "gh pr create --head feature --title ok"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("allow eval code = %d stderr=%s", code, stderr.String())
	}
}

func TestAgentHooksEvalReadsHookEvent(t *testing.T) {
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

	input := `{"tool_input":{"command":"gh stack link x 456"}}`
	var stdout, stderr bytes.Buffer
	code := agentHooksEval([]string{"--policy-dir", policyDir}, strings.NewReader(input), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("eval code = %d want 2 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "gh stack link") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAgentHooksEvalInvalidHookEventFailsClosed(t *testing.T) {
	home := t.TempDir()
	policyDir := filepath.Join(home, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := agentHooksEval([]string{"--policy-dir", policyDir}, strings.NewReader(`{"tool_input":{}}`), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("eval code = %d want 2 stderr=%s", code, stderr.String())
	}
}

func TestAgentHooksApplyUsesPolicyDirInHookCommand(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--policy-dir") || !strings.Contains(string(b), policyDir) {
		t.Fatalf("settings = %s", string(b))
	}
}

func TestAgentHooksApplyTargetsRegisteredAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "caller-codex"))
	configPath := filepath.Join(home, ".config", "quota", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "claudeAccounts": [{"key": "claude-2", "configDir": "~/.claude-2"}],
  "codexAccounts": [{"key": "codex-2", "home": "~/.codex-2"}]
}`), 0o600); err != nil {
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

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "all", "--binary", binary, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var report struct {
		Hooks []agenthooks.HookPlan `json:"hooks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"claude/claude":   canonicalTestPath(t, filepath.Join(home, ".claude", "settings.json")),
		"claude/claude-2": canonicalTestPath(t, filepath.Join(home, ".claude-2", "settings.json")),
		"codex/codex":     canonicalTestPath(t, filepath.Join(home, ".codex", "hooks.json")),
		"codex/codex-2":   canonicalTestPath(t, filepath.Join(home, ".codex-2", "hooks.json")),
	}
	if len(report.Hooks) != len(want) {
		t.Fatalf("hooks=%+v want %d", report.Hooks, len(want))
	}
	for _, hook := range report.Hooks {
		key := hook.Runtime + "/" + hook.Account
		if hook.Path != want[key] {
			t.Fatalf("hook %s path=%q want %q; hooks=%+v", key, hook.Path, want[key], report.Hooks)
		}
		if !hook.Present {
			t.Fatalf("hook was not present: %+v", hook)
		}
		detected := agenthooks.DetectPath(hook.Runtime, hook.Path, binary, policyDir)
		if !detected.Present {
			t.Fatalf("installed hook not detected for %s at %s: %+v", key, hook.Path, detected)
		}
	}
	for _, unexpected := range []string{
		filepath.Join(home, "caller-claude", "settings.json"),
		filepath.Join(home, "caller-codex", "hooks.json"),
	} {
		if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
			t.Fatalf("caller environment path was touched: %s err=%v", unexpected, err)
		}
	}
}

func TestAgentHooksApplyInvalidAccountWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".config", "quota", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "claudeAccounts": [{"key": "claude-2", "configDir": "~/.claude"}]
}`), 0o600); err != nil {
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

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("apply code = %d want 1 stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "same directory") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("apply wrote settings despite invalid account config: %v", err)
	}
}

func TestAgentHooksDoctorUsesBinaryFlag(t *testing.T) {
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
	binary := filepath.Join(home, "bin", "quota-cli")
	writeTestExecutable(t, binary)

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = agentHooksDoctor([]string{"--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "ok claude hook") {
		t.Fatalf("doctor stdout = %q", stdout.String())
	}
}

func TestAgentHooksApplyRejectsMissingBinary(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "claude", "--binary", filepath.Join(home, "missing-quota-cli")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("apply code = %d want 1", code)
	}
	if !strings.Contains(stderr.String(), "hook binary") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAgentHooksDoctorRejectsMissingBinary(t *testing.T) {
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
	binary := filepath.Join(home, "bin", "quota-cli")
	if _, err := agenthooks.Apply("claude", binary, policyDir); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := agentHooksDoctor([]string{"--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor code = %d want 1", code)
	}
	if !strings.Contains(stdout.String(), "broken claude hook") || !strings.Contains(stdout.String(), "hook binary") {
		t.Fatalf("stdout = %q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAgentHooksDoctorChecksStoredBinaryWhenFlagOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	goodBin := filepath.Join(home, "good-bin")
	writeTestExecutable(t, filepath.Join(goodBin, "quota-cli"))
	t.Setenv("PATH", goodBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	policyDir := filepath.Join(home, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	missingBinary := filepath.Join(home, "missing-bin", "quota-cli")
	if _, err := agenthooks.Apply("claude", missingBinary, policyDir); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := agentHooksDoctor([]string{"--policy-dir", policyDir, "--runtime", "claude"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor code = %d want 1", code)
	}
	if !strings.Contains(stdout.String(), "broken claude hook") || !strings.Contains(stdout.String(), missingBinary) {
		t.Fatalf("stdout = %q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAgentHooksApplyNormalizesRelativePolicyDir(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy("policies", policy, false); err != nil {
		t.Fatal(err)
	}
	binary := writeTestExecutable(t, filepath.Join(home, "bin", "quota-cli"))

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", "policies", "--runtime", "claude", "--binary", binary}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "policies"); !strings.Contains(string(b), want) {
		t.Fatalf("settings = %s, want policy dir %s", string(b), want)
	}
}

func TestAgentHooksApplyNormalizesRelativeBinary(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	policyDir := filepath.Join(cwd, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, filepath.Join(cwd, "quota-cli"))

	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--policy-dir", policyDir, "--runtime", "claude", "--binary", "./quota-cli"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cwd, "quota-cli")
	if !strings.Contains(string(b), want) {
		t.Fatalf("settings = %s, want absolute binary %s", string(b), want)
	}
	if strings.Contains(string(b), `"./quota-cli agent`) {
		t.Fatalf("settings still stores the relative binary: %s", string(b))
	}
}

func TestAgentHooksVerifyRejectsInvalidGlob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	policyDir := filepath.Join(home, "policies")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"id":"bad-glob","enabled":true,"rules":[{"id":"bad-glob-rule","effect":"deny","match":{"argv":["git",{"glob":"push["}]}}]}`
	if err := os.WriteFile(filepath.Join(policyDir, "bad-glob.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"verify", "--policy-dir", policyDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("verify code = %d want 1 stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "glob") {
		t.Fatalf("verify stderr = %q, want glob error", stderr.String())
	}
}

func TestAgentHooksVerifyWithoutPoliciesFails(t *testing.T) {
	policyDir := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"verify", "--policy-dir", policyDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("verify code = %d want 1", code)
	}
	if !strings.Contains(stderr.String(), "no enabled agent hook policies found") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func writeTestExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentHooksEvalWithoutEnabledPoliciesFailsClosed(t *testing.T) {
	home := t.TempDir()
	policyDir := filepath.Join(home, "policies")
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := agentHooksEval([]string{"--policy-dir", policyDir, "--command", "git push origin main"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("eval code = %d want 2", code)
	}
	if !strings.Contains(stderr.String(), "no enabled agent hook policies found") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAgentHooksDoctorMissingHookFails(t *testing.T) {
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
	var stdout, stderr bytes.Buffer
	code := agentHooksDoctor([]string{"--policy-dir", policyDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor code = %d want 1", code)
	}
	if !strings.Contains(stdout.String(), "missing claude hook") || !strings.Contains(stdout.String(), "missing codex hook") {
		t.Fatalf("doctor stdout = %q", stdout.String())
	}
}

func TestAgentHooksDiagnosticOutputContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude"))
	policyDir := filepath.Join(home, "policies")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	run := func(command string, extra ...string) int {
		stdout.Reset()
		stderr.Reset()
		args := []string{command, "--policy-dir", policyDir, "--runtime", "claude", "--binary", binary}
		return runAgentHooks(append(args, extra...), &stdout, &stderr)
	}
	policy, err := agenthooks.Preset(agenthooks.PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"disableAllHooks":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run("apply"); code != 1 || !strings.Contains(stderr.String(), "saved") {
		t.Fatalf("apply code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	if code := run("doctor"); code != 1 || !strings.Contains(stdout.String(), "blocked claude") {
		t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	for _, command := range []string{"doctor", "plan"} {
		want := 1
		if command == "plan" {
			want = 0
		}
		if code := run(command, "--json"); code != want {
			t.Fatalf("%s code=%d stderr=%s", command, code, &stderr)
		}
		var report struct {
			Hooks []agenthooks.HookPlan `json:"hooks"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Hooks) != 1 || !report.Hooks[0].Present || len(report.Hooks[0].Reasons) == 0 {
			t.Fatalf("%s report=%s", command, &stdout)
		}
	}
	if err := os.WriteFile(path, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run("doctor"); code != 1 || !strings.Contains(stdout.String(), "error claude") || strings.Contains(stdout.String(), "missing claude") {
		t.Fatalf("parse error misreported: code=%d stdout=%s", code, &stdout)
	}
	if code := run("doctor", "--json"); code != 1 {
		t.Fatalf("code=%d", code)
	}
	var report struct {
		Hooks []agenthooks.HookPlan `json:"hooks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Hooks) != 1 || report.Hooks[0].Error == "" {
		t.Fatalf("report=%s", &stdout)
	}
}

func TestAgentHooksApplyAllPreservesPartialDiagnostics(t *testing.T) {
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
	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings, []byte(`{"disableAllHooks":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runAgentHooks([]string{"apply", "--runtime", "all", "--binary", binary, "--policy-dir", policyDir, "--json"}, &stdout, &stderr)
	var report struct {
		Hooks  []agenthooks.HookPlan `json:"hooks"`
		Errors []string              `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if code != 1 || len(report.Errors) != 1 || len(report.Hooks) != 2 {
		t.Fatalf("code=%d report=%s stderr=%s", code, &stdout, &stderr)
	}
	for _, hook := range report.Hooks {
		if !hook.Present {
			t.Fatalf("installation skipped: %+v", hook)
		}
	}
	if !agenthooks.DetectPath("codex", filepath.Join(home, ".codex", "hooks.json"), binary, policyDir).Present {
		t.Fatal("Codex was not installed")
	}
}

func TestAgentHooksEvalWrapperOptionTablesFailClosed(t *testing.T) {
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
	for _, tt := range []struct {
		command string
		code    int
	}{
		{"exec -a quota git push", 2},
		{"exec -aquota git push", 2},
		{"exec -caquota git push", 2},
		{"exec -cc -l bash -c true", 2},
		{"exec -l git status", 0},
		{"command -pp git push", 2},
		{"command -p git status", 0},
		{`bash +c -e "git push"`, 2},
		{`bash -c -e "git push"`, 2},
		{`bash -h script.sh`, 2},
		{`bash -c "if"`, 2},
		{`env -S "'unterminated"`, 2},
		{`bash -c "git status"`, 0},
	} {
		t.Run(tt.command, func(t *testing.T) {
			input := `{"tool_input":{"command":` + strconv.Quote(tt.command) + `}}`
			var stdout, stderr bytes.Buffer
			code := agentHooksEval([]string{"--policy-dir", policyDir}, strings.NewReader(input), &stdout, &stderr)
			if code != tt.code {
				t.Fatalf("eval code = %d want %d stderr=%s", code, tt.code, stderr.String())
			}
		})
	}
}

func TestAgentHooksEvalUndecidableWrappersWithAllowOnlyPolicy(t *testing.T) {
	policyDir := t.TempDir()
	policy := agenthooks.Policy{
		Version: agenthooks.PolicyVersion,
		ID:      "allow-only",
		Enabled: true,
		Rules: []agenthooks.Rule{{
			ID:     "allow-git-status",
			Effect: agenthooks.EffectAllow,
			Match: agenthooks.Match{Argv: []agenthooks.ArgPattern{
				{Exact: "git"}, {Exact: "status"},
			}},
		}},
	}
	if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		command string
		code    int
	}{
		{`env --unknown git status`, 2},
		{`git --unknown status`, 2},
		{`git commit --unknown`, 2},
		{`git tag -m`, 2},
		{`git branch --format`, 2},
		{`git unknown-helper`, 2},
		{`git "$subcommand"`, 2},
		{`sudo --unknown git status`, 2},
		{`bash --unknown -c "git status"`, 2},
		{`bash -c "if"`, 2},
		{`env -S "'unterminated"`, 2},
		{`bash -c "git status"`, 0},
	} {
		t.Run(tt.command, func(t *testing.T) {
			input := `{"tool_input":{"command":` + strconv.Quote(tt.command) + `}}`
			var stdout, stderr bytes.Buffer
			code := agentHooksEval([]string{"--policy-dir", policyDir}, strings.NewReader(input), &stdout, &stderr)
			if code != tt.code {
				t.Fatalf("eval code = %d want %d stderr=%s", code, tt.code, stderr.String())
			}
		})
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, filepath.Base(path))
}
