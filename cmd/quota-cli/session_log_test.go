package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
)

func TestSessionLogAccountsUseConfiguredRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-two"))
	t.Setenv("CLAUDE_PROJECTS_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-two"))
	t.Setenv("CODEX_SESSIONS_DIR", "")

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/claude-two"}},
		CodexAccounts:  []config.CodexAccount{{Key: "codex-2", Home: "~/codex-two"}},
	}
	accounts, err := sessionLogAccounts(cfg, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, account := range accounts {
		got[account.Key] = account.Root
	}
	claudeExtra, err := config.CanonicalAccountDirectory(filepath.Join(home, "claude-two"))
	if err != nil {
		t.Fatal(err)
	}
	codexExtra, err := config.CanonicalAccountDirectory(filepath.Join(home, "codex-two"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"claude":   filepath.Join(quotaTestAccountDir(t, filepath.Join(home, ".claude")), "projects"),
		"claude-2": filepath.Join(claudeExtra, "projects"),
		"codex":    filepath.Join(quotaTestAccountDir(t, filepath.Join(home, ".codex")), "sessions"),
		"codex-2":  filepath.Join(codexExtra, "sessions"),
	}
	for key, root := range want {
		if got[key] != root {
			t.Fatalf("%s root = %q, want %q", key, got[key], root)
		}
	}
}

func TestSessionLogExtraAccountsUseCanonicalRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, sub := range []string{"real/sub", "real/account", "account"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("real/sub", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	wantAccount, err := filepath.EvalSymlinks(filepath.Join(root, "real/account"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, path := range []string{root + "/alias/../account", "alias/../account", "~/alias/../account"} {
		claudeRoot, err := claudeConfigSessionLogRoot(path)
		if err != nil || claudeRoot != filepath.Join(wantAccount, "projects") {
			t.Fatalf("Claude root for %q = %q, %v; want %q", path, claudeRoot, err, filepath.Join(wantAccount, "projects"))
		}
		codexRoot, err := codexHomeSessionLogRoot(path)
		if err != nil || codexRoot != filepath.Join(wantAccount, "sessions") {
			t.Fatalf("Codex root for %q = %q, %v; want %q", path, codexRoot, err, filepath.Join(wantAccount, "sessions"))
		}
	}
}

func TestSessionLogAccountsRejectDuplicateClaudeLogRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude-config"))
	t.Setenv("CLAUDE_PROJECTS_DIR", filepath.Join(home, "caller-projects"))

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude"}},
	}
	_, err := sessionLogAccounts(cfg, "claude", "")
	if err == nil || !strings.Contains(err.Error(), "duplicate key or session log root") {
		t.Fatalf("sessionLogAccounts error = %v, want duplicate log root", err)
	}
}

func TestSessionLogDefaultClaudeIgnoresInheritedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude-config"))
	t.Setenv("CLAUDE_PROJECTS_DIR", "")

	got, err := claudeDefaultSessionLogRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(quotaTestAccountDir(t, filepath.Join(home, ".claude")), "projects")
	if got != want {
		t.Fatalf("default Claude session log root = %q, want %q", got, want)
	}
}

func TestSessionLogAccountsRejectDuplicateCodexLogRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "caller-codex-home"))
	t.Setenv("CODEX_SESSIONS_DIR", filepath.Join(home, "caller-sessions"))

	cfg := config.Config{
		CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex"}},
	}
	_, err := sessionLogAccounts(cfg, "codex", "")
	if err == nil || !strings.Contains(err.Error(), "duplicate key or session log root") {
		t.Fatalf("sessionLogAccounts error = %v, want duplicate log root", err)
	}
}

func TestSessionLogDefaultCodexIgnoresInheritedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "caller-codex-home"))
	t.Setenv("CODEX_SESSIONS_DIR", "")

	got, err := codexDefaultSessionLogRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(quotaTestAccountDir(t, filepath.Join(home, ".codex")), "sessions")
	if got != want {
		t.Fatalf("default Codex session log root = %q, want %q", got, want)
	}
}

func TestSessionLogSearchFindsConfiguredAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_PROJECTS_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_SESSIONS_DIR", "")

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/claude-two"}},
		CodexAccounts:  []config.CodexAccount{{Key: "codex-2", Home: "~/codex-two"}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	writeSessionLog(t, filepath.Join(home, "claude-two", "projects", "project-a", "claude-session.jsonl"), []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"find needle in claude extra account"}]}}`,
	})
	writeSessionLog(t, filepath.Join(home, "codex-two", "sessions", "2026", "08", "31", "codex-session.jsonl"), []string{
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex extra account also has needle"}]}}`,
	})

	var stdout, stderr bytes.Buffer
	code := sessionLogSearch([]string{"-agent", "all", "-limit", "5", "needle"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"claude-2", "codex-2", "needle"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSessionLogSearchExcludesToolsByDefault(t *testing.T) {
	path := writeSessionLog(t, filepath.Join(t.TempDir(), "session.jsonl"), []string{
		`{"type":"tool_result","content":[{"type":"text","text":"hidden needle from tool output"}]}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"visible answer"}]}}`,
	})
	records := []sessionLogRecord{{Provider: "claude", Account: "claude", Path: path, UpdatedAt: time.Now()}}

	hits, err := searchSessionLogRecords(records, "needle", 10, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("tool-only query should be hidden by default: %+v", hits)
	}

	hits, err = searchSessionLogRecords(records, "needle", 10, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Role != "tool_result" {
		t.Fatalf("include-tools hits = %+v, want one tool_result hit", hits)
	}
}

func TestSessionLogSearchIncludesToolCallObjects(t *testing.T) {
	path := writeSessionLog(t, filepath.Join(t.TempDir(), "session.jsonl"), []string{
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":{"cmd":"grep hidden-needle placeholder.txt"}}}`,
	})
	records := []sessionLogRecord{{Provider: "codex", Account: "codex", Path: path, UpdatedAt: time.Now()}}

	hits, err := searchSessionLogRecords(records, "hidden-needle", 10, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("tool call object should be hidden by default: %+v", hits)
	}

	hits, err = searchSessionLogRecords(records, "hidden-needle", 10, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Role != "function_call" || !strings.Contains(hits[0].Snippet, "hidden-needle") {
		t.Fatalf("include-tools object hits = %+v, want function_call hit", hits)
	}
}

func TestSessionLogSearchSkipsLargeToolLineWithoutScannerFailure(t *testing.T) {
	path := writeSessionLog(t, filepath.Join(t.TempDir(), "session.jsonl"), []string{
		`{"type":"tool_result","content":"` + strings.Repeat("x", 17*1024*1024) + `"}`,
		`{"type":"assistant","message":{"role":"assistant","content":"visible needle after large tool line"}}`,
	})
	records := []sessionLogRecord{{Provider: "claude", Account: "claude", Path: path, UpdatedAt: time.Now()}}

	hits, err := searchSessionLogRecords(records, "needle", 10, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Line != 2 {
		t.Fatalf("hits after large tool line = %+v, want line 2", hits)
	}
}

func TestSessionLogRejectsNonPositiveBounds(t *testing.T) {
	tests := []struct {
		name string
		run  func(io.Writer) int
		want string
	}{
		{
			name: "list limit zero",
			run:  func(stderr io.Writer) int { return sessionLogList([]string{"--limit=0"}, io.Discard, stderr) },
			want: "--limit must be >= 1",
		},
		{
			name: "search limit zero",
			run: func(stderr io.Writer) int {
				return sessionLogSearch([]string{"needle", "--limit=0"}, io.Discard, stderr)
			},
			want: "--limit must be >= 1",
		},
		{
			name: "show tail zero",
			run: func(stderr io.Writer) int {
				return sessionLogShow([]string{"session.jsonl", "--tail=0"}, io.Discard, stderr)
			},
			want: "--tail must be >= 1",
		},
		{
			name: "search max chars negative",
			run: func(stderr io.Writer) int {
				return sessionLogSearch([]string{"needle", "--max-chars=-1"}, io.Discard, stderr)
			},
			want: "--max-chars must be >= 0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := tc.run(&stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestSessionLogShowTailAndMaxChars(t *testing.T) {
	path := writeSessionLog(t, filepath.Join(t.TempDir(), "session.jsonl"), []string{
		`{"type":"user","message":{"role":"user","content":"first message"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second message with many characters"}]}}`,
		`{"type":"user","message":{"role":"user","content":"third message"}}`,
	})

	messages, err := readSessionLogMessages(path, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Line != 2 || messages[1].Line != 3 {
		t.Fatalf("tail messages = %+v, want lines 2 and 3", messages)
	}
	limited := limitSessionLogMessageChars(messages, 6)
	if !strings.HasSuffix(limited[0].Text, "...") || len([]rune(limited[0].Text)) > 9 {
		t.Fatalf("message was not capped: %q", limited[0].Text)
	}
}

func TestSessionLogShowAcceptsFlagsAfterRef(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_PROJECTS_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_SESSIONS_DIR", "")

	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/claude-two"}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	writeSessionLog(t, filepath.Join(home, "claude-two", "projects", "project-a", "claude-session.jsonl"), []string{
		`{"type":"user","message":{"role":"user","content":"first message"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second message with many characters"}]}}`,
	})

	var stdout, stderr bytes.Buffer
	code := sessionLogShow([]string{"claude-session.jsonl", "--account=claude-2", "--tail=1", "--max-chars=6"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "first message") {
		t.Fatalf("tail flag after ref was not applied:\n%s", out)
	}
	if !strings.Contains(out, "second...") {
		t.Fatalf("max-chars flag after ref was not applied:\n%s", out)
	}
}

func TestResolveSessionLogRefPrefersExactPath(t *testing.T) {
	dir := t.TempDir()
	exact := filepath.Join(dir, "session.jsonl")
	other := filepath.Join(dir, "nested", "session.jsonl.copy.jsonl")
	records := []sessionLogRecord{
		{Provider: "claude", Account: "claude", Path: exact, UpdatedAt: time.Now()},
		{Provider: "codex", Account: "codex", Path: other, UpdatedAt: time.Now()},
	}

	got, ok := resolveSessionLogRef(records, exact, &bytes.Buffer{})
	if !ok {
		t.Fatal("exact path should resolve")
	}
	if got.Path != exact {
		t.Fatalf("resolved path = %q, want %q", got.Path, exact)
	}
}

func writeSessionLog(t *testing.T, path string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
