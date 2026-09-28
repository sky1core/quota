package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegatedCodexTargetPreservesOptionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		want     string
		worktree bool
	}{
		{nil, "/start", false},
		{[]string{"-C", "../target "}, "/target ", false},
		{[]string{"--cd=/target"}, "/target", false},
		{[]string{"-C/target"}, "/target", false},
		{[]string{"-C=/target"}, "/target", false},
		{[]string{"-c", "-Cnot-a-directory", "--cd", "/target"}, "/target", false},
		{[]string{"--model", "--cd=not-a-directory", "--", "-C/prompt"}, "/start", false},
		{[]string{"resume", "session", "--cd", "/target", "prompt"}, "/target", false},
		{[]string{"--worktree", "prompt"}, "/start", true},
		{[]string{"-C", "/target", "--worktree"}, "/target", true},
		{[]string{"--model", "--worktree", "prompt"}, "/start", false},
		{[]string{"--", "--worktree"}, "/start", false},
	} {
		got, worktree, err := delegatedCodexTarget("/start", tc.args)
		if err != nil || got != tc.want || worktree != tc.worktree {
			t.Fatalf("args=%q directory=%q worktree=%t error=%v want=%q %t", tc.args, got, worktree, err, tc.want, tc.worktree)
		}
	}
	if _, _, err := delegatedCodexTarget("/start", []string{"-C"}); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestClaudeCreatesWorktreeDetection(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"prompt"}, false},
		{[]string{"--worktree", "prompt"}, true},
		{[]string{"--worktree=feature", "prompt"}, true},
		{[]string{"-w", "prompt"}, true},
		{[]string{"-wfeature", "prompt"}, true},
		{[]string{"-pw", "prompt"}, true},
		{[]string{"--model", "-w", "prompt"}, false},
		{[]string{"-m", "--worktree", "prompt"}, false},
		{[]string{"--append-system-prompt", "--worktree", "prompt"}, false},
		{[]string{"--append-system-prompt=x", "-w"}, true},
		{[]string{"--", "-w"}, false},
		{[]string{"use -w carefully"}, false},
	} {
		if got := claudeCreatesWorktree(tc.args); got != tc.want {
			t.Fatalf("args=%q worktree=%t want=%t", tc.args, got, tc.want)
		}
	}
}

func TestExecPreparedDelegatedRejectsInvalidSourceBeforeExec(t *testing.T) {
	instructionsHome(t)
	repo := instructionsRepo(t)
	t.Chdir(repo)
	path := filepath.Join(repo, "AGENTS.md")
	instructionsWrite(t, path, "invalid\x00instructions")
	err := execPreparedDelegated(context.Background(), "codex", "/unused/agent", []string{"exec"}, nil, os.Environ())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("invalid source reached exec: %v", err)
	}
}

func TestExecPromptChecksCodexTargetAndPreservesArguments(t *testing.T) {
	home := instructionsHome(t)
	source := instructionsRepo(t)
	target := instructionsRepo(t)
	instructionsWrite(t, filepath.Join(source, "AGENTS.local.md"), "invalid caller instructions\x00")
	instructionsWrite(t, filepath.Join(target, "AGENTS.md"), "target instructions\n")
	bin := filepath.Join(home, "bin", "codex")
	instructionsWrite(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 23\n")
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(90, 90))
	for _, directoryArgs := range [][]string{{"-C", target}, {"--cd=" + target}, {"-C" + target}} {
		args := append([]string{"--agent=codex"}, directoryArgs...)
		args = append(args, "--", "prompt with -C/not-a-directory")
		encoded, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^TestExecPromptCacheHelper$")
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "QUOTA_CACHE_HELPER_ARGS="+string(encoded))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		want := strings.Join(append([]string{"exec"}, args[1:]...), "\n") + "\n"
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 || stdout.String() != want || stderr.Len() != 0 {
			t.Fatalf("target rejected or process contract changed: exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
}

type execPromptFixture struct {
	home, repo, linked, marker string
}

func newExecPromptFixture(t *testing.T) execPromptFixture {
	t.Helper()
	home := instructionsHome(t)
	repo := instructionsRepo(t)
	linked := filepath.Join(filepath.Dir(repo), "linked")
	cmd := exec.Command("git", "worktree", "add", "--detach", linked)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree: %v: %s", err, out)
	}
	marker := filepath.Join(home, "launched")
	t.Setenv("QUOTA_INSTRUCTIONS_TEST_LAUNCHED", marker)
	for _, agent := range []string{"claude", "codex"} {
		bin := filepath.Join(home, "bin", agent)
		instructionsWrite(t, bin, "#!/bin/sh\nprintf 'launched' > \"$QUOTA_INSTRUCTIONS_TEST_LAUNCHED\"\n")
		if err := os.Chmod(bin, 0700); err != nil {
			t.Fatal(err)
		}
	}
	putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current session: 10% used\nCurrent week (all models): 10% used")
	putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(90, 90))
	return execPromptFixture{home: home, repo: repo, linked: linked, marker: marker}
}

func (f execPromptFixture) run(t *testing.T, cwd string, args []string) (error, string, string) {
	t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecPromptCacheHelper$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "QUOTA_CACHE_HELPER_ARGS="+string(encoded))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return err, stdout.String(), stderr.String()
}

func (f execPromptFixture) launched(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(f.marker)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func execPromptArgs(provider, prompt string, extra ...string) []string {
	return append(append([]string{"--agent=" + provider}, extra...), prompt)
}

func TestAutoPromptArgumentsKeepDelegationTarget(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, readOnly := range []bool{false, true} {
			account := autoPromptAccount{provider: provider, model: promptModel{model: "model", effort: "high"}}
			args := autoPromptArgs(account, autoPromptOptions{prompt: "use --worktree -w -C /elsewhere", readOnly: readOnly})
			if provider == "claude" {
				if claudeCreatesWorktree(args) {
					t.Fatalf("auto Claude arguments detected as worktree creation: %q", args)
				}
				continue
			}
			dir, worktree, err := delegatedCodexTarget("/start", args)
			if err != nil || dir != "/start" || worktree {
				t.Fatalf("auto Codex arguments changed the target: %q %t %v", dir, worktree, err)
			}
		}
	}
}

func TestExecPromptPreparesWorktreeInstructionsBeforeLaunching(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, state := range []string{"missing root", "missing nested", "stale root"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				f := newExecPromptFixture(t)
				source := filepath.Join(f.repo, "AGENTS.md")
				target := filepath.Join(f.linked, "AGENTS.md")
				start := f.linked
				instructionsWrite(t, source, "shared instructions\n")
				switch state {
				case "missing nested":
					source = filepath.Join(f.repo, "sub", "AGENTS.md")
					instructionsWrite(t, source, "nested instructions\n")
					start = filepath.Join(f.linked, "sub")
					if err := os.Mkdir(start, 0700); err != nil {
						t.Fatal(err)
					}
					target = filepath.Join(start, "AGENTS.md")
				case "stale root":
					instructionsWrite(t, target, "stale instructions\n")
				}
				cwd := start
				var extra []string
				if provider == "codex" {
					extra = []string{"-C", start}
					cwd = f.repo
				}
				err, stdout, stderr := f.run(t, cwd, execPromptArgs(provider, "prompt", extra...))
				if err != nil || stdout != "" || stderr != "" {
					t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout, stderr)
				}
				if !f.launched(t) {
					t.Fatal("agent was not launched after preparation")
				}
				want, _ := os.ReadFile(source)
				got, err := os.ReadFile(target)
				if err != nil || string(got) != string(want) {
					t.Fatalf("worktree instructions = %q %v; want %q", got, err, want)
				}
				if state == "missing nested" {
					if got, err := os.ReadFile(filepath.Join(f.linked, "AGENTS.md")); err != nil || string(got) != "shared instructions\n" {
						t.Fatalf("root instructions not prepared with nested start: %q %v", got, err)
					}
				}
			})
		}
	}
}

func TestExecPromptRejectsUnavailableInstructionsBeforeLaunching(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, invalid := range []string{"invalid local", "directory target", "worktree flag"} {
			t.Run(provider+"/"+invalid, func(t *testing.T) {
				f := newExecPromptFixture(t)
				source := filepath.Join(f.repo, "AGENTS.md")
				instructionsWrite(t, source, "shared instructions\n")
				path := source
				want := "--worktree"
				start := f.linked
				var extra []string
				switch invalid {
				case "invalid local":
					path = filepath.Join(f.repo, "AGENTS.local.md")
					instructionsWrite(t, path, "invalid\x00instructions")
					want = "NUL"
				case "directory target":
					if err := os.Mkdir(filepath.Join(f.linked, "AGENTS.md"), 0700); err != nil {
						t.Fatal(err)
					}
					want = "regular file"
				case "worktree flag":
					start = f.repo
					extra = []string{"--worktree"}
				}
				cwd := start
				if provider == "codex" {
					extra = append([]string{"-C", start}, extra...)
					cwd = f.repo
				}
				err, stdout, stderr := f.run(t, cwd, execPromptArgs(provider, "prompt", extra...))
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout, stderr)
				}
				if stdout != "" || !strings.Contains(stderr, path) || !strings.Contains(stderr, want) {
					t.Fatalf("missing diagnosis: stdout=%q stderr=%q", stdout, stderr)
				}
				if invalid == "directory target" && !strings.Contains(stderr, filepath.Join(f.linked, "AGENTS.md")) {
					t.Fatalf("missing worktree path in diagnosis: %q", stderr)
				}
				if f.launched(t) {
					t.Fatal("agent was launched despite unavailable instructions")
				}
			})
		}
	}
}

func TestExecPromptAllowsWorktreeFlagWhenInstructionsAreTracked(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newExecPromptFixture(t)
			instructionsWrite(t, filepath.Join(f.repo, "AGENTS.md"), "tracked instructions\n")
			for _, args := range [][]string{{"add", "AGENTS.md"}, {"commit", "-q", "-m", "track"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = f.repo
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			err, stdout, stderr := f.run(t, f.repo, execPromptArgs(provider, "prompt", "--worktree"))
			if err != nil || stdout != "" || stderr != "" || !f.launched(t) {
				t.Fatalf("tracked instructions blocked --worktree: exit=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
		})
	}
}
