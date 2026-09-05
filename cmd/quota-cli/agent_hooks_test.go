package main

import (
	"bytes"
	"os"
	"path/filepath"
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

	stdout.Reset()
	stderr.Reset()
	code = runAgentHooks([]string{"verify", "--policy-dir", policyDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), "FAIL") {
		t.Fatalf("verify stdout contains failure:\n%s", stdout.String())
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
	code = agentHooksEval([]string{"--policy-dir", policyDir, "--command", "gh pr create --title ok"}, strings.NewReader(""), &stdout, &stderr)
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
