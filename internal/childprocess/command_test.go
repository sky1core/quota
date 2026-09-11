package childprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandCleansUpDescendantPipes(t *testing.T) {
	for _, mode := range []string{"run", "output", "combined"} {
		for _, exitParent := range []bool{false, true} {
			name := mode + "/cancel"
			if exitParent {
				name = mode + "/parent-exit"
			}
			t.Run(name, func(t *testing.T) {
				ready := filepath.Join(t.TempDir(), "ready")
				script := `sleep 20 & printf '%s' "$!" > "$1"; wait`
				if exitParent {
					script = `sleep 20 & printf '%s' "$!" > "$1"; exit 0`
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := CommandContext(ctx, "/bin/sh", "-c", script, "child-test", ready)
				done := make(chan error, 1)
				go func() {
					var err error
					switch mode {
					case "run":
						cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
						err = Run(cmd)
					case "output":
						_, err = Output(cmd)
					case "combined":
						_, err = CombinedOutput(cmd)
					}
					done <- err
				}()
				var child int
				deadline := time.Now().Add(3 * time.Second)
				for child == 0 && time.Now().Before(deadline) {
					b, _ := os.ReadFile(ready)
					child, _ = strconv.Atoi(string(b))
					if child == 0 {
						time.Sleep(5 * time.Millisecond)
					}
				}
				if child <= 0 {
					cancel()
					<-done
					t.Fatal("child never became ready")
				}
				start := time.Now()
				if !exitParent {
					cancel()
				}
				err := <-done
				if err == nil {
					t.Error("expected cancellation or pipe timeout")
				}
				if exitParent && !errors.Is(err, exec.ErrWaitDelay) {
					t.Errorf("parent exit: %v", err)
				}
				if elapsed := time.Since(start); elapsed > time.Second {
					t.Errorf("return delayed: %s", elapsed)
				}
				for deadline = time.Now().Add(time.Second); ; {
					if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
						break
					}
					state, e := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(child)).Output()
					if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("child survived: pid=%d state=%s error=%v", child, state, e)
					}
					time.Sleep(10 * time.Millisecond)
				}
			})
		}
	}
}
