package update

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sky1core/quota/internal/childprocess"
)

type Target struct {
	Name            string
	Path            string
	PreviousVersion string
	Updated         bool
}

type Result struct {
	Version string
	Targets []Target
}

func (r Result) RestartRequired(name, runningVersion string) bool {
	for _, target := range r.Targets {
		if target.Name == name {
			return runningVersion != r.Version
		}
	}
	return false
}

func Coordinated(ctx context.Context, caller string) (Result, error) {
	if err := supportedCaller(caller, runtime.GOOS); err != nil {
		return Result{}, err
	}
	path, err := BinPath(ctx, caller)
	if err != nil {
		return Result{}, err
	}
	if !filepath.IsAbs(path) {
		return Result{}, fmt.Errorf("installation path must be absolute: %s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return Result{}, err
	}
	lock, err := lockUpdates(ctx, dir)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	latest, err := Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	result, needsInstall, err := planUpdate(dir, caller, runtime.GOOS, latest)
	if err != nil {
		return Result{}, err
	}
	if !needsInstall {
		return result, nil
	}
	stage, err := os.MkdirTemp("", "quota-update-build-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	if err := stageRelease(ctx, stage, result.Targets, latest); err != nil {
		return Result{}, err
	}
	var replacements []replacement
	for _, target := range result.Targets {
		if target.Updated {
			replacements = append(replacements, replacement{source: filepath.Join(stage, target.Name), destination: target.Path})
		}
	}
	if err := replaceFiles(ctx, dir, replacements); err != nil {
		return Result{}, err
	}
	return result, nil
}

func planUpdate(dir, caller, goos, version string) (Result, bool, error) {
	targets, err := installedTargets(dir, caller, goos)
	if err != nil {
		return Result{}, false, err
	}
	changed := false
	for i := range targets {
		targets[i].Updated = targets[i].PreviousVersion != version
		changed = changed || targets[i].Updated
	}
	return Result{Version: version, Targets: targets}, changed, nil
}

func supportedCaller(caller, goos string) error {
	if (caller == "quota-cli" && (goos == "darwin" || goos == "linux")) || (caller == "quota-bar" && goos == "darwin") {
		return nil
	}
	return fmt.Errorf("unsupported update target %q on %s", caller, goos)
}

func installedTargets(dir, caller, goos string) ([]Target, error) {
	if err := supportedCaller(caller, goos); err != nil {
		return nil, err
	}
	var targets []Target
	for _, name := range []string{"quota-cli", "quota-bar"} {
		if name == "quota-bar" && goos != "darwin" {
			continue
		}
		target := Target{Name: name, Path: filepath.Join(dir, name)}
		_, err := os.Lstat(target.Path)
		if errors.Is(err, os.ErrNotExist) {
			if name == caller {
				targets = append(targets, target)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		target.PreviousVersion, err = diskVersion(target.Path, name)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func diskVersion(path, name string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not a regular executable (symlinks are not replaced)", path)
	}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading build metadata of %s: %w", path, err)
	}
	if bi.Main.Path != Module || bi.Path != Module+"/cmd/"+name || bi.Main.Replace != nil {
		return "", fmt.Errorf("%s does not contain an unmodified %s/cmd/%s build", path, Module, name)
	}
	var goos, goarch string
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "GOOS":
			goos = setting.Value
		case "GOARCH":
			goarch = setting.Value
		}
	}
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return "", fmt.Errorf("%s targets %s/%s, expected %s/%s; install a native binary before updating", path, goos, goarch, runtime.GOOS, runtime.GOARCH)
	}
	return ResolveVersion("", bi, true), nil
}

func stageRelease(ctx context.Context, dir string, targets []Target, version string) error {
	var changed []Target
	for _, target := range targets {
		if target.Updated {
			changed = append(changed, target)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	args := []string{"install"}
	for _, target := range changed {
		args = append(args, Module+"/cmd/"+target.Name+"@"+version)
	}
	cmd, err := goCmd(ctx, args...)
	if err != nil {
		return err
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GOBIN" && key != "GOOS" && key != "GOARCH" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GOBIN="+dir, "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if out, err := childprocess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("staging release %s: %w\n%s", version, err, strings.TrimSpace(string(out)))
	}
	for _, target := range changed {
		got, err := diskVersion(filepath.Join(dir, target.Name), target.Name)
		if err != nil {
			return err
		}
		if got != version {
			return fmt.Errorf("staged %s version %q, expected %q", target.Name, got, version)
		}
	}
	return nil
}

func lockUpdates(ctx context.Context, dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
