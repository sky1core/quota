package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/quotacache"
)

func TestSelectAgentUsesSelectedBinaryWithoutModelCatalog(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			binDir := filepath.Join(home, "bin")
			if err := os.Mkdir(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(binDir, provider)
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			dir := quotaTestAccountDir(t, filepath.Join(home, "."+provider))
			raw := autoPromptCodexQuota(80, 80)
			if provider == "claude" {
				raw = "Current session: 20% used\nCurrent week (all models): 20% used"
			}
			quotacache.Put(quotaTestCacheKey(t, provider, dir), raw, time.Now().Add(time.Hour))
			var stdout, stderr bytes.Buffer
			code := runSelectAgentWithIO(context.Background(), []string{"--agent=" + provider, "--json"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
			}
			var result selectAgentResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Selected == nil || len(result.Selected.Command) == 0 || result.Selected.Command[0] != bin {
				t.Fatalf("selected command=%v, want binary %q", result.Selected, bin)
			}
		})
	}
}

func TestExplicitExecPromptRunsWithoutModelCatalog(t *testing.T) {
	if os.Getenv("QUOTA_EXEC_NO_CATALOG_HELPER") == "1" {
		os.Exit(runExecPrompt(context.Background(), []string{"--agent=" + os.Getenv("QUOTA_TEST_PROVIDER"), "prompt"}))
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			binDir := filepath.Join(home, "bin")
			if err := os.Mkdir(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(binDir, provider)
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'launched:%s\\n' \"$1\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			dir := quotaTestAccountDir(t, filepath.Join(home, "."+provider))
			raw := autoPromptCodexQuota(80, 80)
			wantArg := "exec"
			if provider == "claude" {
				raw = "Current session: 20% used\nCurrent week (all models): 20% used"
				wantArg = "-p"
			}
			quotacache.Put(quotaTestCacheKey(t, provider, dir), raw, time.Now().Add(time.Hour))
			cmd := exec.Command(os.Args[0], "-test.run=^TestExplicitExecPromptRunsWithoutModelCatalog$")
			cmd.Env = append(os.Environ(), "QUOTA_EXEC_NO_CATALOG_HELPER=1", "QUOTA_TEST_PROVIDER="+provider)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "launched:"+wantArg {
				t.Fatalf("exec error=%v output=%q", err, out)
			}
		})
	}
}

func TestSignalStopsCLIProbeDescendants(t *testing.T) {
	if args := os.Getenv("QUOTA_SIGNAL_HELPER_ARGS"); args != "" {
		if err := json.Unmarshal([]byte(args), &os.Args); err != nil {
			os.Exit(90)
		}
		os.Exit(run())
	}
	tests := []struct {
		name     string
		provider string
		args     []string
		stage    string
		arg      string
	}{
		{"Claude query quota", "claude", []string{"quota-cli", "--json"}, "quota", "-p"},
		{"Claude select quota", "claude", []string{"quota-cli", "select-agent", "--agent=claude"}, "quota", "-p"},
		{"Claude explicit prompt quota", "claude", []string{"quota-cli", "exec-prompt", "--agent=claude", "prompt"}, "quota", "-p"},
		{"query quota", "codex", []string{"quota-cli", "--json"}, "quota", "app-server"},
		{"select quota", "codex", []string{"quota-cli", "select-agent", "--agent=codex"}, "quota", "app-server"},
		{"explicit prompt quota", "codex", []string{"quota-cli", "exec-prompt", "--agent=codex", "prompt"}, "quota", "app-server"},
		{"models version", "codex", []string{"quota-cli", "models", "--agent=codex"}, "version", "--version"},
		{"models discovery", "codex", []string{"quota-cli", "models", "--agent=codex"}, "discovery", "app-server"},
		{"automatic prompt version", "codex", []string{"quota-cli", "exec-prompt", "--model", "fable:high", "--model", "code-model:high", "--", "prompt"}, "version", "--version"},
		{"automatic prompt discovery", "codex", []string{"quota-cli", "exec-prompt", "--model", "fable:high", "--model", "code-model:high", "--", "prompt"}, "discovery", "app-server"},
	}
	for _, tc := range tests {
		for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, signal), func(t *testing.T) {
				testSignalStopsCLIProbeDescendant(t, tc.provider, tc.args, tc.stage, tc.arg, signal)
			})
		}
	}
}

func testSignalStopsCLIProbeDescendant(t *testing.T, provider string, args []string, stage, wantArg string, signal syscall.Signal) {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(home, "started")
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$QUOTA_BLOCK_STAGE" != "version" ]; then
  printf 'synthetic-v1\n'
  exit 0
fi
if [ "$1" != "--version" ] && [ "$1" != "app-server" ] && [ "$1" != "-p" ]; then
  exit 42
fi
/bin/sleep 30 &
child=$!
printf '%s:%s:%s:%s' "$QUOTA_BLOCK_STAGE" "$1" "$$" "$child" > "$QUOTA_DISCOVERY_STARTED"
wait "$child"
`
	if err := os.WriteFile(filepath.Join(binDir, provider), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	otherProvider := "claude"
	if provider == "claude" {
		otherProvider = "codex"
	}
	if err := os.WriteFile(filepath.Join(binDir, otherProvider), []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalStopsCLIProbeDescendants$")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "PATH="+binDir, "QUOTA_SIGNAL_HELPER_ARGS="+string(encodedArgs), "QUOTA_BLOCK_STAGE="+stage, "QUOTA_DISCOVERY_STARTED="+started)
	logFile, err := os.Create(filepath.Join(home, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	probePIDs := waitForSignalProbe(t, cmd, started, stage, wantArg)
	defer func() {
		for _, pid := range probePIDs {
			if signalProbeRunning(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
	if err := cmd.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted command exited successfully")
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("interrupted command did not stop")
	}
	deadline := time.Now().Add(time.Second)
	for _, pid := range probePIDs {
		for signalProbeRunning(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("probe process survived %s after CLI exit: pid=%d stage=%s", signal, pid, stage)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func waitForSignalProbe(t *testing.T, cmd *exec.Cmd, started, stage, wantArg string) [2]int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, err := os.ReadFile(started)
		if err == nil {
			parts := strings.Split(string(body), ":")
			if len(parts) == 4 {
				if parts[0] != stage || parts[1] != wantArg {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("probe stage=%q arg=%q, want stage=%q arg=%q", parts[0], parts[1], stage, wantArg)
				}
				processPID, processErr := strconv.Atoi(parts[2])
				descendantPID, descendantErr := strconv.Atoi(parts[3])
				if processErr == nil && descendantErr == nil && processPID > 0 && descendantPID > 0 {
					return [2]int{processPID, descendantPID}
				}
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("probe stage %s did not start: %v", stage, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func signalProbeRunning(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false
	}
	state, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err != nil || !strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
}

func TestCancelledExecPromptDoesNotLaunchProvider(t *testing.T) {
	if os.Getenv("QUOTA_EXEC_CANCEL_HELPER") == "1" {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		os.Exit(runExecPrompt(ctx, []string{"--agent=codex", "prompt"}))
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	launched := filepath.Join(home, "launched")
	bin := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf 'launched' > \"$QUOTA_EXEC_LAUNCHED\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCancelledExecPromptDoesNotLaunchProvider$")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "PATH="+binDir, "QUOTA_EXEC_CANCEL_HELPER=1", "QUOTA_EXEC_LAUNCHED="+launched)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("cancelled command exited successfully: %q", out)
	}
	if _, statErr := os.Stat(launched); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled command launched provider: stat=%v output=%q", statErr, out)
	}
}
