package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/config"
)

type callerEnvTargetSnapshot struct {
	ResolvedAccounts     []string `json:"resolvedAccounts"`
	SessionLogs          []string `json:"sessionLogs"`
	SessionLogExplicit   []string `json:"sessionLogExplicit"`
	ModelAccounts        []string `json:"modelAccounts"`
	ModelAccountExplicit []string `json:"modelAccountExplicit"`
	AgentHooks           []string `json:"agentHooks"`
	AgentInstructions    []string `json:"agentInstructions"`
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o700)
}

func TestQueryEntryPointCallerEnvironmentDoesNotChangeProbeTargets(t *testing.T) {
	if os.Getenv("QUOTA_QUERY_ENV_CHILD") == "1" {
		os.Args = []string{"quota-cli", "-json", "-timeout", "2"}
		runQuery()
		os.Exit(0)
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, dir := range []string{".claude", ".codex", "claude-extra", "codex-extra"} {
		if err := ensureDir(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/claude-extra"}},
		CodexAccounts:  []config.CodexAccount{{Key: "codex-2", Home: "~/codex-extra"}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(home, "bin")
	writeFakeQuotaCLIs(t, binDir)

	wantClaude := []string{
		quotaTestAccountDir(t, filepath.Join(home, ".claude")),
		quotaTestAccountDir(t, filepath.Join(home, "claude-extra")),
	}
	wantCodex := []string{
		quotaTestAccountDir(t, filepath.Join(home, ".codex")),
		quotaTestAccountDir(t, filepath.Join(home, "codex-extra")),
	}
	sort.Strings(wantClaude)
	sort.Strings(wantCodex)

	for _, tc := range []struct {
		name           string
		claudeEnv      string
		codexEnv       string
		claudeProjects string
		codexSessions  string
		controlEnv     bool
	}{
		{name: "no-agent-env"},
		{name: "claude-env", claudeEnv: filepath.Join(home, "caller-claude")},
		{name: "codex-env", codexEnv: filepath.Join(home, "caller-codex")},
		{name: "unregistered-agent-envs", claudeEnv: filepath.Join(home, "caller-claude"), codexEnv: filepath.Join(home, "caller-codex"), claudeProjects: filepath.Join(home, "caller-projects"), codexSessions: filepath.Join(home, "caller-sessions"), controlEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(filepath.Join(home, ".config", "quota", "quota-cache.json")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			recordDir := filepath.Join(home, "records", tc.name)
			if err := os.MkdirAll(recordDir, 0o755); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"QUOTA_QUERY_ENV_CHILD=1",
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"PATH=" + binDir,
				"QUOTA_TEST_RECORD_DIR=" + recordDir,
			}
			if tc.claudeEnv != "" {
				env = append(env, "CLAUDE_CONFIG_DIR="+tc.claudeEnv)
			}
			if tc.codexEnv != "" {
				env = append(env, "CODEX_HOME="+tc.codexEnv)
			}
			if tc.claudeProjects != "" {
				env = append(env, "CLAUDE_PROJECTS_DIR="+tc.claudeProjects)
			}
			if tc.codexSessions != "" {
				env = append(env, "CODEX_SESSIONS_DIR="+tc.codexSessions)
			}
			if tc.controlEnv {
				env = append(env,
					"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
					"CLAUDE_CODE_EFFORT_LEVEL=low",
					"CLAUDE_CODE_SIMPLE=1",
					"CLAUDE_CODE_USE_VERTEX=1",
					"CODEX_SQLITE_HOME="+filepath.Join(home, "caller-sqlite"),
				)
			}
			testBinary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(testBinary, "-test.run=^TestQueryEntryPointCallerEnvironmentDoesNotChangeProbeTargets$")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("query child failed: %v\n%s", err, out)
			}
			var result struct {
				Errors []any `json:"errors"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("query output is not JSON: %v\n%s", err, out)
			}
			if len(result.Errors) != 0 {
				t.Fatalf("query reported errors: %s", out)
			}
			if got := sortedRecordedLines(t, filepath.Join(recordDir, "claude-env")); strings.Join(got, "\n") != strings.Join(wantClaude, "\n") {
				t.Fatalf("Claude child targets = %v, want %v", got, wantClaude)
			}
			if got := sortedRecordedLines(t, filepath.Join(recordDir, "codex-env")); strings.Join(got, "\n") != strings.Join(wantCodex, "\n") {
				t.Fatalf("Codex child targets = %v, want %v", got, wantCodex)
			}
			if got := optionalSortedRecordedLines(t, filepath.Join(recordDir, "claude-control-env")); len(got) != 0 {
				t.Fatalf("Claude child inherited control environment: %v", got)
			}
			if got := optionalSortedRecordedLines(t, filepath.Join(recordDir, "codex-control-env")); len(got) != 0 {
				t.Fatalf("Codex child inherited control environment: %v", got)
			}
		})
	}
}

func writeFakeQuotaCLIs(t *testing.T, binDir string) {
	t.Helper()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeScript := `#!/bin/sh
if [ -n "${CLAUDE_CONFIG_DIR+x}" ] && [ -n "$CLAUDE_CONFIG_DIR" ]; then
	printf '%s\n' "$CLAUDE_CONFIG_DIR" >> "$QUOTA_TEST_RECORD_DIR/claude-env"
else
	(cd "$HOME/.claude" 2>/dev/null && pwd -P || printf '%s\n' "$HOME/.claude") >> "$QUOTA_TEST_RECORD_DIR/claude-env"
fi
[ -n "${CLAUDE_CODE_DISABLE_CLAUDE_MDS+x}" ] && printf '%s\n' CLAUDE_CODE_DISABLE_CLAUDE_MDS >> "$QUOTA_TEST_RECORD_DIR/claude-control-env"
[ -n "${CLAUDE_CODE_EFFORT_LEVEL+x}" ] && printf '%s\n' CLAUDE_CODE_EFFORT_LEVEL >> "$QUOTA_TEST_RECORD_DIR/claude-control-env"
[ -n "${CLAUDE_CODE_SIMPLE+x}" ] && printf '%s\n' CLAUDE_CODE_SIMPLE >> "$QUOTA_TEST_RECORD_DIR/claude-control-env"
[ -n "${CLAUDE_CODE_USE_VERTEX+x}" ] && printf '%s\n' CLAUDE_CODE_USE_VERTEX >> "$QUOTA_TEST_RECORD_DIR/claude-control-env"
printf '%s\n' '{"result":"Current session: 10% used\nCurrent week (all models): 20% used\n","is_error":false}'
`
	codexScript := `#!/bin/sh
printf '%s\n' "$CODEX_HOME" >> "$QUOTA_TEST_RECORD_DIR/codex-env"
[ -n "${CODEX_SQLITE_HOME+x}" ] && printf '%s\n' CODEX_SQLITE_HOME >> "$QUOTA_TEST_RECORD_DIR/codex-control-env"
[ "$1" = app-server ] || exit 2
while IFS= read -r line; do
	case "$line" in
		*'"id":1'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
		*'"id":2'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080}}}}'; exit 0 ;;
	esac
done
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(claudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(codexScript), 0o755); err != nil {
		t.Fatal(err)
	}
}

func sortedRecordedLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines
}

func optionalSortedRecordedLines(t *testing.T, path string) []string {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return sortedRecordedLines(t, path)
}

func TestCallerAgentEnvironmentDoesNotChangeConfiguredTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, dir := range []string{".claude", ".codex", "claude-extra", "codex-extra"} {
		if err := ensureDir(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/claude-extra"}},
		CodexAccounts:  []config.CodexAccount{{Key: "codex-2", Home: "~/codex-extra"}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	callerClaude := filepath.Join(home, "caller-claude")
	callerCodex := filepath.Join(home, "caller-codex")
	for _, dir := range []string{callerClaude, callerCodex} {
		if err := ensureDir(dir); err != nil {
			t.Fatal(err)
		}
	}

	var baseline string
	for _, tc := range []struct {
		name           string
		claudeEnv      string
		codexEnv       string
		claudeProjects string
		codexSessions  string
		controlEnv     bool
	}{
		{name: "no-agent-env"},
		{name: "claude-env", claudeEnv: callerClaude},
		{name: "codex-env", codexEnv: callerCodex},
		{name: "unregistered-agent-envs", claudeEnv: callerClaude, codexEnv: callerCodex, claudeProjects: filepath.Join(home, "caller-projects"), codexSessions: filepath.Join(home, "caller-sessions"), controlEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", tc.claudeEnv)
			t.Setenv("CODEX_HOME", tc.codexEnv)
			t.Setenv("CLAUDE_PROJECTS_DIR", tc.claudeProjects)
			t.Setenv("CODEX_SESSIONS_DIR", tc.codexSessions)
			if tc.controlEnv {
				t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
				t.Setenv("CLAUDE_CODE_EFFORT_LEVEL", "low")
				t.Setenv("CLAUDE_CODE_SIMPLE", "1")
				t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
				t.Setenv("CODEX_SQLITE_HOME", filepath.Join(home, "caller-sqlite"))
			}

			loaded, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := collectCallerEnvTargetSnapshot(loaded)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.MarshalIndent(snapshot, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			if baseline == "" {
				baseline = got
				return
			}
			if got != baseline {
				t.Fatalf("target snapshot changed with caller env\nwant:\n%s\ngot:\n%s", baseline, got)
			}
		})
	}
}

func collectCallerEnvTargetSnapshot(cfg config.Config) (callerEnvTargetSnapshot, error) {
	var snapshot callerEnvTargetSnapshot
	claudeAccounts, claudeErrs := cfg.ResolveAccounts()
	if len(claudeErrs) != 0 {
		return snapshot, fmt.Errorf("claude accounts: %v", claudeErrs)
	}
	for _, account := range claudeAccounts {
		snapshot.ResolvedAccounts = append(snapshot.ResolvedAccounts, "claude/"+account.Key+"="+account.ConfigDir)
	}
	codexAccounts, codexErrs := cfg.ResolveCodexAccounts()
	if len(codexErrs) != 0 {
		return snapshot, fmt.Errorf("codex accounts: %v", codexErrs)
	}
	for _, account := range codexAccounts {
		snapshot.ResolvedAccounts = append(snapshot.ResolvedAccounts, "codex/"+account.Key+"="+account.Home)
	}

	sessionAccounts, err := sessionLogAccounts(cfg, "all", "")
	if err != nil {
		return snapshot, err
	}
	for _, account := range sessionAccounts {
		snapshot.SessionLogs = append(snapshot.SessionLogs, account.Provider+"/"+account.Key+"="+account.Root)
	}
	selectedSessionAccounts, err := sessionLogAccounts(cfg, "claude", "claude-2")
	if err != nil {
		return snapshot, err
	}
	for _, account := range selectedSessionAccounts {
		snapshot.SessionLogExplicit = append(snapshot.SessionLogExplicit, account.Provider+"/"+account.Key+"="+account.Root)
	}

	models, dirs, err := modelAccounts(cfg, modelOptions{agent: "all"})
	if err != nil {
		return snapshot, err
	}
	for i, account := range models {
		snapshot.ModelAccounts = append(snapshot.ModelAccounts, account.Provider+"/"+account.Account+"="+dirs[i])
	}
	models, dirs, err = modelAccounts(cfg, modelOptions{agent: "codex", account: "codex-2"})
	if err != nil {
		return snapshot, err
	}
	for i, account := range models {
		snapshot.ModelAccountExplicit = append(snapshot.ModelAccountExplicit, account.Provider+"/"+account.Account+"="+dirs[i])
	}

	hookTargets, hookErrs := agentHookTargets(cfg, []string{"claude", "codex"})
	if len(hookErrs) != 0 {
		return snapshot, fmt.Errorf("agent hooks: %v", hookErrs)
	}
	for _, target := range hookTargets {
		snapshot.AgentHooks = append(snapshot.AgentHooks, target.runtime+"/"+target.account+"="+target.path)
	}

	installations, installErrs := instructionInstallations("/usr/local/bin/quota-cli", cfg, []string{"claude", "codex"})
	if len(installErrs) != 0 {
		return snapshot, fmt.Errorf("agent instructions: %v", installErrs)
	}
	for _, target := range installations {
		dir := target.configDir
		if dir == "" {
			dir = target.home
		}
		snapshot.AgentInstructions = append(snapshot.AgentInstructions, target.agent+"/"+target.account+"="+dir)
	}

	sort.Strings(snapshot.ResolvedAccounts)
	sort.Strings(snapshot.SessionLogs)
	sort.Strings(snapshot.SessionLogExplicit)
	sort.Strings(snapshot.ModelAccounts)
	sort.Strings(snapshot.ModelAccountExplicit)
	sort.Strings(snapshot.AgentHooks)
	sort.Strings(snapshot.AgentInstructions)
	return snapshot, nil
}
