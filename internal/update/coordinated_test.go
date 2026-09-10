package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTargetSelectionWithoutCompanion(t *testing.T) {
	for _, tc := range []struct{ caller, goos string }{
		{"quota-cli", "darwin"}, {"quota-bar", "darwin"}, {"quota-cli", "linux"},
	} {
		t.Run(tc.caller+"/"+tc.goos, func(t *testing.T) {
			dir := t.TempDir()
			if tc.goos == "linux" {
				writeExecutable(t, filepath.Join(dir, "quota-bar"), "unsupported platform binary")
			}
			result, changed, err := planUpdate(dir, tc.caller, tc.goos, "v1.2.3")
			if err != nil || !changed || len(result.Targets) != 1 || result.Targets[0].Name != tc.caller || !result.Targets[0].Updated {
				t.Fatalf("selection=%+v, changed=%v, err=%v", result, changed, err)
			}
		})
	}
	for _, tc := range []struct{ caller, goos string }{
		{"quota-bar", "linux"}, {"quota-cli", "windows"}, {"other", "darwin"},
	} {
		if _, err := installedTargets(t.TempDir(), tc.caller, tc.goos); err == nil {
			t.Fatalf("accepted unsupported caller %v", tc)
		}
	}
}

func TestPlanUpdateReadsEveryInstalledBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds production executables with the real Go tool")
	}
	dir := t.TempDir()
	t.Setenv("GOBIN", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	install := func(vcs string, names ...string) {
		t.Helper()
		args := []string{"install", "-buildvcs=" + vcs}
		for _, name := range names {
			args = append(args, Module+"/cmd/"+name)
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real go install: %v\n%s", err, out)
		}
	}
	names := []string{"quota-cli"}
	if runtime.GOOS == "darwin" {
		names = append(names, "quota-bar")
	}
	install("false", names...)
	cli := filepath.Join(dir, "quota-cli")
	localVersion, err := diskVersion(cli, "quota-cli")
	if err != nil {
		t.Fatal(err)
	}
	result, changed, err := planUpdate(dir, "quota-cli", runtime.GOOS, localVersion)
	if err != nil || changed || len(result.Targets) != len(names) {
		t.Fatalf("current disk builds: %+v, %v, %v", result, changed, err)
	}
	if result.RestartRequired("quota-cli", localVersion) || !result.RestartRequired("quota-cli", "older-running-version") {
		t.Fatal("restart decision did not compare running version independently of disk")
	}
	if _, err := diskVersion(cli, "quota-bar"); err == nil {
		t.Fatal("accepted wrong executable identity")
	}
	if runtime.GOOS != "darwin" {
		return
	}
	install("true", "quota-cli")
	callerVersion, err := diskVersion(cli, "quota-cli")
	if err != nil {
		t.Fatal(err)
	}
	if callerVersion == localVersion {
		t.Skip("checkout has no distinct VCS build metadata")
	}
	for _, caller := range []string{"quota-cli", "quota-bar"} {
		result, changed, err = planUpdate(dir, caller, "darwin", callerVersion)
		if err != nil || !changed || len(result.Targets) != 2 || result.Targets[0].Updated || !result.Targets[1].Updated {
			t.Fatalf("current CLI did not repair stale bar: %+v, %v, %v", result, changed, err)
		}
		result, changed, err = planUpdate(dir, caller, "darwin", localVersion)
		if err != nil || !changed || len(result.Targets) != 2 || !result.Targets[0].Updated || result.Targets[1].Updated {
			t.Fatalf("current bar did not repair stale CLI: %+v, %v, %v", result, changed, err)
		}
		if result.RestartRequired("quota-bar", localVersion) {
			t.Fatal("companion-only change requires unnecessary bar restart")
		}
	}
	bar := filepath.Join(dir, "quota-bar")
	if err := os.Rename(bar, bar+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bar+".original", bar); err != nil {
		t.Fatal(err)
	}
	if _, _, err := planUpdate(dir, "quota-cli", "darwin", callerVersion); err == nil {
		t.Fatal("up-to-date caller ignored invalid companion")
	}
}

func TestRestartRequired(t *testing.T) {
	for _, tc := range []struct {
		name, running string
		barUpdated    bool
		want          bool
	}{
		{"old running bar with updated disk", "v1.0.0", false, true},
		{"bar installed by this update", "v1.0.0", true, true},
		{"only companion changed", "v2.0.0", false, false},
		{"disk repaired to running version", "v2.0.0", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Result{Version: "v2.0.0", Targets: []Target{
				{Name: "quota-cli", Updated: true}, {Name: "quota-bar", Updated: tc.barUpdated},
			}}
			if got := result.RestartRequired("quota-bar", tc.running); got != tc.want {
				t.Fatalf("restart=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateLockProcess(t *testing.T) {
	if dir := os.Getenv("QUOTA_TEST_UPDATE_LOCK_DIR"); dir != "" {
		mode := os.Getenv("QUOTA_TEST_UPDATE_LOCK_MODE")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var wantErr error
		switch mode {
		case "timeout":
			ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
			defer cancel()
			wantErr = context.DeadlineExceeded
		case "cancel":
			timer := time.AfterFunc(150*time.Millisecond, cancel)
			defer timer.Stop()
			wantErr = context.Canceled
		}
		f, err := lockUpdates(ctx, dir)
		if !errors.Is(err, wantErr) {
			if f != nil {
				f.Close()
			}
			t.Fatalf("lock result: %v, want %v", err, wantErr)
		}
		if f != nil {
			runtime.KeepAlive(f)
		}
		return
	}
	dir := t.TempDir()
	f, err := lockUpdates(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("lock descriptor is not a directory: %v, %v", info, err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func(mode string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUpdateLockProcess$")
		cmd.Env = append(os.Environ(), "QUOTA_TEST_UPDATE_LOCK_DIR="+dir, "QUOTA_TEST_UPDATE_LOCK_MODE="+mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("lock child: %v\n%s", err, out)
		}
	}
	child("timeout")
	child("cancel")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	child("acquire")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, err = lockUpdates(ctx, dir)
	if err != nil {
		t.Fatalf("exiting process left stale lock: %v", err)
	}
	f.Close()
	cancel()
	if _, err := lockUpdates(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request acquired lock: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("locking created files: %v, %v", entries, err)
	}
}

func TestUpdateDirectoryLockIgnoresLegacyLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".quota-update.lock")
	legacy, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.WriteString("legacy lock"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Chmod(0); err != nil {
		t.Fatal(err)
	}
	original, err := legacy.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(legacy.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := lockUpdates(ctx, dir)
	if err != nil {
		t.Fatalf("inaccessible, held legacy lock blocked directory lock: %v", err)
	}
	defer lock.Close()
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) || current.Mode() != original.Mode() || !current.ModTime().Equal(original.ModTime()) {
		t.Fatalf("legacy lock changed: %v, %v", current, err)
	}
	contents := make([]byte, 32)
	n, err := legacy.ReadAt(contents, 0)
	if n != len("legacy lock") || string(contents[:n]) != "legacy lock" {
		t.Fatalf("legacy lock contents changed: %q, %v", contents[:n], err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".quota-update.lock" {
		t.Fatalf("unexpected directory contents: %v, %v", entries, err)
	}
}

func TestStageReleaseRealGo(t *testing.T) {
	version := os.Getenv("QUOTA_UPDATE_TEST_RELEASE")
	if version == "" {
		t.Skip("set QUOTA_UPDATE_TEST_RELEASE for a real release install into a temporary GOBIN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	protectedBin := t.TempDir()
	t.Setenv("GOBIN", protectedBin)
	targets := []Target{{Name: "quota-cli"}}
	if runtime.GOOS == "darwin" {
		targets = append(targets, Target{Name: "quota-bar"})
	}
	for _, target := range targets {
		writeExecutable(t, filepath.Join(protectedBin, target.Name), "original "+target.Name)
	}
	if err := stageRelease(ctx, dir, targets, version); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		got, err := diskVersion(filepath.Join(dir, target.Name), target.Name)
		if err != nil || got != version {
			t.Fatalf("release metadata: %s, %v", got, err)
		}
		assertContents(t, filepath.Join(protectedBin, target.Name), "original "+target.Name)
	}
	badTargets := append(append([]Target(nil), targets...), Target{Name: "nonexistent-command"})
	if err := stageRelease(ctx, t.TempDir(), badTargets, version); err == nil || !strings.Contains(err.Error(), "staging release") {
		t.Fatalf("real Go failure not propagated: %v", err)
	}
	for _, target := range targets {
		assertContents(t, filepath.Join(protectedBin, target.Name), "original "+target.Name)
	}
}

func TestCoordinatedRejectsRelativeGOBINBeforeCreatingFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GOBIN", "relative-bin")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Coordinated(ctx, "quota-cli"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative GOBIN accepted: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("created files before validation: %v err=%v", entries, err)
	}
}

func TestDiskVersionRejectsForeignPlatform(t *testing.T) {
	if testing.Short() {
		t.Skip("builds foreign production CLI")
	}
	goos := "linux"
	if runtime.GOOS == "linux" {
		goos = "darwin"
	}
	for _, tc := range []struct{ name, goos, goarch string }{
		{"foreign OS", goos, runtime.GOARCH},
		{"foreign architecture", runtime.GOOS, map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.goarch == "" {
				t.Skip("no foreign architecture selected")
			}
			path := filepath.Join(t.TempDir(), "quota-cli")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/quota-cli")
			cmd.Dir = "../.."
			cmd.Env = append(os.Environ(), "GOOS="+tc.goos, "GOARCH="+tc.goarch, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cross build: %v\n%s", err, out)
			}
			if _, err := diskVersion(path, "quota-cli"); err == nil || !strings.Contains(err.Error(), "targets "+tc.goos+"/"+tc.goarch) {
				t.Fatalf("foreign binary accepted: %v", err)
			}
		})
	}
}
