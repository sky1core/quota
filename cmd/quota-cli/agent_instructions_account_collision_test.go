package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
)

type instructionCollisionFile struct {
	Body     string
	Mode     fs.FileMode
	Modified time.Time
}

func instructionCollisionSnapshot(t *testing.T, root string, runtimeHomes ...string) map[string]instructionCollisionFile {
	t.Helper()
	files := map[string]instructionCollisionFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, home := range runtimeHomes {
			if filepath.Dir(path) == home && entry.Name() != "config.toml" && entry.Name() != "hooks.json" && !strings.Contains(entry.Name(), ".bak.") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var body []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			body = []byte(target)
		} else {
			body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = instructionCollisionFile{string(body), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInstructionsSetupAccountCollision(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	bin := t.TempDir()
	binary := filepath.Join(bin, "quota-cli")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for name, target := range map[string]string{"git": git, "sh": "/bin/sh"} {
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	command := agenthooks.ShellQuote([]string{binary, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"})
	jsonHooks, err := json.Marshal(map[string]any{"keep": true, "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tomlHooks := fmt.Sprintf("keep = true\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = %q\n", command)
	for _, tc := range []struct {
		name, agent, rel, target, alias, body string
		fresh, allow, accountParent           bool
	}{
		{name: "claude-copy", agent: "claude", rel: "account/settings.json", target: "linked", body: "{}"},
		{name: "claude-new-copy", agent: "claude", rel: "account/settings.json", target: "linked", body: "{}", fresh: true},
		{name: "claude-source", agent: "claude", rel: "account/settings.json", target: "repo", body: "{}"},
		{name: "claude-copy-file-alias", agent: "claude", rel: "account/settings.json", target: "linked", alias: "file", body: "{}"},
		{name: "claude-copy-parent-alias", agent: "claude", rel: "account/settings.json", target: "linked", alias: "directory", body: "{}"},
		{name: "shared-source", agent: "claude", rel: "AGENTS.md", target: "repo", alias: "file", body: "{}"},
		{name: "checkout-shared-source", agent: "claude", rel: "AGENTS.md", target: "linked", alias: "file", body: "{}"},
		{name: "private-source", agent: "claude", rel: "AGENTS.local.md", target: "repo", alias: "file", body: "{}"},
		{name: "private-copy", agent: "claude", rel: "AGENTS.local.md", target: "linked", alias: "file", body: "{}"},
		{name: "codex-generated-other-agent", agent: "claude", rel: "AGENTS.override.md", target: "linked", alias: "file", body: "{}"},
		{name: "codex-hooks-copy", agent: "codex", rel: "account/hooks.json", target: "linked", body: string(jsonHooks)},
		{name: "codex-hooks-source", agent: "codex", rel: "account/hooks.json", target: "repo", alias: "file", body: string(jsonHooks)},
		{name: "codex-hooks-unchanged", agent: "codex", rel: "account/hooks.json", target: "linked", body: "{}"},
		{name: "codex-hooks-planned-parent", agent: "codex", rel: "account/hooks.json/extra.json", target: "linked", body: "{}", fresh: true, accountParent: true},
		{name: "codex-config-copy", agent: "codex", rel: "account/config.toml", target: "linked", body: tomlHooks},
		{name: "codex-config-source", agent: "codex", rel: "account/config.toml", target: "repo", alias: "file", body: tomlHooks},
		{name: "codex-config-parent-alias", agent: "codex", rel: "account/config.toml", target: "linked", alias: "directory", body: tomlHooks},
		{name: "all-before-any-account-write", agent: "all", rel: "account/hooks.json", target: "linked", body: string(jsonHooks)},
		{name: "unselected-account", agent: "codex", rel: "account/settings.json", target: "linked", body: "{}", allow: true},
		{name: "unregistered-account-inside-repo", agent: "claude", rel: "unregistered/settings.json", target: "repo", body: "{}", allow: true},
		{name: "codex-project-hooks-account", agent: "codex", rel: ".codex/hooks.json", target: "repo", body: string(jsonHooks), fresh: true, allow: true},
		{name: "codex-project-config-account", agent: "codex", rel: ".codex/config.toml", target: "repo", body: tomlHooks, fresh: true, allow: true},
		{name: "codex-project-account-alias", agent: "codex", rel: ".codex/hooks.json", target: "repo", alias: "directory", body: string(jsonHooks), fresh: true, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.agent != "claude" || !tc.fresh {
				if _, err := exec.LookPath("codex"); err != nil {
					t.Skip("real Codex CLI is required for this case or its seed setup")
				}
			}
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo, linked, home := filepath.Join(root, "repo"), filepath.Join(root, "linked"), filepath.Join(root, "home")
			for _, path := range []string{repo, home, filepath.Join(home, ".codex")} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"HOME=" + home, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "CODEX_HOME=" + filepath.Join(home, ".codex")}
			runGit := func(args ...string) {
				t.Helper()
				cmd := exec.CommandContext(ctx, git, args...)
				cmd.Dir, cmd.Env = repo, env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			call := func(args ...string) (int, instructionsReport) {
				t.Helper()
				cmd := exec.CommandContext(ctx, binary, append([]string{"agent", "instructions"}, args...)...)
				cmd.Dir, cmd.Env = repo, env
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if err != nil {
					if _, ok := err.(*exec.ExitError); !ok {
						t.Fatal(err)
					}
				}
				var report instructionsReport
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatalf("decode: %v, stdout=%s stderr=%s", err, &stdout, &stderr)
				}
				return cmd.ProcessState.ExitCode(), report
			}
			runGit("init", "-q")
			runGit("config", "user.name", "Instructions Test")
			runGit("config", "user.email", "instructions@example.invalid")
			write(filepath.Join(repo, "AGENTS.md"), "")
			write(filepath.Join(repo, "AGENTS.local.md"), "{}")
			runGit("add", "AGENTS.md")
			runGit("commit", "-q", "-m", "seed")
			runGit("worktree", "add", "-q", "--detach", linked, "HEAD")
			registered := strings.HasPrefix(tc.rel, "account/")
			target := filepath.Join(root, tc.target, tc.rel)
			if tc.rel != "AGENTS.override.md" {
				source := target
				if registered || tc.rel == "AGENTS.local.md" {
					source = filepath.Join(repo, tc.rel)
				}
				write(source, tc.body)
			}
			if strings.HasPrefix(tc.rel, ".codex/") {
				body := ""
				if tc.rel == ".codex/config.toml" {
					body = tc.body
				}
				write(filepath.Join(repo, ".codex", "config.toml"), body+fmt.Sprintf("\n[projects.%q]\ntrust_level = 'trusted'\n", repo))
			}
			write(filepath.Join(repo, ".git", "info", "exclude"), "/AGENTS.local.md\n/"+tc.rel+"\n")
			setup := []string{"setup", repo, "--agent=codex", "--json"}
			if registered {
				setup = append(setup, "--local-file="+tc.rel)
			}
			if !tc.fresh {
				if code, report := call(setup...); code > 1 || report.Error != "" || report.Applied == nil {
					t.Fatalf("seed setup: exit=%d error=%s", code, report.Error)
				}
				if registered {
					write(filepath.Join(repo, tc.rel), tc.body+"\n")
				}
			}
			accountName, envIndex, envName := "settings.json", 2, "CLAUDE_CONFIG_DIR"
			if tc.accountParent {
				target = filepath.Dir(target)
			}
			if filepath.Base(target) == "hooks.json" || filepath.Base(target) == "config.toml" {
				accountName, envIndex, envName = filepath.Base(target), 3, "CODEX_HOME"
			}
			accountDir := filepath.Dir(target)
			if tc.alias != "" {
				accountDir = filepath.Join(home, "alias")
				if tc.alias == "directory" {
					if err := os.Symlink(filepath.Dir(target), accountDir); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(accountDir, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, filepath.Join(accountDir, accountName)); err != nil {
						t.Fatal(err)
					}
				}
			}
			env[envIndex] = envName + "=" + accountDir
			runtimeHomes := []string{filepath.Join(home, ".codex")}
			if envName == "CODEX_HOME" {
				if resolved, err := filepath.EvalSymlinks(accountDir); err == nil {
					runtimeHomes = append(runtimeHomes, resolved)
				}
			}
			before := instructionCollisionSnapshot(t, root, runtimeHomes...)
			for _, dryRun := range []bool{true, false} {
				args := []string{"setup", repo, "--agent=" + tc.agent, "--json"}
				if registered {
					args = append(args, "--local-file="+tc.rel)
				}
				if dryRun {
					args = append(args, "--dry-run")
				}
				code, report := call(args...)
				if tc.allow {
					if report.Error != "" || (dryRun && code != 0) || (!dryRun && report.Applied == nil) {
						t.Fatalf("dry-run=%t: nonoverlapping account rejected: exit=%d error=%q", dryRun, code, report.Error)
					}
					if !dryRun {
						if strings.HasPrefix(tc.rel, ".codex/") {
							body, err := os.ReadFile(target)
							if err != nil || strings.Contains(string(body), command) || !strings.Contains(string(body), "keep") {
								t.Fatalf("account migration did not remove the owned hook and preserve unrelated settings: %v", err)
							}
						}
						continue
					}
				} else if code != 1 || !strings.Contains(report.Error, "overlaps repository") || report.Applied != nil {
					t.Errorf("dry-run=%t: collision not rejected in preflight: exit=%d error=%q", dryRun, code, report.Error)
				}
				after := instructionCollisionSnapshot(t, root, runtimeHomes...)
				for path, original := range before {
					if !reflect.DeepEqual(original, after[path]) {
						t.Errorf("dry-run=%t changed %s", dryRun, path)
					}
				}
				for path := range after {
					if _, existed := before[path]; !existed {
						t.Errorf("dry-run=%t created %s", dryRun, path)
					}
				}
			}
		})
	}
}
