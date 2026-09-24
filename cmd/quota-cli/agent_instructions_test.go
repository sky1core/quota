package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agentinstructions"
	"github.com/sky1core/quota/internal/config"
)

func instructionsHome(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	instructionsWrite(t, filepath.Join(binDir, "git"), "#!/bin/sh\nexec "+gitPath+" \"$@\"\n")
	if err := os.Chmod(filepath.Join(binDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", binDir)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	instructionsWrite(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n")
	return home
}

func instructionsWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func instructionsRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func runInstructions(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAgentInstructions(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func hookContext(t *testing.T, stdout string) string {
	t.Helper()
	if stdout == "" {
		return ""
	}
	var out struct {
		Hook map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("hook output %q: %v", stdout, err)
	}
	return out.Hook["additionalContext"]
}

func TestInstructionsRejectsRemovedAndGenericInputs(t *testing.T) {
	for _, args := range [][]string{
		{"setup", "."},
		{"setup", "--no-global-ignore"},
		{"setup", "--local-file=example"},
		{"setup", "--agent=all", "--agent=codex"},
		{"setup", "--agent=other"},
		{"uninstall", "--remove-global-ignore"},
		{"uninstall", "."},
		{"status", ".", "extra"},
		{"verify"},
		{"local-file", "list"},
		{"_hook", "--agent=claude", "--event=SessionStart"},
		{"_prepare", "--agent=claude", "--event=SubagentStart"},
		{"_prepare", "--agent=claude", "--event=WorktreeCreate"},
		{"_prepare", "--agent=claude", "--event=SessionStart", "--part=1"},
		{"_prepare", "--agent=claude", "--event=SessionStart", "--part=8"},
		{"_prepare", "--agent=claude", "--event=SessionStart", "--part=0"},
		{"_prepare", "--agent=claude", "--event=SessionStart", "--part=9"},
		{"_prepare", "--agent=claude", "--event=SessionStart", "--part=x"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "--part=1"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "arbitrary.md"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "--command=echo"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "--claude-config-dir", "/tmp"},
	} {
		code, out, stderr := runInstructions(t, "{}", args...)
		if code != 2 {
			t.Errorf("%q: exit=%d output=%q errors=%q", args, code, out, stderr)
		}
	}
}

func TestInstructionsSetupAndUninstallAccountOnly(t *testing.T) {
	home := instructionsHome(t)
	code, out, stderr := runInstructions(t, "", "setup", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run: %d %s %s", code, out, stderr)
	}
	var report instructionsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Plan == nil || len(report.Plan.Changes) != 3 || report.Applied != nil {
		t.Fatalf("dry-run report = %s", out)
	}
	for _, path := range []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json"), filepath.Join(home, ".config", "git", "ignore")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run wrote %s", path)
		}
	}
	code, out, stderr = runInstructions(t, "", "setup", "--json")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, stderr)
	}
	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(settings), "'--agent=claude' '--event=SessionStart'") != 1 || strings.Contains(string(settings), "--part") {
		t.Fatalf("Claude settings must contain one hook without part arguments: %s", settings)
	}
	for _, removed := range []string{"WorktreeCreate", "WorktreeRemove", "--claude-config-dir", "_hook"} {
		if strings.Contains(string(settings), removed) {
			t.Fatalf("Claude settings contain %s: %s", removed, settings)
		}
	}
	hooks, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil || strings.Count(string(hooks), "'--agent=codex' '--event=SessionStart'") != 1 || !strings.Contains(string(hooks), `"additionalContextLimit": 0`) || strings.Contains(string(hooks), "--codex-home") || strings.Contains(string(hooks), "--part") {
		t.Fatalf("Codex hooks = %s (%v)", hooks, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "git", "ignore")); !os.IsNotExist(err) {
		t.Fatal("setup wrote the global git ignore file")
	}
	settingsInfo, _ := os.Stat(filepath.Join(home, ".claude", "settings.json"))
	code, out, _ = runInstructions(t, "", "setup", "--json")
	if code != 0 {
		t.Fatalf("second setup: %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Applied.Applied) != 0 {
		t.Fatalf("second setup rewrote files: %s", out)
	}
	if again, _ := os.Stat(filepath.Join(home, ".claude", "settings.json")); !again.ModTime().Equal(settingsInfo.ModTime()) {
		t.Fatal("second setup rewrote Claude settings")
	}
	if backups, _ := filepath.Glob(filepath.Join(home, ".claude", "settings.json.bak.*")); len(backups) != 0 {
		t.Fatalf("setup of a new file created backups: %v", backups)
	}
	code, out, _ = runInstructions(t, "", "status", t.TempDir(), "--agent=claude", "--json")
	if code == 0 || !strings.Contains(out, `"state": "blocked"`) || strings.Count(out, "not inside a Git worktree") != 1 {
		t.Fatalf("status outside a repository: %d %s", code, out)
	}
	code, out, stderr = runInstructions(t, "", "uninstall", "--agent=claude", "--json")
	if code != 0 {
		t.Fatalf("uninstall claude: %d %s %s", code, out, stderr)
	}
	if hooks, _ = os.ReadFile(filepath.Join(home, ".codex", "hooks.json")); !strings.Contains(string(hooks), "_prepare") {
		t.Fatal("single-agent uninstall touched the Codex account")
	}
	code, out, stderr = runInstructions(t, "", "uninstall", "--json")
	if code != 0 {
		t.Fatalf("uninstall: %d %s %s", code, out, stderr)
	}
	settings, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	hooks, _ = os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if strings.Contains(string(settings), "_prepare") || strings.Contains(string(hooks), "_prepare") {
		t.Fatalf("uninstall left hooks: %s %s", settings, hooks)
	}
	code, out, _ = runInstructions(t, "", "uninstall", "--json")
	if code != 0 {
		t.Fatalf("second uninstall: %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Applied.Applied) != 0 {
		t.Fatalf("second uninstall rewrote files: %s", out)
	}
}

func TestInstructionsSetupInitializesClaudeAccountsWithoutBlockingInstallation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failDefault bool
		symlinkHome bool
	}{
		{name: "success"},
		{name: "default-fails", failDefault: true},
		{name: "symlink-home", symlinkHome: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := instructionsHome(t)
			if tc.symlinkHome {
				alias := filepath.Join(t.TempDir(), "home")
				if err := os.Symlink(home, alias); err != nil {
					t.Fatal(err)
				}
				home = alias
				t.Setenv("HOME", home)
			}
			repo := instructionsRepo(t)
			t.Chdir(repo)
			extra := filepath.Join(home, ".claude-2")
			if err := config.Save(config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: extra}}}); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(home, "initializations")
			t.Setenv("QUOTA_INIT_RECORD", record)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-account"))
			t.Setenv("CLAUDECODE", "parent-session")
			t.Setenv("ANTHROPIC_API_KEY", "caller-credential")
			if tc.failDefault {
				t.Setenv("QUOTA_INIT_FAIL_DIR", filepath.Join(home, ".claude"))
			}
			cli := filepath.Join(home, "bin", "claude")
			instructionsWrite(t, cli, `#!/bin/sh
target=${CLAUDE_CONFIG_DIR:-"$HOME/.claude"}
[ -f "$target/settings.json" ] || exit 17
printf '%s|%s|%s|%s|%s|%s\n' "$#" "$*" "${CLAUDE_CONFIG_DIR-}" "$PWD" "${CLAUDECODE-unset}" "${ANTHROPIC_API_KEY-unset}" >> "$QUOTA_INIT_RECORD"
printf 'child stdout\n'
printf 'child stderr\n' >&2
[ "$target" != "$QUOTA_INIT_FAIL_DIR" ] || exit 7
`)
			if err := os.Chmod(cli, 0o755); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runInstructions(t, "", "setup", "--agent=claude", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("setup: %d %s %s", code, out, stderr)
			}
			var report struct {
				Notices []string `json:"notices"`
				Error   string   `json:"error"`
			}
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Notices) != 2 || report.Error != "" || !strings.Contains(report.Notices[1], "claude-2") || !strings.Contains(report.Notices[1], "completed") {
				t.Fatalf("account initialization notices: %s", out)
			}
			if tc.failDefault && (!strings.Contains(report.Notices[0], "WARN") || !strings.Contains(report.Notices[0], "exit status 7")) {
				t.Fatalf("initialization failure was not reported: %s", out)
			}
			code, out, stderr = runInstructions(t, "", "setup", "--agent=claude")
			if code != 0 || stderr != "" || !strings.Contains(out, report.Notices[0]) || !strings.Contains(out, report.Notices[1]) {
				t.Fatalf("text setup report: %d %s %s", code, out, stderr)
			}
			resolvedExtra, err := filepath.EvalSymlinks(extra)
			if err != nil {
				t.Fatal(err)
			}
			want := "1|--init-only||" + filepath.Dir(config.Path()) + "|unset|unset\n" +
				"1|--init-only|" + resolvedExtra + "|" + filepath.Dir(config.Path()) + "|unset|unset\n"
			calls, err := os.ReadFile(record)
			if err != nil || string(calls) != want+want {
				t.Fatalf("initialization calls = %q (%v), want %q", calls, err, want+want)
			}
			for _, args := range [][]string{
				{"setup", "--agent=claude", "--dry-run"},
				{"setup", "--agent=codex"},
				{"status", repo, "--agent=claude"},
				{"uninstall", "--agent=claude"},
			} {
				code, out, stderr := runInstructions(t, "", args...)
				if code != 0 {
					t.Fatalf("%v: %d %s %s", args, code, out, stderr)
				}
			}
			if after, err := os.ReadFile(record); err != nil || !bytes.Equal(calls, after) {
				t.Fatalf("non-initializing operations invoked Claude: %q (%v)", after, err)
			}
		})
	}
}

func TestInstructionsSetupMissingClaudeIsAdvisory(t *testing.T) {
	instructionsHome(t)
	code, out, stderr := runInstructions(t, "", "setup", "--agent=claude")
	if code != 0 || stderr != "" || !strings.Contains(out, "WARN") || !strings.Contains(out, "claude CLI not found") || !strings.Contains(out, "saved:") {
		t.Fatalf("missing Claude prevented installation or was not reported: %d %s %s", code, out, stderr)
	}
}

func TestCodexHookInstallChangedDoesNotDependOnFilename(t *testing.T) {
	plan := agentinstructions.InstallPlan{Changes: []agentinstructions.InstallChange{
		{Agent: "codex", Path: "/tmp/shared/codex-hooks-target.json", Changed: true, Operation: "install"},
	}}
	if !codexHookInstallChanged(plan) {
		t.Fatal("codex hook install change was missed for a non-hooks.json target")
	}
}

func TestInstructionsStatusAndUninstallProceedPastInvalidExtraAccount(t *testing.T) {
	home := instructionsHome(t)
	repo := instructionsRepo(t)
	code, out, stderr := runInstructions(t, "", "setup", "--agent=claude", "--json")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, stderr)
	}
	if err := config.Save(config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "work", ConfigDir: "~/.claude-extra"}}}); err != nil {
		t.Fatal(err)
	}

	code, out, stderr = runInstructions(t, "", "status", repo, "--agent=claude", "--json")
	if code == 0 || stderr != "" || !strings.Contains(out, `account key \"work\" must match claude-`) || !strings.Contains(out, `"account": "claude"`) {
		t.Fatalf("status did not inspect valid account and report invalid extra: %d %s %s", code, out, stderr)
	}

	code, out, stderr = runInstructions(t, "", "uninstall", "--agent=claude", "--json")
	if code == 0 || stderr != "" || !strings.Contains(out, `account key \"work\" must match claude-`) {
		t.Fatalf("uninstall did not report invalid extra: %d %s %s", code, out, stderr)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(settings), "_prepare") {
		t.Fatalf("uninstall did not remove valid default account hook: %s", settings)
	}
}

func TestInstructionsPrepareEntryReportsLimitAndStatusReportsSource(t *testing.T) {
	instructionsHome(t)
	repo := instructionsRepo(t)
	var body strings.Builder
	for n := 0; n < 1600; n++ {
		fmt.Fprintf(&body, "- placeholder rule %03d %s\n", n, strings.Repeat("자리표시자 ", 4))
	}
	instructionsWrite(t, filepath.Join(repo, "AGENTS.local.md"), body.String())
	input := `{"cwd":` + string(mustJSON(repo)) + `,"source":"startup","session_id":"x"}`
	for _, agent := range []string{"claude", "codex"} {
		code, out, stderr := runInstructions(t, input, "_prepare", "--agent="+agent, "--event=SessionStart")
		if code != 0 || stderr != "" {
			t.Fatalf("%s: exit=%d stderr=%q", agent, code, stderr)
		}
		got := hookContext(t, out)
		if agent == "claude" {
			if !strings.Contains(got, "not delivered in full") || !strings.Contains(got, "Tell the user") || len(got) > 1000 {
				t.Fatalf("missing delivery error: %.300s", got)
			}
		} else if got != "AGENTS.local.md\n"+body.String() {
			t.Fatal("Codex whole body changed")
		}
	}
	resume := `{"cwd":` + string(mustJSON(repo)) + `,"source":"resume"}`
	for _, agent := range []string{"claude", "codex"} {
		if code, out, stderr := runInstructions(t, resume, "_prepare", "--agent="+agent, "--event=SessionStart"); code != 0 || out != "" || stderr != "" {
			t.Fatalf("%s resume delivered: exit=%d stdout bytes=%d stderr=%q", agent, code, len(out), stderr)
		}
	}
	if entries, _ := filepath.Glob(filepath.Join(repo, ".claude*")); len(entries) != 0 {
		t.Fatalf("hook wrote into the repository: %v", entries)
	}
	code, out, _ := runInstructions(t, "", "status", repo, "--agent=claude", "--json")
	var report instructionsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if code == 0 || len(report.Agents) != 1 || report.Agents[0].State != "blocked" || report.Agents[0].Repository == nil || !report.Agents[0].Repository.LocalPresent {
		t.Fatalf("status without account hooks = %s", out)
	}
	if code, out, stderr := runInstructions(t, "", "setup", "--agent=claude", "--json"); code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, stderr)
	}
	code, out, _ = runInstructions(t, "", "status", repo, "--agent=claude")
	if code == 0 || !strings.Contains(out, "claude: blocked") || !strings.Contains(out, "hook limit") || !strings.Contains(out, filepath.Join(repo, "AGENTS.local.md")) {
		t.Fatalf("status after setup: %d %s", code, out)
	}
	instructionsWrite(t, filepath.Join(repo, "AGENTS.local.md"), "short local instruction\n")
	code, out, stderr := runInstructions(t, input, "_prepare", "--agent=claude", "--event=SessionStart")
	if code != 0 || stderr != "" || hookContext(t, out) != "AGENTS.local.md\nshort local instruction\n" {
		t.Fatalf("short source: %d %q %q", code, out, stderr)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestInstructionsPromptFailureContextEntry(t *testing.T) {
	instructionsHome(t)
	repo := instructionsRepo(t)
	input := `{"cwd":` + string(mustJSON(repo)) + `}`
	for _, tc := range []struct {
		name           string
		body           string
		reportsFailure bool
	}{
		{"at limit", strings.Repeat("x", 9984), false},
		{"over limit", strings.Repeat("x", 9985), true},
		{"unicode at limit", strings.Repeat("😀", 4992), false},
		{"unicode over limit", strings.Repeat("😀", 4993), true},
		{"invalid source", "bad\x00source", true},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instructionsWrite(t, filepath.Join(repo, "AGENTS.local.md"), tc.body)
			code, out, stderr := runInstructions(t, input, "_prepare", "--agent=claude", "--event=UserPromptSubmit")
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if !tc.reportsFailure {
				if out != "" {
					t.Fatalf("allowed input emitted %q", out)
				}
				return
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			hook, _ := result["hookSpecificOutput"].(map[string]any)
			context, _ := hook["additionalContext"].(string)
			if len(result) != 1 || len(hook) != 2 || hook["hookEventName"] != "UserPromptSubmit" || !strings.Contains(context, "instruction check failed") || strings.Contains(context, tc.body) {
				t.Fatalf("missing agent failure context or unexpected body injection: %s", out)
			}
		})
	}
	if code, _, _ := runInstructions(t, input, "_prepare", "--agent=codex", "--event=UserPromptSubmit"); code != 2 {
		t.Fatalf("Codex prompt hook accepted: %d", code)
	}
}
