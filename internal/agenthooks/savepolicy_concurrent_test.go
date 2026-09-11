package agenthooks

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSavePolicyConcurrentProcesses(t *testing.T) {
	if mode := os.Getenv("QUOTA_SAVEPOLICY_MODE"); mode != "" {
		dir := os.Getenv("QUOTA_SAVEPOLICY_DIR")
		policy, err := Preset(PresetGitHubHistoryGuard)
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
		_, err = SavePolicy(dir, policy, mode == "force")
		switch {
		case err == nil:
			os.Exit(0)
		case strings.Contains(err.Error(), "already exists"):
			os.Exit(3)
		default:
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(4)
		}
	}

	t.Run("no-clobber picks one winner", func(t *testing.T) {
		dir := t.TempDir()
		success, exists := runSavePolicyChildren(t, "noclobber", dir, 8)
		if success != 1 || exists != 7 {
			t.Fatalf("success=%d exists=%d, want 1 and 7", success, exists)
		}
		assertPolicyValid(t, dir)
		assertNoPolicyTempFiles(t, dir)
	})

	t.Run("force replaces without corruption", func(t *testing.T) {
		dir := t.TempDir()
		policy, err := Preset(PresetGitHubHistoryGuard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := SavePolicy(dir, policy, false); err != nil {
			t.Fatal(err)
		}
		success, exists := runSavePolicyChildren(t, "force", dir, 8)
		if success != 8 || exists != 0 {
			t.Fatalf("success=%d exists=%d, want 8 and 0", success, exists)
		}
		assertPolicyValid(t, dir)
		assertNoPolicyTempFiles(t, dir)
	})
}

func runSavePolicyChildren(t *testing.T, mode, dir string, n int) (success, exists int) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, n)
	outs := make([]*bytes.Buffer, n)
	for i := range cmds {
		cmd := exec.Command(binary, "-test.run=^TestSavePolicyConcurrentProcesses$")
		cmd.Env = append(os.Environ(),
			"QUOTA_SAVEPOLICY_MODE="+mode,
			"QUOTA_SAVEPOLICY_DIR="+dir,
		)
		out := new(bytes.Buffer)
		cmd.Stdout = out
		cmd.Stderr = out
		cmds[i] = cmd
		outs[i] = out
	}
	for i, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
	}
	for i, cmd := range cmds {
		err := cmd.Wait()
		if err == nil {
			success++
			continue
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("child %d: %v\n%s", i, err, outs[i])
		}
		if ee.ExitCode() == 3 {
			exists++
			continue
		}
		t.Fatalf("child %d exit=%d\n%s", i, ee.ExitCode(), outs[i])
	}
	return success, exists
}

func assertPolicyValid(t *testing.T, dir string) {
	t.Helper()
	res := LoadPolicies(dir)
	if len(res.Errors) != 0 {
		t.Fatalf("load errors: %v", res.Errors)
	}
	if len(res.Policies) != 1 || res.Policies[0].ID != PresetGitHubHistoryGuard {
		t.Fatalf("policies = %+v", res.Policies)
	}
	path := filepath.Join(dir, PresetGitHubHistoryGuard+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}
}

func assertNoPolicyTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestSavePolicySimultaneousNoClobber(t *testing.T) {
	dir := t.TempDir()

	const writers = 32
	start := make(chan struct{})
	results := make(chan error, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	for i := range writers {
		go func(i int) {
			ready.Done()
			<-start
			policy, err := Preset(PresetGitHubHistoryGuard)
			if err == nil {
				policy.Description = fmt.Sprintf("entry-%d-%s", i, strings.Repeat("x", 65536))
				_, err = SavePolicy(dir, policy, false)
			}
			results <- err
		}(i)
	}
	ready.Wait()
	close(start)
	successes := 0
	for range writers {
		err := <-results
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Error(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creators = %d, want 1", successes)
	}
	assertPolicyValid(t, dir)
}

func TestSavePolicyLongFilename(t *testing.T) {
	dir := t.TempDir()
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policy.ID = strings.Repeat("p", 246)
	for _, force := range []bool{false, true} {
		if _, err := SavePolicy(dir, policy, force); err != nil {
			t.Fatal(err)
		}
	}
	if loaded := LoadPolicies(dir); len(loaded.Errors) != 0 || len(loaded.Policies) != 1 {
		t.Fatal(loaded)
	}
}
