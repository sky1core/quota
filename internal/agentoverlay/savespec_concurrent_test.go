package agentoverlay

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

func TestSaveSpecConcurrentProcesses(t *testing.T) {
	if mode := os.Getenv("QUOTA_SAVESPEC_MODE"); mode != "" {
		path := os.Getenv("QUOTA_SAVESPEC_PATH")
		_, err := SaveSpec(path, InitTemplate(), mode == "force")
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
		path := filepath.Join(dir, "agent-overlay.json")
		success, exists := runSaveSpecChildren(t, "noclobber", path, 8)
		if success != 1 || exists != 7 {
			t.Fatalf("success=%d exists=%d, want 1 and 7", success, exists)
		}
		if _, err := LoadSpec(path); err != nil {
			t.Fatalf("final spec invalid: %v", err)
		}
		assertMode600(t, path)
		assertNoTempFiles(t, dir)
	})

	t.Run("force replaces without corruption", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "agent-overlay.json")
		if _, err := SaveSpec(path, InitTemplate(), false); err != nil {
			t.Fatal(err)
		}
		success, exists := runSaveSpecChildren(t, "force", path, 8)
		if success != 8 || exists != 0 {
			t.Fatalf("success=%d exists=%d, want 8 and 0", success, exists)
		}
		if _, err := LoadSpec(path); err != nil {
			t.Fatalf("final spec invalid after concurrent force: %v", err)
		}
		assertMode600(t, path)
		assertNoTempFiles(t, dir)
	})
}

func runSaveSpecChildren(t *testing.T, mode, path string, n int) (success, exists int) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, n)
	outs := make([]*bytes.Buffer, n)
	for i := range cmds {
		cmd := exec.Command(binary, "-test.run=^TestSaveSpecConcurrentProcesses$")
		cmd.Env = append(os.Environ(),
			"QUOTA_SAVESPEC_MODE="+mode,
			"QUOTA_SAVESPEC_PATH="+path,
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

func assertMode600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestSaveSpecSimultaneousNoClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay.json")
	const writers = 32
	start := make(chan struct{})
	results := make(chan error, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	for i := range writers {
		go func(i int) {
			ready.Done()
			<-start
			spec := InitTemplate()
			spec.Claude.Hooks["SessionStart"][0].Command = fmt.Sprintf("entry-%d-%s", i, strings.Repeat("x", 65536))
			_, err := SaveSpec(path, spec, false)
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
	if _, err := LoadSpec(path); err != nil {
		t.Fatal(err)
	}
}

func TestSaveSpecLongFilename(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("s", 246)+".json")
	for _, force := range []bool{false, true} {
		if _, err := SaveSpec(path, InitTemplate(), force); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadSpec(path); err != nil {
		t.Fatal(err)
	}
}
