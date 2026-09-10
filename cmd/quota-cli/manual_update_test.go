package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/update"
)

func TestRunUpdateSignals(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "quota-cli")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			lock, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "update")
			cmd.Env = append(os.Environ(), "GOBIN="+dir, "HOME="+t.TempDir(),
				"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off")
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				t.Fatalf("update exited while directory locked: %v\n%s", err, &out)
			case <-time.After(time.Second):
			}
			if err := cmd.Process.Signal(sig); err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			err = <-done
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("update did not return failure normally after %s: %v\n%s", sig, err, &out)
			}
			if got := out.String(); got != "업데이트 실패: context canceled\n" {
				t.Fatalf("update did not cancel its lock wait: %q", got)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cancelled update created files: %v, %v", entries, err)
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
