package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeIncludeSetupCreatesOnlyRequiredPublicPaths(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	git(t, repo, "rm", "--cached", "AGENTS.md")
	write(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\nprivate*/\n*.local.md\n")
	write(t, filepath.Join(repo, "private[1]", "AGENTS.md"), "nested\n")
	write(t, filepath.Join(repo, "AGENTS.local.md"), "local\n")
	result, err := SetupWorktreeInclude(context.Background(), repo, true)
	if err != nil || result.Applied || len(result.Added) != 3 {
		t.Fatalf("plan = %+v %v", result, err)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("dry-run created configuration: %v", err)
	}
	result, err = SetupWorktreeInclude(context.Background(), repo, false)
	if err != nil || !result.Applied {
		t.Fatalf("apply = %+v %v", result, err)
	}
	body, _ := os.ReadFile(result.Path)
	if string(body) != "/AGENTS.md\n/private\\[1\\]/AGENTS.md\n/private[1]/AGENTS.md\n" {
		t.Fatalf("incorrect literal paths or local file included: %q", body)
	}
	status, err := CheckRepository(context.Background(), repo, "claude", CheckOptions{})
	if err != nil || len(status.Warnings) != 0 || len(status.Problems) != 0 {
		t.Fatalf("generated configuration is not effective: %+v %v", status, err)
	}
	result, err = SetupWorktreeInclude(context.Background(), repo, false)
	if err != nil || result.Applied || len(result.Added) != 0 {
		t.Fatalf("repeat setup added duplicate paths: %+v %v", result, err)
	}
}

func TestWorktreeIncludeSetupLeavesInvalidInputsUntouched(t *testing.T) {
	for _, invalid := range []string{"unignored", "invalid source", "invalid include", "symlink include"} {
		t.Run(invalid, func(t *testing.T) {
			testHome(t)
			repo := newRepo(t)
			git(t, repo, "rm", "--cached", "AGENTS.md")
			ignore := filepath.Join(repo, ".gitignore")
			write(t, ignore, "AGENTS.md\n")
			path := filepath.Join(repo, ".worktreeinclude")
			original := ".env\n"
			if invalid == "invalid include" {
				original = "invalid\x00"
			}
			write(t, path, original)
			switch invalid {
			case "unignored":
				write(t, ignore, "")
			case "invalid source":
				write(t, filepath.Join(repo, "sub", "AGENTS.md"), "invalid\x00")
			case "symlink include":
				other := filepath.Join(t.TempDir(), "other")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			}
			beforeIgnore, _ := os.ReadFile(ignore)
			result, err := SetupWorktreeInclude(context.Background(), repo, false)
			if err == nil || result.Applied {
				t.Fatalf("invalid input applied: %+v %v", result, err)
			}
			after, readErr := os.ReadFile(path)
			afterIgnore, _ := os.ReadFile(ignore)
			if readErr != nil || string(after) != original || string(beforeIgnore) != string(afterIgnore) {
				t.Fatalf("invalid setup changed configuration: %q %v", after, readErr)
			}
		})
	}
}

func TestWorktreeIncludeSetupDoesNotRequireAbsentOrTrackedSources(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		testHome(t)
		repo := newRepo(t)
		if !tracked {
			git(t, repo, "rm", "AGENTS.md")
		}
		write(t, filepath.Join(repo, "AGENTS.local.md"), "local\n")
		result, err := SetupWorktreeInclude(context.Background(), repo, false)
		if err != nil || result.Applied || len(result.Added) != 0 {
			t.Fatalf("unnecessary setup tracked=%t: %+v %v", tracked, result, err)
		}
		if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
			t.Fatalf("created unused configuration: %v", err)
		}
	}
}

func TestWorktreeIncludeSetupRejectsGitEnvironmentOverride(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	other := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	result, err := SetupWorktreeInclude(context.Background(), repo, false)
	if err == nil || !strings.Contains(err.Error(), "GIT_DIR") || result.Applied {
		t.Fatalf("environment selected a different repository: %+v %v", result, err)
	}
}
