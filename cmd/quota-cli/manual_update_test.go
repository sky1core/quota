package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/update"
)

func TestRunUpdateSignals(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	goBinary, err = filepath.EvalSymlinks(goBinary)
	if err != nil {
		t.Fatal(err)
	}
	goPath := filepath.Dir(goBinary) + string(os.PathListSeparator) + "/usr/bin:/bin:/usr/sbin:/sbin"
	binary := filepath.Join(t.TempDir(), "quota-cli")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, goBinary, "build", "-buildvcs=false", "-o", binary, ".")
	build.Env = append(os.Environ(), "PATH="+goPath, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			home := t.TempDir()
			binDir := filepath.Join(home, "bin")
			if err := os.Mkdir(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(home, "go-env-ready")
			script := `#!/bin/sh
if [ "$1" != "env" ] || [ "$2" != "GOBIN" ]; then
  exit 42
fi
/bin/sleep 30 &
child=$!
printf '%s %s\n' "$$" "$child" > "$QUOTA_GO_ENV_READY"
wait "$child"
`
			if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "update")
			cmd.Env = append(os.Environ(), "HOME="+home, "GOPATH="+filepath.Join(home, "go"),
				"PATH="+binDir+string(os.PathListSeparator)+goPath, "QUOTA_GO_ENV_READY="+ready,
				"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off")
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var probePIDs [2]int
			readyDeadline := time.NewTimer(5 * time.Second)
			defer readyDeadline.Stop()
			readyPoll := time.NewTicker(20 * time.Millisecond)
			defer readyPoll.Stop()
		waitReady:
			for {
				body, err := os.ReadFile(ready)
				if err == nil {
					parts := strings.Fields(string(body))
					if len(parts) == 2 {
						parentPID, parentErr := strconv.Atoi(parts[0])
						childPID, childErr := strconv.Atoi(parts[1])
						if parentErr == nil && childErr == nil && parentPID > 0 && childPID > 0 {
							probePIDs = [2]int{parentPID, childPID}
							break waitReady
						}
					}
				}
				select {
				case err := <-done:
					t.Fatalf("update exited before go env was ready: %v\n%s", err, &out)
				case <-readyDeadline.C:
					cancel()
					<-done
					t.Fatalf("go env did not become ready: %v\n%s", err, &out)
				case <-readyPoll.C:
				}
			}
			defer func() {
				for _, pid := range probePIDs {
					if signalProbeRunning(pid) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}()
			if err := cmd.Process.Signal(sig); err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			err := <-done
			if ctx.Err() != nil {
				t.Fatalf("update exceeded its test deadline after %s: %v\n%s", sig, ctx.Err(), &out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("update did not return failure normally after %s: %v\n%s", sig, err, &out)
			}
			if got := out.String(); !strings.HasPrefix(got, "업데이트 실패: ") || !strings.HasSuffix(got, "\n") {
				t.Fatalf("update did not report its failure: %q", got)
			}
			for _, pid := range probePIDs {
				if signalProbeRunning(pid) {
					t.Fatalf("go env process survived %s: pid=%d", sig, pid)
				}
			}
		})
	}
}

func TestPrintCoordinatedUpdateResult(t *testing.T) {
	for _, barInstalled := range []bool{false, true} {
		result := update.Result{Version: "v1.2.3", Targets: []update.Target{
			{Name: "quota-cli", Path: "/example/bin/quota-cli", PreviousVersion: "v1.2.3"},
		}}
		if barInstalled {
			result.Targets = append(result.Targets, update.Target{Name: "quota-bar", Path: "/example/bin/quota-bar", PreviousVersion: "v1.2.2", Updated: true})
		}
		var out bytes.Buffer
		printUpdateResult(&out, result)
		text := out.String()
		if !strings.Contains(text, "이미 최신 버전입니다: /example/bin/quota-cli (v1.2.3)") {
			t.Fatalf("missing current CLI status: %s", text)
		}
		if barInstalled {
			if !strings.Contains(text, "설치 완료: /example/bin/quota-bar (v1.2.2 → v1.2.3)") || !strings.Contains(text, "quota-bar를 재시작하세요") {
				t.Fatalf("missing companion update or restart requirement: %s", text)
			}
		} else if strings.Contains(text, "quota-bar") {
			t.Fatalf("CLI-only update reports bar: %s", text)
		}
	}
}

func TestPrintNewCallerInstall(t *testing.T) {
	var out bytes.Buffer
	printUpdateResult(&out, update.Result{Version: "v1.2.3", Targets: []update.Target{
		{Name: "quota-cli", Path: "/example/bin/quota-cli", Updated: true},
	}})
	if got := out.String(); got != "설치 완료: /example/bin/quota-cli (v1.2.3)\n" {
		t.Fatalf("new caller status: %q", got)
	}
}
