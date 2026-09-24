package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	write(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n")
	return home
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := resolvePath(filepath.Join(t.TempDir(), "repo"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "AGENTS.md"), "# shared placeholder\n")
	write(t, filepath.Join(dir, ".gitignore"), "AGENTS.local.md\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func hook(t *testing.T, agent string, input map[string]any) (int, string, string) {
	t.Helper()
	b, _ := json.Marshal(input)
	var stdout, stderr bytes.Buffer
	code := RunSessionStartHook(context.Background(), agent, bytes.NewReader(b), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func promptHookContext(t *testing.T, input string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunClaudePromptHook(context.Background(), strings.NewReader(input), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("prompt hook: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() == 0 {
		return ""
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	hook, _ := result["hookSpecificOutput"].(map[string]any)
	context, _ := hook["additionalContext"].(string)
	if len(result) != 1 || len(hook) != 2 || hook["hookEventName"] != "UserPromptSubmit" || context == "" {
		t.Fatalf("missing agent context or unexpected prompt blocking: %s", stdout.String())
	}
	return context
}

func promptInput(dir string) string {
	b, _ := json.Marshal(map[string]string{"cwd": dir})
	return string(b)
}

func TestClaudePromptAllowsAbsentAndOutsideRepository(t *testing.T) {
	testHome(t)
	for _, dir := range []string{newRepo(t), t.TempDir()} {
		if reason := promptHookContext(t, promptInput(dir)); reason != "" {
			t.Fatal(reason)
		}
	}
}

func TestClaudePromptAllowsLargePromptWithValidInstructions(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "valid instructions")
	input, err := json.Marshal(map[string]string{"cwd": repo, "prompt": strings.Repeat("x", (1<<20)+1)})
	if err != nil {
		t.Fatal(err)
	}
	if reason := promptHookContext(t, string(input)); reason != "" {
		t.Fatalf("valid instructions rejected because of prompt size: %s", reason)
	}
}

func TestClaudePromptReportsInspectionErrors(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	for _, input := range []string{"{", "null", "{}", `{"cwd":7}`, promptInput(filepath.Join(repo, "missing"))} {
		if reason := promptHookContext(t, input); reason == "" {
			t.Fatalf("allowed invalid input %q", input)
		}
	}
	t.Run("unsafe Git environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
		if reason := promptHookContext(t, promptInput(repo)); reason == "" {
			t.Fatal("unsafe environment allowed")
		}
	})
	t.Run("missing Git executable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if reason := promptHookContext(t, promptInput(repo)); reason == "" {
			t.Fatal("Git failure treated as outside repository")
		}
	})
	t.Run("corrupt Git directory", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "invalid gitfile")
		if reason := promptHookContext(t, promptInput(dir)); reason == "" {
			t.Fatal("corrupt repository allowed")
		}
	})
}

func TestClaudePromptReportsInvalidSource(t *testing.T) {
	testHome(t)
	for _, kind := range []string{"symlink", "directory", "utf8", "nul", "oversize", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			repo := newRepo(t)
			local := filepath.Join(repo, "AGENTS.local.md")
			switch kind {
			case "symlink":
				if err := os.Symlink("AGENTS.md", local); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(local, 0o755); err != nil {
					t.Fatal(err)
				}
			case "utf8":
				write(t, local, "\xff")
			case "nul":
				write(t, local, "a\x00b")
			case "oversize":
				write(t, local, strings.Repeat("x", maxInstructionBytes+1))
			case "unreadable":
				if os.Getuid() == 0 {
					t.Skip("root can read mode 000 files")
				}
				write(t, local, "body")
				if err := os.Chmod(local, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(local, 0o644) })
			}
			if reason := promptHookContext(t, promptInput(repo)); !strings.Contains(reason, local) {
				t.Fatalf("source error lost: %q", reason)
			}
		})
	}
}

func TestClaudePromptPreservesDirectivesBeforeLongInspectionError(t *testing.T) {
	testHome(t)
	t.Setenv("GIT_PAGER", "cat")
	repo := newRepo(t)
	bin := t.TempDir()
	detail := strings.Repeat("diagnostic ", 1200)
	path := filepath.Join(bin, "git")
	write(t, path, "#!/bin/sh\nprintf '%s' '"+detail+"' >&2\nexit 128\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	context := promptHookContext(t, promptInput(repo))
	if !strings.Contains(context, strings.TrimSpace(detail)) {
		t.Fatal("inspection error lost")
	}
	preview := context[:1000]
	for _, directive := range []string{"instruction check failed", "Do not perform", "or call tools", "explain", "start a new session", "Do not suggest splitting, truncating, or bypassing", "Leave instruction files unchanged"} {
		if !strings.Contains(preview, directive) {
			t.Fatalf("directive %q lost behind long error", directive)
		}
	}
}

func TestClaudePromptReadsCurrentPrimaryWithoutCleanup(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	write(t, filepath.Join(linked, "AGENTS.local.md"), strings.Repeat("x", 10001))
	legacy := map[string]string{"AGENTS.override.md": "generated"}
	record := recordLegacy(t, repo, legacy)
	write(t, filepath.Join(repo, "AGENTS.override.md"), "generated")
	for _, body := range []string{"valid", strings.Repeat("x", 9985), "fixed"} {
		write(t, filepath.Join(repo, "AGENTS.local.md"), body)
		reported := promptHookContext(t, promptInput(linked)) != ""
		if reported != (len(body) > 9984) {
			t.Fatalf("current primary not checked: reported=%t size=%d", reported, len(body))
		}
	}
	for _, path := range []string{record, filepath.Join(repo, "AGENTS.override.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prompt hook cleaned %s: %v", path, err)
		}
	}
}

func additionalContext(t *testing.T, stdout string) string {
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
	if out.Hook["hookEventName"] != "SessionStart" {
		t.Fatalf("hook event = %q", out.Hook["hookEventName"])
	}
	return out.Hook["additionalContext"]
}

func placeholderBody(lines int) string {
	var b strings.Builder
	for n := 0; n < lines; n++ {
		switch n % 4 {
		case 0:
			fmt.Fprintf(&b, "- rule %04d: %s\n", n, strings.Repeat("placeholder ", 5))
		case 1:
			fmt.Fprintf(&b, "- 규칙 %04d: %s\n", n, strings.Repeat("자리표시자 ", 6))
		case 2:
			fmt.Fprintf(&b, "- item %04d: %s\n", n, strings.Repeat("😀", 12))
		default:
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestClaudeSessionStartOutputsWholeBodyOnStartupClearCompact(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	body := placeholderBody(40) + strings.Repeat("긴 줄 😀", 40) + "\r\nlast line"
	write(t, filepath.Join(repo, "AGENTS.local.md"), body)
	for _, source := range []string{"startup", "clear", "compact"} {
		code, stdout, stderr := hook(t, "claude", map[string]any{"cwd": repo, "source": source})
		if code != 0 || stderr != "" || additionalContext(t, stdout) != "AGENTS.local.md\n"+body {
			t.Fatalf("%s: whole body not output: exit=%d stderr=%q", source, code, stderr)
		}
	}
	for _, input := range []map[string]any{{"cwd": repo, "source": "resume"}, {"cwd": repo}, {"cwd": repo, "source": "other"}} {
		if code, stdout, stderr := hook(t, "claude", input); code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("input %v delivered: %d %q %q", input, code, stdout, stderr)
		}
	}
}

func TestCodexSessionStartOutputsWholeBodyOnStartupClearCompact(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	body := placeholderBody(1400)
	write(t, filepath.Join(repo, "AGENTS.local.md"), body)
	for _, source := range []string{"startup", "clear", "compact"} {
		input := map[string]any{"cwd": repo, "source": source}
		code, stdout, stderr := hook(t, "codex", input)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: %d %q", input, code, stderr)
		}
		if got := additionalContext(t, stdout); got != "AGENTS.local.md\n"+body {
			t.Fatalf("%v: context differs from the body (%d bytes)", input, len(got))
		}
	}
	for _, input := range []map[string]any{{"cwd": repo, "source": "resume"}, {"cwd": repo}, {"cwd": repo, "source": "other"}} {
		if code, stdout, stderr := hook(t, "codex", input); code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("input %v delivered: exit=%d stdout bytes=%d stderr=%q", input, code, len(stdout), stderr)
		}
	}
}

func TestClaudeSessionStartReportsContextLimit(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	for _, tc := range []struct {
		name string
		body string
		over bool
	}{
		{"below", strings.Repeat("x", 9983), false},
		{"at", strings.Repeat("x", 9984), false},
		{"above", strings.Repeat("x", 9985), true},
		{"unicode-at", strings.Repeat("😀", 4992), false},
		{"unicode-above", strings.Repeat("😀", 4993), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(repo, "AGENTS.local.md")
			write(t, path, tc.body)
			for _, agent := range []string{"claude", "codex"} {
				for _, source := range []string{"startup", "clear", "compact"} {
					code, stdout, stderr := hook(t, agent, map[string]any{"cwd": repo, "source": source})
					if code != 0 || stderr != "" {
						t.Fatalf("%s/%s: exit=%d stderr=%q", agent, source, code, stderr)
					}
					got := additionalContext(t, stdout)
					if agent == "claude" && tc.over {
						if !strings.Contains(got, "not delivered in full") || !strings.Contains(got, "Tell the user") || !strings.Contains(got, "10000") || len(got) > 1000 || strings.Contains(got, tc.body) {
							t.Fatalf("%s: missing short delivery error: %.300s", source, got)
						}
					} else if got != "AGENTS.local.md\n"+tc.body {
						t.Fatalf("%s/%s: body changed", agent, source)
					}
				}
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != tc.body {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}

func TestClaudeSessionStartLimitIncludesNotices(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), strings.Repeat("x", 9984))
	write(t, filepath.Join(repo, "AGENTS.override.md"), "user edited\n")
	recordLegacy(t, repo, map[string]string{"AGENTS.override.md": "generated\n"})
	code, stdout, stderr := hook(t, "claude", map[string]any{"cwd": repo, "source": "startup"})
	got := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.Contains(got, "not delivered in full") || !strings.Contains(got, "Tell the user") || len(got) > 1000 || !strings.Contains(got, "AGENTS.override.md differs from the quota-generated content; left in place") {
		t.Fatalf("notice overflow: exit=%d stderr=%q context=%.300s", code, stderr, got)
	}
	code, stdout, stderr = hook(t, "claude", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || stderr != "" || additionalContext(t, stdout) != "AGENTS.local.md\n"+strings.Repeat("x", 9984) {
		t.Fatal("second start repeated cleanup notices or lost the body")
	}
}

func TestClaudeSessionStartNoticeOverflowWithoutLocalFile(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	generated := map[string]string{}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("generated-%03d-%s.md", i, strings.Repeat("x", 100))
		write(t, filepath.Join(repo, name), "user edited\n")
		generated[name] = "generated\n"
	}
	recordLegacy(t, repo, generated)
	code, stdout, stderr := hook(t, "claude", map[string]any{"cwd": repo, "source": "startup"})
	got := additionalContext(t, stdout)
	first, _, _ := strings.Cut(got, "\n")
	if code != 0 || stderr != "" || strings.Contains(first, "AGENTS.local.md") || !strings.Contains(first, "10000") || !strings.Contains(first, "Tell the user") {
		t.Fatalf("misleading overflow error: exit=%d stderr=%q first=%q", code, stderr, first)
	}
	for name := range generated {
		if !strings.Contains(got, name+" differs from the quota-generated content; left in place") {
			t.Fatalf("cleanup notice lost: %s", name)
		}
	}
}

func TestSessionStartDeliversNothingWithoutSourceFileOrRepository(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	outside := t.TempDir()
	for _, agent := range []string{"claude", "codex"} {
		for _, dir := range []string{repo, outside} {
			if code, stdout, stderr := hook(t, agent, map[string]any{"cwd": dir, "source": "startup"}); code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("%s %s: %d %q %q", agent, dir, code, stdout, stderr)
			}
		}
	}
	if code, _, stderr := hook(t, "claude", map[string]any{"source": "startup"}); code != 1 || !strings.Contains(stderr, "cwd") {
		t.Fatalf("missing cwd: %d %q", code, stderr)
	}
	var out bytes.Buffer
	if code := RunSessionStartHook(context.Background(), "claude", strings.NewReader("not json"), &out, &out); code != 1 {
		t.Fatalf("malformed input exit %d", code)
	}
	if code := RunSessionStartHook(context.Background(), "other", strings.NewReader("{}"), &out, &out); code != 2 {
		t.Fatalf("invalid agent exit %d", code)
	}
}

func TestSessionStartReportsSourceProblemsAsNotices(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	local := filepath.Join(repo, "AGENTS.local.md")
	for _, tc := range []struct {
		name  string
		setup func()
		want  string
	}{
		{"symlink", func() {
			write(t, filepath.Join(repo, "target.md"), "linked\n")
			if err := os.Symlink("target.md", local); err != nil {
				t.Fatal(err)
			}
		}, "is a symlink"},
		{"nul", func() { write(t, local, "a\x00b\n") }, "contains a NUL byte"},
		{"utf8", func() { write(t, local, "\xff\xfe\n") }, "is not valid UTF-8"},
		{"size", func() { write(t, local, strings.Repeat("x", maxInstructionBytes+1)) }, "exceeds the"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.RemoveAll(local); err != nil {
				t.Fatal(err)
			}
			tc.setup()
			for _, agent := range []string{"claude", "codex"} {
				code, stdout, stderr := hook(t, agent, map[string]any{"cwd": repo, "source": "startup"})
				context := additionalContext(t, stdout)
				if code != 0 || stderr != "" || !strings.HasPrefix(context, noticePrefix) || !strings.Contains(context, tc.want) || strings.Contains(context, "AGENTS.local.md\n") {
					t.Fatalf("%s: %d %q %q", agent, code, context, stderr)
				}
			}
		})
	}
	if err := os.RemoveAll(local); err != nil {
		t.Fatal(err)
	}
	write(t, local, "unreadable\n")
	if err := os.Chmod(local, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(local, 0o644) })
	if code, stdout, stderr := hook(t, "codex", map[string]any{"cwd": repo, "source": "startup"}); os.Getuid() != 0 && (code != 1 || stdout != "" || !strings.Contains(stderr, "permission denied")) {
		t.Fatalf("read failure was not an error: %d %q %q", code, stdout, stderr)
	}
}

func TestSessionStartReadsPrimaryFromLinkedWorktreeAndBareRepository(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.local.md"), "primary body\n")
	linked := filepath.Join(filepath.Dir(repo), "linked")
	git(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	write(t, filepath.Join(linked, "AGENTS.local.md"), "linked copy must be ignored\n")
	_, stdout, _ := hook(t, "claude", map[string]any{"cwd": filepath.Join(linked), "source": "startup"})
	if got := additionalContext(t, stdout); got != "AGENTS.local.md\nprimary body\n" {
		t.Fatalf("linked worktree context = %q", got)
	}
	bare := filepath.Join(filepath.Dir(repo), "bare.git")
	git(t, repo, "clone", "-q", "--bare", repo, bare)
	bare = resolvePath(bare)
	checkout := filepath.Join(filepath.Dir(repo), "checkout")
	git(t, bare, "worktree", "add", "-q", checkout, "main")
	write(t, filepath.Join(bare, "AGENTS.local.md"), "bare body\n")
	_, stdout, _ = hook(t, "codex", map[string]any{"cwd": checkout, "source": "startup"})
	if got := additionalContext(t, stdout); got != "AGENTS.local.md\nbare body\n" {
		t.Fatalf("bare checkout context = %q", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(bare, "quota-instructions*")); len(entries) != 0 {
		t.Fatalf("hook wrote into the bare repository: %v", entries)
	}
}

func recordLegacy(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	r, err := resolveContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	path, err := statePath(r)
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string]string{}
	for rel, content := range files {
		generated[filepath.Join(repo, rel)] = fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	}
	data, err := json.Marshal(map[string]any{"generated": generated, "generated_modes": map[string]any{}, "local_files": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(data))
	write(t, path+".lock", "")
	return path
}

func TestSessionStartRemovesPreviousGeneratedFilesOnce(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	body := placeholderBody(40)
	write(t, filepath.Join(repo, "AGENTS.local.md"), body)
	generated := map[string]string{
		".claude/rules/quota-instructions.md": "@../../AGENTS.local.md\n",
		"AGENTS.override.md":                  "merged\n",
		".claude/AGENTS.md":                   "@../AGENTS.md\n",
		"tracked-bridge.md":                   "tracked\n",
		"AGENTS.md":                           "# shared placeholder\n",
	}
	for rel, content := range generated {
		write(t, filepath.Join(repo, rel), content)
	}
	write(t, filepath.Join(repo, "AGENTS.override.md"), "user edited\n")
	git(t, repo, "add", "-f", "tracked-bridge.md")
	git(t, repo, "commit", "-q", "-m", "track")
	record := recordLegacy(t, repo, generated)
	code, stdout, stderr := hook(t, "claude", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.HasPrefix(context, noticePrefix) || !strings.HasSuffix(context, "\n\nAGENTS.local.md\n"+body) {
		t.Fatalf("cleanup run: %d %q %q", code, context, stderr)
	}
	for _, rel := range []string{".claude/rules/quota-instructions.md", ".claude/rules", ".claude/AGENTS.md"} {
		if exists(filepath.Join(repo, rel)) {
			t.Fatalf("%s was not removed", rel)
		}
	}
	for _, rel := range []string{"AGENTS.override.md", "tracked-bridge.md", "AGENTS.md", ".claude"} {
		if !exists(filepath.Join(repo, rel)) {
			t.Fatalf("%s was removed", rel)
		}
	}
	for _, want := range []string{"AGENTS.override.md differs from the quota-generated content", "tracked-bridge.md is tracked by Git", repo + "/AGENTS.md is an instruction source"} {
		if !strings.Contains(context, want) {
			t.Fatalf("notice %q missing in %q", want, context)
		}
	}
	if exists(record) || exists(record+".lock") {
		t.Fatal("record was not removed")
	}
	code, stdout, _ = hook(t, "claude", map[string]any{"cwd": repo, "source": "startup"})
	if code != 0 || additionalContext(t, stdout) != "AGENTS.local.md\n"+body {
		t.Fatalf("second run repeated notices: %q", stdout)
	}
}

func TestSessionStartReportsUnreadableRecordWithoutRemovingFiles(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".claude/rules/quota-instructions.md"), "@../../AGENTS.local.md\n")
	record := recordLegacy(t, repo, map[string]string{".claude/rules/quota-instructions.md": "@../../AGENTS.local.md\n"})
	write(t, record, "{not json")
	code, stdout, stderr := hook(t, "codex", map[string]any{"cwd": repo, "source": "startup"})
	context := additionalContext(t, stdout)
	if code != 0 || stderr != "" || !strings.Contains(context, "left in place") || !exists(filepath.Join(repo, ".claude/rules/quota-instructions.md")) || !exists(record) {
		t.Fatalf("unreadable record: %d %q %q", code, context, stderr)
	}
}
