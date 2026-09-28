package agentinstructions

import (
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

	"github.com/sky1core/quota/internal/agenthooks"
)

func TestInspectNativeCodexHooksCleansDescendants(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantError string
	}{
		{"successful inspection", `{"id":2,"result":{"data":[{"cwd":%s,"hooks":[],"errors":[],"warnings":[]}]}}`, ""},
		{"early RPC error", `{"id":1,"error":{"code":-32601}}`, "initialize native hook inspection"},
		{"invalid hooks response", `{"id":2,"result":{}}`, "hooks/list did not return"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(root, "repo")
			home := filepath.Join(root, "home")
			for _, dir := range []string{repo, home} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			pidFile := filepath.Join(root, "child.pid")
			response := tc.response
			if tc.wantError == "" {
				cwdJSON, err := json.Marshal(repo)
				if err != nil {
					t.Fatal(err)
				}
				response = fmt.Sprintf(response, cwdJSON)
			}
			firstResponse := `{"id":1,"result":{}}`
			if tc.wantError == "initialize native hook inspection" {
				firstResponse = response
			}
			script := "#!/bin/sh\n" +
				"/bin/sleep 30 </dev/null >/dev/null 2>&1 &\n" +
				"printf '%s\\n' \"$!\" > " + agenthooks.ShellQuote([]string{pidFile}) + "\n" +
				"IFS= read -r request || exit 1\n" +
				"printf '%s\\n' " + agenthooks.ShellQuote([]string{firstResponse}) + "\n"
			if tc.wantError != "initialize native hook inspection" {
				script += "IFS= read -r request || exit 1\nIFS= read -r request || exit 1\n" +
					"printf '%s\\n' " + agenthooks.ShellQuote([]string{response}) + "\n"
			}
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("PATH", bin)
			report, inspectErr := InspectNativeCodexHooksForHome(context.Background(), repo, filepath.Join(home, ".codex"), NativeExpectations{})
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if exited, _ := nativeDescendantExited(pid); !exited {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if tc.wantError == "" {
				if inspectErr != nil || report.State != "configured" {
					t.Fatalf("inspection report = %+v, error = %v", report, inspectErr)
				}
			} else if inspectErr == nil || !strings.Contains(inspectErr.Error(), tc.wantError) {
				t.Fatalf("inspection error = %v, want %q", inspectErr, tc.wantError)
			}
			deadline := time.Now().Add(time.Second)
			for {
				exited, err := nativeDescendantExited(pid)
				if err != nil {
					t.Fatalf("check descendant %d: %v", pid, err)
				}
				if exited {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d survived inspection return", pid)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func nativeDescendantExited(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	state, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH), nil
	}
	return strings.HasPrefix(strings.TrimSpace(string(state)), "Z"), nil
}
