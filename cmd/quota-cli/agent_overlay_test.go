package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const overlayHook = "/opt/overlay/hook"

// A spec covering both runtimes with placeholder commands only.
const bothRuntimesSpec = `{
  "version": 1,
  "claude": {
    "hooks": {
      "SessionStart": [ { "command": "/opt/overlay/hook session" } ]
    }
  },
  "codex": {
    "settings": { "project_doc_max_bytes": 32768 },
    "hooks": {
      "SessionStart": [ { "command": "/opt/overlay/hook codex-session", "additionalContextLimit": 0 } ]
    }
  }
}
`

func writeOverlaySpec(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "agent-overlay.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentOverlayInitForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spec := filepath.Join(home, "overlay.json")

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"init", "--spec", spec}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), spec) {
		t.Fatalf("init stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAgentOverlay([]string{"init", "--spec", spec}, &stdout, &stderr); code != 1 {
		t.Fatalf("second init code = %d want 1 (refuse existing)", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAgentOverlay([]string{"init", "--spec", spec, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("forced init code = %d stderr=%s", code, stderr.String())
	}
}

func TestAgentOverlayMissingSpecFailsForAllCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	missing := filepath.Join(home, "does-not-exist.json")

	for _, args := range [][]string{
		{"plan", "--spec", missing},
		{"apply", "--spec", missing},
		{"doctor", "--spec", missing},
		{"verify", "--spec", missing},
	} {
		var stdout, stderr bytes.Buffer
		code := runAgentOverlay(args, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("%v code = %d want 1", args, code)
		}
		if !strings.Contains(stderr.String(), "not found") {
			t.Fatalf("%v stderr = %q, want not-found", args, stderr.String())
		}
	}
}

func TestAgentOverlayUnknownVersionFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spec := writeOverlaySpec(t, home, `{"version":2}`)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"plan", "--spec", spec}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("plan code = %d want 1", code)
	}
	if !strings.Contains(stderr.String(), "version 2") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAgentOverlayApplyCodexExplicitFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	spec := writeOverlaySpec(t, home, bothRuntimesSpec)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "codex"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("apply codex code = %d want 1", code)
	}
	if !strings.Contains(stderr.String(), "codex apply is not supported") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	// It must not have written the Codex config file.
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("codex config.toml should not exist, err=%v", err)
	}
}

func TestAgentOverlayApplyAllInstallsClaudeAndNotesCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	spec := writeOverlaySpec(t, home, bothRuntimesSpec)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"apply", "--spec", spec}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply all code = %d want 0 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "applied claude overlay") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "codex apply is not supported") {
		t.Fatalf("stderr = %q, want codex note", stderr.String())
	}
	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), overlayHook+" session") {
		t.Fatalf("claude settings missing hook: %s", string(settings))
	}
	// Codex was never written.
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("codex config.toml should not exist")
	}
}

func TestAgentOverlayApplyThenDoctorClaudeInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := writeOverlaySpec(t, home, `{
  "version": 1,
  "claude": { "hooks": { "SessionStart": [ { "command": "/opt/overlay/hook session" } ] } }
}`)

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runAgentOverlay([]string{"doctor", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "claude installed") {
		t.Fatalf("doctor stdout = %q, want installed (doctor never reports enforced)", stdout.String())
	}
}

// P1-2: disableAllHooks:true makes the Claude runtime degraded end-to-end, so
// doctor exits 1 even after the hook was installed.
func TestAgentOverlayDoctorClaudeDisableAllHooksExit1(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := writeOverlaySpec(t, home, `{
  "version": 1,
  "claude": { "hooks": { "SessionStart": [ { "command": "/opt/overlay/hook session" } ] } }
}`)

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	// Turn off all hooks at the settings root after install.
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	root["disableAllHooks"] = true
	out, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, out, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := runAgentOverlay([]string{"doctor", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor code = %d want 1 stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "claude degraded") ||
		!strings.Contains(stdout.String(), "disableAllHooks") {
		t.Fatalf("stdout = %q, want degraded with disableAllHooks cause", stdout.String())
	}
}

func TestAgentOverlayDoctorCodexDegradedPrintsSnippet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	spec := writeOverlaySpec(t, home, bothRuntimesSpec)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"doctor", "--spec", spec, "--runtime", "codex"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor codex code = %d want 1 stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "codex degraded") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[[hooks.SessionStart]]") {
		t.Fatalf("snippet not printed: %s", stdout.String())
	}
}

// P1-2/verify: a runtime whose entries are installed and whose verify command
// exits 0 is enforced; unconfigured runtimes are skipped, so verify passes.
func TestAgentOverlayVerifyRuntimeEnforced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	spec := writeOverlaySpec(t, home, `{
  "version": 1,
  "claude": { "hooks": { "SessionStart": [ { "command": "/opt/overlay/hook session" } ] } },
  "verify": { "claude": { "command": ["/usr/bin/true"] } }
}`)

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := runAgentOverlay([]string{"verify", "--spec", spec}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code = %d want 0 stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude enforced") {
		t.Fatalf("stdout = %q, want claude enforced", stdout.String())
	}
}

// verify: an installed runtime with no verify command is degraded, exit 1.
func TestAgentOverlayVerifyMissingCommandDegraded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := writeOverlaySpec(t, home, `{
  "version": 1,
  "claude": { "hooks": { "SessionStart": [ { "command": "/opt/overlay/hook session" } ] } }
}`)

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := runAgentOverlay([]string{"verify", "--spec", spec}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("verify code = %d want 1", code)
	}
	if !strings.Contains(stdout.String(), "live verification not configured") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// verify: an installed runtime whose verify command fails is degraded, exit 1.
func TestAgentOverlayVerifyCommandFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	spec := writeOverlaySpec(t, home, `{
  "version": 1,
  "claude": { "hooks": { "SessionStart": [ { "command": "/opt/overlay/hook session" } ] } },
  "verify": { "claude": { "command": ["/usr/bin/false"] } }
}`)

	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay([]string{"apply", "--spec", spec, "--runtime", "claude"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := runAgentOverlay([]string{"verify", "--spec", spec}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("verify code = %d want 1", code)
	}
	if !strings.Contains(stdout.String(), "claude degraded") ||
		!strings.Contains(stdout.String(), "live verification: exit 1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAgentOverlayVerifyNoRuntimeConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spec := writeOverlaySpec(t, home, `{"version":1}`)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"verify", "--spec", spec}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code = %d want 0 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude unconfigured") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// P2-5: a Codex setting whose value differs is reported as a replace instruction,
// and the mismatched key never appears in the add snippet.
func TestAgentOverlayDoctorCodexValueMismatchReplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Setting mismatches; the hook is already present so only the value differs.
	config := "project_doc_max_bytes = 100\n\n[[hooks.SessionStart]]\n\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"/opt/overlay/hook codex-session\"\nadditionalContextLimit = 0\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := writeOverlaySpec(t, home, bothRuntimesSpec)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"doctor", "--spec", spec, "--runtime", "codex"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor codex code = %d want 1 stdout=%s", code, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "replace the value of project_doc_max_bytes with 32768") {
		t.Fatalf("missing replace instruction: %q", out)
	}
	if strings.Contains(out, "add to ") {
		t.Fatalf("value mismatch must not produce an add snippet: %q", out)
	}
}

func TestAgentOverlayPlanShowsStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	spec := writeOverlaySpec(t, home, bothRuntimesSpec)

	var stdout, stderr bytes.Buffer
	code := runAgentOverlay([]string{"plan", "--spec", spec, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("plan code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "missing"`) {
		t.Fatalf("plan json = %s", stdout.String())
	}
}

func TestAgentOverlayUsageExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runAgentOverlay(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("empty args code = %d want 2", code)
	}
	stderr.Reset()
	if code := runAgentOverlay([]string{"bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown subcommand code = %d want 2", code)
	}
}
