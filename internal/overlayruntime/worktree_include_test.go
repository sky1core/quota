package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryChecksWorktreeIncludeCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, patterns, missing string
	}{
		{"absent", "", ".worktreeinclude is missing"},
		{"unrelated", "other.txt\n", "does not include"},
		{"excluded", "AGENTS.md\n!sub/AGENTS.md\n", "sub/AGENTS.md"},
		{"root only", "/AGENTS.md\n", "sub/AGENTS.md"},
		{"included", "AGENTS.md\n", ""},
		{"exact paths", "/AGENTS.md\n/sub/AGENTS.md\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testHome(t)
			repo := newRepo(t)
			git(t, repo, "rm", "--cached", "AGENTS.md")
			write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\n")
			write(t, filepath.Join(repo, "sub", "AGENTS.md"), "nested instructions\n")
			path := filepath.Join(repo, ".worktreeinclude")
			if tc.patterns != "" {
				write(t, path, tc.patterns)
			}
			for _, agent := range []string{"claude", "codex"} {
				status, err := CheckRepository(context.Background(), repo, agent, CheckOptions{})
				if err != nil || len(status.Problems) != 0 {
					t.Fatalf("status = %+v, %v", status, err)
				}
				warnings := strings.Join(status.Warnings, "\n")
				if tc.missing == "" && warnings != "" || tc.missing != "" && !strings.Contains(warnings, tc.missing) {
					t.Fatalf("configuration diagnosis = %q; want %q", warnings, tc.missing)
				}
			}
			body, err := os.ReadFile(path)
			if tc.patterns == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("status created configuration: %v", err)
				}
			} else if err != nil || string(body) != tc.patterns {
				t.Fatalf("status changed configuration: %q %v", body, err)
			}
		})
	}
}

func TestRepositoryReportsUnignoredInstructionAndInvalidInclude(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	git(t, repo, "rm", "--cached", "AGENTS.md")
	path := filepath.Join(repo, ".worktreeinclude")
	write(t, path, "AGENTS.md\n")
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Warnings, "\n"), "not gitignored") {
		t.Fatalf("untracked, unignored instructions reported as copyable: %+v %v", status, err)
	}
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\n")
	write(t, path, "AGENTS.md\x00")
	status, err = CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Warnings, "\n"), "NUL") {
		t.Fatalf("invalid include accepted: %+v %v", status, err)
	}
}

func TestRepositoryReportsWorktreeCreateOverride(t *testing.T) {
	for _, rel := range []string{"account/settings.json", "repo/.claude/settings.json", "repo/.claude/settings.local.json"} {
		t.Run(rel, func(t *testing.T) {
			home := testHome(t)
			repo := newRepo(t)
			path := filepath.Join(home, rel)
			if strings.HasPrefix(rel, "repo/") {
				path = filepath.Join(repo, strings.TrimPrefix(rel, "repo/"))
			}
			write(t, path, `{"hooks":{"WorktreeCreate":[{"hooks":[{"type":"command","command":"custom-creator"}]}]}}`)
			status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{ClaudeConfigDir: filepath.Join(home, "account")})
			if err != nil || !strings.Contains(strings.Join(status.Warnings, "\n"), path+": WorktreeCreate") {
				t.Fatalf("override not reported: %+v %v", status, err)
			}
			status, err = CheckRepository(context.Background(), repo, "codex", CheckOptions{})
			if err != nil || len(status.Warnings) != 0 {
				t.Fatalf("tracked instructions reported as requiring copy configuration: %+v %v", status, err)
			}
		})
	}
}

func TestRepositoryReportsIgnoredDirectoryTraversal(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "private/\n")
	write(t, filepath.Join(repo, "private", "AGENTS.md"), "nested\n")
	include := filepath.Join(repo, ".worktreeinclude")
	for _, pattern := range []string{"AGENTS.md\n", "**/AGENTS.md\n"} {
		write(t, include, pattern)
		status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
		if err != nil || !strings.Contains(strings.Join(status.Warnings, "\n"), "gitignored directory") {
			t.Fatalf("ignored-directory gap hidden: %+v %v", status, err)
		}
	}
	write(t, include, "AGENTS.md\n/private/AGENTS.md\n")
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || len(status.Warnings) != 0 {
		t.Fatalf("explicit ignored-directory file rejected: %+v %v", status, err)
	}
}

func TestRepositoryDistinguishesGitPatternFromClaudeLiteralPath(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "private*/\n")
	write(t, filepath.Join(repo, "private[1]", "AGENTS.md"), "nested\n")
	path := filepath.Join(repo, ".worktreeinclude")
	write(t, path, "/private\\[1\\]/AGENTS.md\n")
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || !strings.Contains(strings.Join(status.Warnings, "\n"), "gitignored directory") {
		t.Fatalf("escaped pattern hid native copy gap: %+v %v", status, err)
	}
	write(t, path, "/private[1]/AGENTS.md\n")
	status, err = CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || len(status.Warnings) != 0 {
		t.Fatalf("native literal path incorrectly rejected: %+v %v", status, err)
	}
}
