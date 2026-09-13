package overlayruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func trackCaseAlias(t *testing.T, repo, name string) {
	t.Helper()
	path := filepath.Join(repo, name)
	alias := strings.ToLower(name)
	aliasPath := filepath.Join(repo, alias)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(aliasPath)
	if err != nil || !os.SameFile(original, other) {
		t.Skip("filesystem does not alias filename case")
	}
	if err := os.Rename(path, aliasPath); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-f", "--", alias)
	if got := git(t, repo, "ls-files", "--", ":(literal)"+name); got != "" {
		t.Fatalf("canonical literal unexpectedly matched tracked alias: %q", got)
	}
	if got := git(t, repo, "ls-files", "--", ":(icase,literal)"+name); got != alias {
		t.Fatalf("case-insensitive pathspec did not match: %q", got)
	}
}

func TestTrackedLocalSourceCaseAliasNotDelivered(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	original := regressionRead(t, target)
	const body = "private rule in tracked case alias\n"
	write(t, filepath.Join(repo, "AGENTS.local.md"), body)
	trackCaseAlias(t, repo, "AGENTS.local.md")
	for _, operation := range []string{"check", "setup"} {
		code, out, errb := run(t, "", operation, "--runtime=codex", repo)
		if code != 1 || !strings.Contains(out+errb, "tracked") || strings.Contains(out+errb, body) {
			t.Fatalf("%s accepted tracked alias: exit=%d out=%q err=%q", operation, code, out, errb)
		}
	}
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		policy := "codex-session"
		if event == "SubagentStart" {
			policy = "codex-subagent"
		}
		input := `{"cwd":` + quote(linked) + `}`
		if event == "SessionStart" {
			input = `{"cwd":` + quote(linked) + `,"source":"startup"}`
		}
		code, out, errb := run(t, input, "json", event, "AGENTS.md", "-", linked, policy)
		if code != 0 || !strings.Contains(out+errb, "tracked") || strings.Contains(out+errb, strings.TrimSpace(body)) {
			t.Fatalf("%s delivered tracked alias: exit=%d out=%q err=%q", event, code, out, errb)
		}
	}
	if notice := regressionSession(t, linked); !strings.Contains(notice, "tracked") {
		t.Fatalf("Claude tracked alias refusal missing: %q", notice)
	}
	if got := regressionRead(t, target); got != original {
		t.Fatalf("Claude delivered tracked alias: %q", got)
	}
}

func TestTrackedGeneratedCopyCaseAliasPreserved(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	regressionSetup(t, repo)
	target := filepath.Join(linked, "CLAUDE.local.md")
	original := regressionRead(t, target)
	trackCaseAlias(t, linked, "CLAUDE.local.md")
	source := filepath.Join(repo, "AGENTS.local.md")
	write(t, source, "updated source\n")
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
		}
		for _, operation := range []string{"setup", "check"} {
			code, out, errb := run(t, "", operation, "--runtime=claude", repo)
			if code != 1 || !strings.Contains(out+errb, "tracked") {
				t.Fatalf("%s accepted tracked copy alias: exit=%d out=%q err=%q", operation, code, out, errb)
			}
			if got := regressionRead(t, target); got != original {
				t.Fatalf("%s changed tracked copy alias: %q", operation, got)
			}
		}
		if notice := regressionSession(t, linked); !strings.Contains(notice, "tracked") {
			t.Fatalf("tracked copy alias refusal missing: %q", notice)
		}
		if got := regressionRead(t, target); got != original {
			t.Fatalf("session changed tracked copy alias: %q", got)
		}
	}
}
