package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEnsureAppBundleWritesAndRepointsWrapper(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	first := filepath.Join(t.TempDir(), "quota-bar-1")
	second := filepath.Join(t.TempDir(), "quota-bar-2")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureAppBundle(first); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(appBundlePath(), "Contents", "Info.plist"))
	if err != nil || !bytes.Equal(plist, bundleInfoPlist) {
		t.Fatalf("Info.plist not written from embedded copy: %v", err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != first {
		t.Fatalf("symlink = %q, want %q", got, first)
	}
	if err := ensureAppBundle(second); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != second {
		t.Fatalf("symlink after repoint = %q, want %q", got, second)
	}
	entries, err := os.ReadDir(filepath.Dir(appBundleExecutable()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "quota-bar" {
		t.Fatalf("temporary symlink left behind: %v", entries)
	}
}

func TestAppStartupProcess(t *testing.T) {
	if os.Getenv("QUOTA_TEST_BAR_STARTUP") != "1" {
		return
	}
	if _, inherited := os.LookupEnv("QUOTA_BAR_LOCK_FD"); inherited {
		fd, err := syscall.Open(pidLockPath(), syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		syscall.Close(fd)
		if err != syscall.EWOULDBLOCK {
			t.Fatalf("singleton lock was released during process transition: %v", err)
		}
	}
	prepareAppProcess()
	if !runningFromAppBundle() || lockFD < 0 {
		t.Fatal("startup did not retain bundle identity and pid lock")
	}
	if _, ok := os.LookupEnv("QUOTA_BAR_LOCK_FD"); ok {
		t.Fatal("lock inheritance marker leaked past startup")
	}
	flags, err := unix.FcntlInt(uintptr(lockFD), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("pid lock could leak across later exec: flags=%d, err=%v", flags, err)
	}
	if data, err := os.ReadFile(pidLockPath()); err != nil || string(data) != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Fatalf("pid file does not identify daemon: %q %v", data, err)
	}
	conn, err := net.DialTimeout("tcp", os.Getenv("QUOTA_TEST_BAR_READY"), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintln(conn, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Close(lockFD); err != nil {
		t.Fatal(err)
	}
	lockFD = -1
}

func TestAppStartupRejectsDuplicateBeforeBundleMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("QUOTA_BAR_BUNDLED", "")
	t.Setenv("QUOTA_TEST_BAR_STARTUP", "1")
	first, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "quota-bar")
	source, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	copy, err := os.OpenFile(second, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(copy, source); err != nil {
		t.Fatal(err)
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	fd, contended := acquireLock()
	if fd < 0 {
		t.Fatalf("first lock failed: contended=%v", contended)
	}
	defer syscall.Close(fd)
	if err := ensureAppBundle(first); err != nil {
		t.Fatal(err)
	}
	for _, daemon := range []string{"0", "1"} {
		t.Setenv("QUOTA_BAR_DAEMON", daemon)
		for _, executable := range []string{first, second, appBundleExecutable()} {
			if out, err := exec.Command(executable, "-test.run=^TestAppStartupProcess$").CombinedOutput(); err != nil {
				t.Fatalf("duplicate (daemon=%s, executable=%s): %v\n%s", daemon, executable, err, out)
			}
			if got, err := os.Readlink(appBundleExecutable()); err != nil || got != first {
				t.Fatalf("duplicate replaced running bundle target: %q, want %q (%v)", got, first, err)
			}
		}
	}
}

func TestAppStartupRetainsLockAcrossProcessTransitions(t *testing.T) {
	for _, mode := range []string{"spawn", "exec", "bundle"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("QUOTA_BAR_DAEMON", "1")
			t.Setenv("QUOTA_BAR_BUNDLED", "")
			t.Setenv("QUOTA_TEST_BAR_STARTUP", "1")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "spawn" {
				t.Setenv("QUOTA_BAR_DAEMON", "0")
			} else if mode == "bundle" {
				if err := ensureAppBundle(executable); err != nil {
					t.Fatal(err)
				}
				executable = appBundleExecutable()
			}
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			listener.SetDeadline(time.Now().Add(30 * time.Second))
			t.Setenv("QUOTA_TEST_BAR_READY", listener.Addr().String())
			cmd := exec.Command(executable, "-test.run=^TestAppStartupProcess$")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := cmd.Wait(); err != nil {
					t.Errorf("startup child: %v", err)
				}
				fd, contended := acquireLock()
				if fd < 0 {
					t.Errorf("exited child retained lock: contended=%v", contended)
				} else {
					syscall.Close(fd)
				}
			}()
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(30 * time.Second))
			defer func() {
				conn.Write([]byte{1})
				if _, err := io.Copy(io.Discard, conn); err != nil {
					t.Errorf("startup child shutdown: %v", err)
				}
			}()
			var pid int
			if _, err := fmt.Fscanln(conn, &pid); err != nil {
				t.Fatal(err)
			}
			if (pid == cmd.Process.Pid) != (mode != "spawn") {
				t.Fatalf("unexpected process identity: launcher=%d daemon=%d mode=%s", cmd.Process.Pid, pid, mode)
			}
			if fd, contended := acquireLock(); fd >= 0 || !contended {
				if fd >= 0 {
					syscall.Close(fd)
				}
				t.Fatal("startup child did not keep the singleton lock")
			}
		})
	}
}

func TestAppStartupBundleFailureReleasesLock(t *testing.T) {
	for _, daemon := range []string{"0", "1"} {
		t.Run("daemon="+daemon, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("QUOTA_BAR_DAEMON", daemon)
			t.Setenv("QUOTA_TEST_BAR_STARTUP", "1")
			if err := os.MkdirAll(filepath.Dir(appBundlePath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(appBundlePath(), []byte("blocked"), 0o644); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestAppStartupProcess$")
			if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 1 {
				t.Fatalf("bundle failure did not exit 1: %v\n%s", err, out)
			}
			fd, contended := acquireLock()
			if fd < 0 {
				t.Fatalf("failed startup retained lock: contended=%v", contended)
			}
			syscall.Close(fd)
		})
	}
}

func TestLoginRegistrationRunsThroughAppBundle(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	content, err := newLoginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<string>"+appBundleExecutable()+"</string>") {
		t.Fatalf("plist does not launch the app bundle executable:\n%s", content)
	}
	real, err := realExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != real {
		t.Fatalf("bundle symlink = %q, want running binary %q", got, real)
	}
}
