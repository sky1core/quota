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
)

func runSkills(t *testing.T, args ...string) (int, skillsReport, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := runAgentSkills(args, &out, &stderr)
	var report skillsReport
	if strings.HasPrefix(out.String(), "{") {
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("invalid JSON: %s (%v)", out.String(), err)
		}
	}
	return code, report, out.String() + stderr.String()
}

func skillsCLIHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "caller-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "caller-codex"))
	return home
}

func TestAgentSkillsCLILifecycle(t *testing.T) {
	home := skillsCLIHome(t)
	bin := filepath.Join(home, "bin")
	source := filepath.Join(home, "source")
	instructionsWrite(t, filepath.Join(source, "SKILL.md"), "---\nname: example\ndescription: Synthetic example.\n---\nExample.\n")
	instructionsWrite(t, filepath.Join(source, "asset.txt"), "asset content\n")
	instructionsWrite(t, filepath.Join(bin, "node"), "#!/bin/sh\necho v22.20.0\n")
	instructionsWrite(t, filepath.Join(bin, "npm"), `#!/bin/sh
previous=''
for arg do
  if [ "$previous" = add ]; then source="$arg"; fi
  previous="$arg"
done
skills_dir=".agents/skills"
/bin/mkdir -p "$skills_dir"
/bin/cp -R "$source" "$skills_dir/example"
printf '[{"name":"example","status":"installed","path":"%s/%s/example","scope":"project","agents":["Codex"],"mode":"copy"}]\n' "$PWD" "$skills_dir"
`)
	for _, name := range []string{"node", "npm"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	code, report, output := runSkills(t, "install", source, "--skill", "example", "--json")
	if code != 0 || report.Operation != "install" || len(report.Results) != 3 {
		t.Fatalf("install: %d %s", code, output)
	}
	for _, result := range report.Results[:2] {
		got, err := os.ReadFile(filepath.Join(result.Path, "asset.txt"))
		if err != nil || string(got) != "asset content\n" {
			t.Fatalf("missing asset %s (%v)", result.Path, err)
		}
	}
	t.Setenv("PATH", "")
	if code, report, output := runSkills(t, "list", "--json"); code != 0 || len(report.Results) != 3 {
		t.Fatalf("list without npm: %d %s", code, output)
	}
	if err := os.Remove(report.Results[1].Path); err != nil {
		t.Fatal(err)
	}
	if code, report, output := runSkills(t, "link", "--json"); code != 0 || report.Results[1].Status != "linked" {
		t.Fatalf("link missing account path: %d %s", code, output)
	}
	changedPath := filepath.Join(report.Results[1].Path, "asset.txt")
	instructionsWrite(t, changedPath, "user modification\n")
	if code, report, output := runSkills(t, "list", "--json"); code != 0 || report.Results[0].Status != "source" || report.Results[1].Status != "linked" {
		t.Fatalf("list edited skill: %d %s", code, output)
	}
	if code, report, output := runSkills(t, "remove", "--json", "example"); code != 0 || len(report.Results) != 3 {
		t.Fatalf("remove without npm: %d %s", code, output)
	} else if got, err := os.ReadFile(filepath.Join(report.Results[0].BackupPath, "asset.txt")); err != nil || string(got) != "user modification\n" {
		t.Fatalf("backup of edited skill: %q (%v)", got, err)
	}
	if code, report, output := runSkills(t, "list", "--json"); code != 0 || len(report.Results) != 0 {
		t.Fatalf("list empty: %d %s", code, output)
	}
}

func TestAgentSkillsRejectsUnsupportedInputs(t *testing.T) {
	skillsCLIHome(t)
	t.Setenv("PATH", "")
	for _, args := range [][]string{
		nil, {"update"}, {"install"}, {"install", "one", "two"},
		{"install", "source", "--skill="}, {"install", "source", "--skill=../outside"},
		{"install", "source", "--force"}, {"install", "source", "--global"},
		{"install", "source", "--json", "--json"}, {"list", "extra"},
		{"list", "--scope=other"},
		{"list", "--skill=example"}, {"remove"}, {"remove", "../outside"},
		{"remove", "example", "--agent=codex"},
		{"link", "one", "two"}, {"link", "../outside"}, {"link", "--skill=example"},
	} {
		if code, _, output := runSkills(t, args...); code != 2 {
			t.Errorf("%q: %d %s", args, code, output)
		}
	}
}

func TestAgentSkillsDependencyFailureIsJSON(t *testing.T) {
	home := skillsCLIHome(t)
	t.Setenv("PATH", "")
	code, report, output := runSkills(t, "install", "example/repository", "--json")
	if code != 1 || report.Error == "" || len(report.Results) != 0 {
		t.Fatalf("missing dependency: %d %s", code, output)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("wrote skills despite dependency failure")
	}
}

func TestAgentSkillsInvalidAccountBlocksWholeOperation(t *testing.T) {
	home := skillsCLIHome(t)
	t.Setenv("PATH", "")
	instructionsWrite(t, filepath.Join(home, ".config", "quota", "config.json"), `{"claudeAccounts":[{"key":"invalid","configDir":"~/extra"}]}`)
	if code, report, output := runSkills(t, "list", "--json"); code != 1 || !strings.Contains(report.Error, "invalid") {
		t.Fatalf("invalid account ignored: %d %s", code, output)
	}
}

func TestAgentSkillsReportsUnreadableSkill(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user")
	}
	home := skillsCLIHome(t)
	path := filepath.Join(home, ".agents/skills", "example")
	blocked := filepath.Join(home, "unreadable")
	instructionsWrite(t, filepath.Join(blocked, "SKILL.md"), "Synthetic skill.\n")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(blocked, "SKILL.md"), filepath.Join(path, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o755) })
	for _, operation := range []string{"list", "link"} {
		code, report, output := runSkills(t, operation, "--json")
		if code != 1 || !strings.Contains(report.Error, "SKILL.md") {
			t.Errorf("%s hid skill read failure: %d %s", operation, code, output)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "example")); !os.IsNotExist(err) {
		t.Fatalf("unreadable skill was linked: %v", err)
	}
	code, report, output := runSkills(t, "remove", "example", "--json")
	if code != 0 || len(report.Results) != 3 || report.Results[0].Status != "backed-up" {
		t.Fatalf("unreadable directory was not backed up: %d %s", code, output)
	}
	backup := report.Results[0].BackupPath
	if err := os.Chmod(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(backup, "SKILL.md")); err != nil || string(got) != "Synthetic skill.\n" {
		t.Fatalf("backed-up content changed: %q %v", got, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("removed directory remains: %v", err)
	}
}

func TestAgentSkillsRepoRejectsGitOverrides(t *testing.T) {
	home := skillsCLIHome(t)
	current := skillsTestRepo(t, filepath.Join(home, "current"))
	other := skillsTestRepo(t, filepath.Join(home, "other"))
	t.Chdir(current)
	for _, repo := range []string{current, other} {
		instructionsWrite(t, filepath.Join(repo, ".agents/skills", "example", "SKILL.md"), "Synthetic skill.\n")
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	for _, args := range [][]string{
		{"list"}, {"link", "example"}, {"remove", "example"}, {"install", "./source"},
	} {
		code, report, output := runSkills(t, append(args, "--scope=repo", "--json")...)
		if code != 1 || !strings.Contains(report.Error, "GIT_DIR") || !strings.Contains(report.Error, "GIT_WORK_TREE") || len(report.Results) != 0 {
			t.Errorf("%v did not reject repository override: %d %s", args, code, output)
		}
	}
	for _, repo := range []string{current, other} {
		if content, err := os.ReadFile(filepath.Join(repo, ".agents/skills", "example", "SKILL.md")); err != nil || string(content) != "Synthetic skill.\n" {
			t.Errorf("skill changed in %q: %q %v", repo, content, err)
		}
		if _, err := os.Lstat(filepath.Join(repo, ".claude", "skills", "example")); !os.IsNotExist(err) {
			t.Errorf("link created in %q: %v", repo, err)
		}
	}
}

func TestAgentSkillsRepoGitConfigBoundary(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprintf("repository_override=%t", override), func(t *testing.T) {
			home := skillsCLIHome(t)
			current := skillsTestRepo(t, filepath.Join(home, "current"))
			other := skillsTestRepo(t, filepath.Join(home, "other"))
			t.Chdir(current)
			for _, repo := range []string{current, other} {
				instructionsWrite(t, filepath.Join(repo, ".agents/skills", "example", "SKILL.md"), "Synthetic skill.\n")
			}
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "credential.interactive")
			t.Setenv("GIT_CONFIG_VALUE_0", "false")
			if override {
				t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
				t.Setenv("GIT_CONFIG_VALUE_0", other)
			}
			code, report, output := runSkills(t, "link", "example", "--scope=repo", "--json")
			if override {
				if code != 1 || !strings.Contains(report.Error, "core.worktree") || len(report.Results) != 0 {
					t.Fatalf("repository override not rejected: %d %s", code, output)
				}
			} else if code != 0 {
				t.Fatalf("authentication setting prevented linking: %d %s", code, output)
			}
			for _, repo := range []string{current, other} {
				if body, err := os.ReadFile(filepath.Join(repo, ".agents/skills", "example", "SKILL.md")); err != nil || string(body) != "Synthetic skill.\n" {
					t.Fatalf("source changed in %s: %q %v", repo, body, err)
				}
				link := filepath.Join(repo, ".claude", "skills", "example")
				if !override && repo == current {
					got, err := filepath.EvalSymlinks(link)
					want, wantErr := filepath.EvalSymlinks(filepath.Join(current, ".agents/skills", "example"))
					if err != nil || wantErr != nil || got != want {
						t.Fatalf("wrong link: %q want=%q errors=%v %v", got, want, err, wantErr)
					}
				} else if _, err := os.Lstat(link); !os.IsNotExist(err) {
					t.Fatalf("unexpected link at %s: %v", link, err)
				}
			}
		})
	}
}

func TestAgentSkillsRepoPreservesPathWhitespace(t *testing.T) {
	for _, suffix := range []string{"", " ", "\t", "\n"} {
		t.Run(fmt.Sprintf("suffix_%q", suffix), func(t *testing.T) {
			home := skillsCLIHome(t)
			neighbor := filepath.Join(home, "repo")
			repo := skillsTestRepo(t, neighbor+suffix)
			t.Chdir(repo)
			path := filepath.Join(repo, ".agents/skills", "example", "SKILL.md")
			instructionsWrite(t, path, "Selected repository.\n")
			if suffix != "" {
				instructionsWrite(t, filepath.Join(neighbor, ".agents/skills", "example", "SKILL.md"), "Neighbor.\n")
			}
			for _, args := range [][]string{{"list"}, {"link", "example"}, {"remove", "example"}} {
				code, report, output := runSkills(t, append(args, "--scope=repo", "--json")...)
				if code != 0 || len(report.Results) == 0 || report.Results[0].Path != filepath.Dir(path) {
					t.Fatalf("%v selected the wrong repository: %d %s", args, code, output)
				}
			}
			if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("selected skill was not removed: %v", err)
			}
			if suffix != "" {
				if content, err := os.ReadFile(filepath.Join(neighbor, ".agents/skills", "example", "SKILL.md")); err != nil || string(content) != "Neighbor.\n" {
					t.Fatalf("neighbor changed: %q %v", content, err)
				}
			}
		})
	}
}

func skillsTestRepo(t *testing.T, path string) string {
	t.Helper()
	if output, err := exec.Command("git", "init", "-q", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
