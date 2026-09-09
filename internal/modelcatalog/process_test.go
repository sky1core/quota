package modelcatalog

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationStopsLauncherChild(t *testing.T) {
	ps, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := command(ctx, Target{Binary: "/bin/sh", Env: os.Environ()}, "-c", "sleep 30 & child=$!; printf '%s\\n' \"$child\"; wait")
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		cancel()
		cmd.Wait()
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 0 {
		cancel()
		cmd.Wait()
		t.Fatalf("child PID: %q, %v", line, err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("launcher survived cancellation")
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		state, err := exec.Command(ps, "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatalf("cannot inspect child process: %v", err)
		}
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child still running after cancellation: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
