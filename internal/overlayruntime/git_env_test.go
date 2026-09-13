package overlayruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRefusesInheritedGitRepositoryOverrides(t *testing.T) {
	for _, variable := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"} {
		t.Run(variable, func(t *testing.T) {
			foreign := newRepo(t)
			write(t, filepath.Join(foreign, "AGENTS.md"), "# foreign shared\n")
			write(t, filepath.Join(foreign, "AGENTS.local.md"), "foreign private rule\n")
			target := newRepo(t)
			write(t, filepath.Join(target, "AGENTS.md"), "# intended shared\n")
			config := regressionRead(t, filepath.Join(foreign, ".git", "config"))
			value := filepath.Join(foreign, ".git")
			switch variable {
			case "GIT_WORK_TREE":
				value = foreign
			case "GIT_INDEX_FILE":
				value = filepath.Join(foreign, ".git", "index")
			case "GIT_OBJECT_DIRECTORY":
				value = filepath.Join(foreign, ".git", "objects")
			}
			t.Setenv(variable, value)
			code, out, errb := run(t, "", "setup", "--runtime=claude", target)
			if code != 1 || !strings.Contains(out+errb, variable) {
				t.Fatalf("repository override accepted: exit=%d out=%q err=%q", code, out, errb)
			}
			if os.Getenv(variable) != value {
				t.Fatal("caller environment was modified")
			}
			for _, repo := range []string{foreign, target} {
				for _, name := range []string{"CLAUDE.md", "CLAUDE.local.md", ".gitignore"} {
					if _, err := os.Lstat(filepath.Join(repo, name)); !os.IsNotExist(err) {
						t.Fatalf("override caused mutation at %s/%s: %v", repo, name, err)
					}
				}
			}
			if regressionRead(t, filepath.Join(foreign, ".git", "config")) != config {
				t.Fatal("foreign git config changed")
			}
			if regressionRead(t, filepath.Join(foreign, "AGENTS.local.md")) != "foreign private rule\n" {
				t.Fatal("foreign rule changed")
			}
		})
	}
}

func TestFixtureRejectsInheritedGitRepositoryOverride(t *testing.T) {
	foreign := newRepo(t)
	configPath := filepath.Join(foreign, ".git", "config")
	before := regressionRead(t, configPath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestFixtureGitEnvironmentChild$")
	cmd.Env = append(os.Environ(), "QUOTA_TEST_FIXTURE_GIT_ENV=1", "GIT_DIR="+filepath.Join(foreign, ".git"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "refuses repository-local Git environment variables: GIT_DIR") {
		t.Fatalf("fixture accepted a foreign Git directory: %v\n%s", err, output)
	}
	if regressionRead(t, configPath) != before {
		t.Fatal("fixture changed the foreign repository")
	}
}

func TestFixtureGitEnvironmentChild(t *testing.T) {
	if os.Getenv("QUOTA_TEST_FIXTURE_GIT_ENV") != "1" {
		return
	}
	newRepo(t)
}
