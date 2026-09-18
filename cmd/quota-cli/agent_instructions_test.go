package main

import (
	"bytes"
	"encoding/json"
	"github.com/sky1core/quota/internal/agentinstructions"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func instructionsHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
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

func TestInstructionsRejectsRemovedAndGenericInputs(t *testing.T) {
	for _, args := range [][]string{
		{"setup", "."},
		{"setup", "--shared-source=primary"},
		{"setup", "--local-file=example"},
		{"setup", "--spec=arbitrary.json"},
		{"setup", "--agent=all", "--agent=codex"},
		{"setup", "--agent=other"},
		{"uninstall", "--scope=account"},
		{"uninstall", "."},
		{"status", ".", "extra"},
		{"verify"},
		{"local-file"},
		{"local-file", "add"},
		{"local-file", "add", "--command=echo"},
		{"local-file", "copy", "x"},
		{"local-file", "list", ".", "extra"},
		{"_hook", "--agent=claude", "--event=SessionStart"},
		{"_prepare", "--agent=claude", "--event=SubagentStart"},
		{"_prepare", "--agent=codex", "--event=WorktreeCreate"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "arbitrary.md"},
		{"_prepare", "--agent=codex", "--event=SessionStart", "--command=echo"},
	} {
		code, out, stderr := runInstructions(t, "{}", args...)
		if code != 2 {
			t.Errorf("%q: exit=%d output=%q errors=%q", args, code, out, stderr)
		}
	}
}

func TestInstructionsSetupAndUninstallAccountOnly(t *testing.T) {
	home := instructionsHome(t)
	ignore := filepath.Join(home, ".config", "git", "ignore")
	code, out, stderr := runInstructions(t, "", "setup", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run: %d %s %s", code, out, stderr)
	}
	var report instructionsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	wantChanges := 3
	if _, err := exec.LookPath("codex"); err == nil {
		wantChanges = 4
	}
	if !report.DryRun || report.Plan == nil || len(report.Plan.Changes) != wantChanges || report.GlobalIgnore == nil || report.GlobalIgnore.Path != ignore || len(report.GlobalIgnore.Add) != len(agentinstructions.GlobalIgnoreLines) || report.Applied != nil {
		t.Fatalf("dry-run report = %s", out)
	}
	for _, path := range []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json"), ignore} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run wrote %s", path)
		}
	}
	code, out, stderr = runInstructions(t, "", "setup", "--json")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, stderr)
	}
	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(settings), "_prepare") || !strings.Contains(string(settings), "WorktreeRemove") {
		t.Fatalf("Claude settings = %s (%v)", settings, err)
	}
	hooks, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil || !strings.Contains(string(hooks), "'--agent=codex' '--event=SessionStart'") || strings.Contains(string(hooks), "SubagentStart") {
		t.Fatalf("Codex hooks = %s (%v)", hooks, err)
	}
	wantIgnore := agentinstructions.GlobalIgnoreMarker + "\n" + strings.Join(agentinstructions.GlobalIgnoreLines, "\n") + "\n"
	if got, _ := os.ReadFile(ignore); string(got) != wantIgnore {
		t.Fatalf("global ignore = %q", got)
	}
	settingsInfo, _ := os.Stat(filepath.Join(home, ".claude", "settings.json"))
	code, out, _ = runInstructions(t, "", "setup", "--json")
	if code != 0 {
		t.Fatalf("second setup: %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Applied.Applied) != 0 || report.GlobalIgnore.Changed {
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
	code, out, stderr = runInstructions(t, "", "uninstall", "--agent=claude", "--remove-global-ignore", "--json")
	if code == 0 || !strings.Contains(out, "--remove-global-ignore requires --agent=all") {
		t.Fatalf("single-agent --remove-global-ignore accepted: %d %s %s", code, out, stderr)
	}
	if settings, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !strings.Contains(string(settings), "_prepare") {
		t.Fatal("rejected uninstall changed Claude settings")
	}
	code, out, stderr = runInstructions(t, "", "uninstall", "--agent=claude", "--json")
	if code != 0 {
		t.Fatalf("uninstall claude: %d %s %s", code, out, stderr)
	}
	if got, _ := os.ReadFile(ignore); string(got) != wantIgnore {
		t.Fatalf("single-agent uninstall changed global ignore: %q", got)
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
	report = instructionsReport{}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.GlobalIgnore != nil {
		t.Fatalf("default uninstall touched the global ignore plan: %s", out)
	}
	if got, _ := os.ReadFile(ignore); string(got) != wantIgnore {
		t.Fatalf("default uninstall changed global ignore: %q", got)
	}
	code, out, _ = runInstructions(t, "", "uninstall", "--remove-global-ignore", "--json")
	if code != 0 {
		t.Fatalf("uninstall with ignore removal: %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Applied.Applied) != 0 || report.GlobalIgnore == nil || !report.GlobalIgnore.Changed {
		t.Fatalf("ignore removal report = %s", out)
	}
	if got, _ := os.ReadFile(ignore); string(got) != "" {
		t.Fatalf("global ignore after removal = %q", got)
	}
	code, out, _ = runInstructions(t, "", "uninstall", "--remove-global-ignore", "--json")
	if code != 0 {
		t.Fatalf("second uninstall: %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Applied.Applied) != 0 || report.GlobalIgnore.Changed {
		t.Fatalf("second uninstall rewrote files: %s", out)
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

func TestInstructionsSetupNoGlobalIgnore(t *testing.T) {
	home := instructionsHome(t)
	code, out, stderr := runInstructions(t, "", "setup", "--agent=claude", "--no-global-ignore", "--json")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "git", "ignore")); !os.IsNotExist(err) {
		t.Fatal("--no-global-ignore wrote the global ignore file")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatal("--agent=claude touched the Codex account")
	}
}

func TestInstructionsLocalFileCommands(t *testing.T) {
	instructionsHome(t)
	repo := instructionsRepo(t)
	instructionsWrite(t, filepath.Join(repo, ".gitignore"), "/config/\n")
	instructionsWrite(t, filepath.Join(repo, "config", "app.json"), "{}\n")
	code, out, stderr := runInstructions(t, "", "local-file", "list", repo)
	if code != 0 || !strings.Contains(out, "no registered local files") {
		t.Fatalf("list: %d %s %s", code, out, stderr)
	}
	code, out, stderr = runInstructions(t, "", "local-file", "add", repo, "config/app.json", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, out, stderr)
	}
	var report instructionsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Operation != "local-file add" || strings.Join(report.LocalFiles, ",") != "config/app.json" {
		t.Fatalf("add report = %s", out)
	}
	if code, out, _ := runInstructions(t, "", "local-file", "add", repo, "config/missing.json"); code == 0 || !strings.Contains(out, "error:") {
		t.Fatalf("missing source accepted: %d %s", code, out)
	}
	if code, out, _ := runInstructions(t, "", "local-file", "add", repo, ".gitignore"); code == 0 {
		t.Fatalf(".gitignore accepted: %d %s", code, out)
	}
	code, out, _ = runInstructions(t, "", "local-file", "list", repo)
	if code != 0 || !strings.Contains(out, "local file: config/app.json") {
		t.Fatalf("list after add: %d %s", code, out)
	}
	code, out, _ = runInstructions(t, "", "local-file", "remove", repo, "config/app.json")
	if code != 0 || !strings.Contains(out, "no registered local files") {
		t.Fatalf("remove: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "config", "app.json")); err != nil {
		t.Fatal("source file removed")
	}
	if entries, _ := filepath.Glob(filepath.Join(repo, ".git", "quota-instructions*")); len(entries) != 0 {
		t.Fatalf("registration wrote into .git: %v", entries)
	}
}

func TestInstructionsPrepareEntryPreparesClaudeAndDeliversCodexOnStartup(t *testing.T) {
	home := instructionsHome(t)
	instructionsWrite(t, filepath.Join(home, ".config", "git", "ignore"), "AGENTS.override.md\n.claude/AGENTS.md\n")
	repo := instructionsRepo(t)
	instructionsWrite(t, filepath.Join(repo, "AGENTS.local.md"), "private body\n")
	input := `{"cwd":` + string(mustJSON(repo)) + `,"source":"startup","session_id":"x"}`
	code, out, stderr := runInstructions(t, input, "_prepare", "--agent=claude", "--event=SessionStart")
	if code != 0 || stderr != "" || !strings.Contains(out, "private body") {
		t.Fatalf("startup: %d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "AGENTS.md")); err != nil {
		t.Fatalf("Claude local bridge was not prepared: %v", err)
	}
	code, out, stderr = runInstructions(t, input, "_prepare", "--agent=claude", "--event=SessionStart")
	if code != 0 || out != "" || stderr != "" {
		t.Fatalf("second startup: %d %q %q", code, out, stderr)
	}
	code, out, _ = runInstructions(t, "", "status", repo, "--agent=claude", "--json")
	var report instructionsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Agents) != 1 || report.Agents[0].Repository == nil || len(report.Agents[0].Repository.Problems) != 0 || report.Agents[0].State != "blocked" {
		t.Fatalf("status without account hooks = %s", out)
	}
	if !strings.Contains(strings.Join(report.Agents[0].Repository.Generated, "\n"), ".claude/AGENTS.md") {
		t.Fatalf("status did not report remaining generated files = %s", out)
	}
	if code == 0 {
		t.Fatal("status reported success without the account connection")
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
