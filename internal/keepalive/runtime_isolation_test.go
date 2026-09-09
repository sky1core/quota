//go:build darwin || linux

package keepalive

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestScanIsolationProcesses(t *testing.T) {
	if os.Getenv("QUOTA_SCAN_CHILD") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if os.Getenv("QUOTA_SCAN_OS_TEST") != "1" {
		t.Skip("requires a terminal and real process inspection; set QUOTA_SCAN_OS_TEST=1")
	}
	actualLsof, err := exec.LookPath("lsof")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := readRuntimeProcess(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if process.TTY == "?" || process.TTY == "??" {
		t.Fatal("run test in a terminal")
	}
	root, err := os.MkdirTemp("/tmp", "quota-scan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "session.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	now := time.Now()
	makeAccount := func(key string, pids []int) Account {
		home := filepath.Join(root, key)
		if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, "projects", "example"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, pid := range pids {
			p, err := readRuntimeProcess(context.Background(), pid)
			if err != nil {
				t.Fatal(err)
			}
			r := syntheticRegistry()
			r.PID = pid
			r.ProcStart = p.Start
			r.MessagingSocketPath = listener.Addr().String()
			if len(pids) > 1 {
				r.SessionID = "11111111-1111-4111-8111-" + strings.Repeat("0", 12-len(strconv.Itoa(pid))) + strconv.Itoa(pid)
			}
			if err := os.WriteFile(filepath.Join(home, "sessions", strconv.Itoa(pid)+".json"), runtimeJSON(t, r), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(home, "projects", "example", runtimeTestSession+".jsonl"), runtimeTurn(t, runtimeTestUser, runtimeTestReply, now.Add(-time.Minute)), 0600); err != nil {
			t.Fatal(err)
		}
		return Account{Provider: "claude", Key: key, Home: home}
	}
	var children []int
	for i := 0; i < 12; i++ {
		child := exec.Command(executable, "-test.run=^TestScanIsolationProcesses$")
		child.Env = append(os.Environ(), "QUOTA_SCAN_CHILD=1")
		input, err := child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			input.Close()
			if err := child.Wait(); err != nil {
				t.Error(err)
			}
		})
		children = append(children, child.Process.Pid)
	}
	first := makeAccount("first", []int{os.Getpid()})
	slow := makeAccount("slow", children)
	last := makeAccount("last", []int{os.Getpid()})
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "delayed")
	var cases []string
	for _, pid := range children {
		cases = append(cases, strconv.Itoa(pid))
	}
	script := "#!/bin/sh\nprevious=\nfor arg do\n if [ \"$previous\" = -p ]; then\n  case \"$arg\" in " + strings.Join(cases, "|") + ") : > '" + marker + "'; sleep 4;; esac\n fi\n previous=$arg\ndone\nexec '" + actualLsof + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "lsof"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := Runtime{Accounts: []Account{first, slow, last}}
	started := time.Now()
	found, err := r.Scan(context.Background(), now, time.Hour, nil)
	if !errors.Is(err, context.DeadlineExceeded) || len(found) != 2 || found[0].Account != "last" || found[1].Account != "first" {
		t.Fatalf("deadline lost candidates: %v %v", found, err)
	}
	t.Logf("account timeout preserved first and subsequent candidates in %s", time.Since(started))
	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	bad := Account{Provider: "claude", Key: "broken", Home: loop}
	for _, accounts := range [][]Account{{bad, first}, {first, bad}} {
		found, err = (&Runtime{Accounts: accounts}).Scan(context.Background(), now, time.Hour, nil)
		if err == nil || len(found) != 1 || found[0].Account != "first" {
			t.Fatalf("path error lost candidate: %v %v", found, err)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	found, err = r.Scan(ctx, now, time.Hour, nil)
	cancel()
	<-done
	if !errors.Is(err, context.Canceled) || len(found) != 0 {
		t.Fatalf("parent cancellation retained candidates: %v %v", found, err)
	}
	t.Log("parent cancellation after first candidate discarded all candidates")
}
