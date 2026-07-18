// Package update implements the manual self-update flow shared by quota-cli
// (the `update` subcommand) and quota-bar (the update menu item): resolve the
// latest release tag through the Go module proxy, install it with
// `go install`, and report where it landed. There is no automatic update —
// each binary only updates itself, and only when the user asks.
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Module is this project's Go module path; binaries install as Module/cmd/<name>.
const Module = "github.com/sky1core/quota"

// ResolveVersion picks a binary's version string from, in order: an explicit
// ldflags override; the module version of a `go install …@version` build; the
// short VCS revision of a local build; else "dev". Pure (takes build info as
// args) so it is testable.
func ResolveVersion(ldflag string, bi *debug.BuildInfo, ok bool) string {
	if ldflag != "" {
		return ldflag
	}
	if !ok || bi == nil {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		// A release install reports its clean tag ("v0.9.0"); a local build in
		// a tagged checkout can also land here as "v0.9.0+dirty" or a pseudo-
		// version — never equal to a release tag, which is what update wants.
		return v
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
			return rev // local build: short commit hash
		}
	}
	return "dev"
}

// CurrentVersion resolves the running binary's version from its embedded
// build info. A release install reports its clean tag ("v0.9.0"); a local
// build reports a form that never equals a release tag ("v0.9.0+dirty", a
// short commit hash, or "dev"), so `update` on a dev build always reinstalls
// the latest release.
func CurrentVersion() string {
	bi, ok := debug.ReadBuildInfo()
	return ResolveVersion("", bi, ok)
}

// goCmd builds an exec.Cmd for the go tool, rooted in the user's home
// directory so module resolution never picks up a development checkout's
// go.mod (`go install pkg@version` and `go list -m pkg@latest` are
// module-independent, but rooting them explicitly keeps that true regardless
// of the caller's CWD).
func goCmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("go toolchain not found in PATH (required for update): %w", err)
	}
	cmd := exec.CommandContext(ctx, goBin, args...)
	if home, herr := os.UserHomeDir(); herr == nil {
		cmd.Dir = home
	}
	return cmd, nil
}

// Latest returns the newest release tag of this module, exactly as the Go
// module proxy resolves "@latest". A tag pushed moments ago can lag behind by
// a few minutes of proxy cache.
func Latest(ctx context.Context) (string, error) {
	cmd, err := goCmd(ctx, "list", "-m", "-f", "{{.Version}}", Module+"@latest")
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving %s@latest: %w%s", Module, err, stderrOf(err))
	}
	v := strings.TrimSpace(string(out))
	if !strings.HasPrefix(v, "v") {
		return "", fmt.Errorf("unexpected version %q from go list", v)
	}
	return v, nil
}

// Install runs `go install Module/cmd/<name>@<version>` and returns the path
// the binary landed at. It installs exactly the requested version — deciding
// WHETHER to update (and what to do with the running process) is the caller's
// job.
func Install(ctx context.Context, name, version string) (string, error) {
	cmd, err := goCmd(ctx, "install", fmt.Sprintf("%s/cmd/%s@%s", Module, name, version))
	if err != nil {
		return "", err
	}
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return "", fmt.Errorf("go install %s@%s: %w\n%s", name, version, cerr, strings.TrimSpace(string(out)))
	}
	return BinPath(ctx, name)
}

// BinPath returns where `go install` puts a binary named name: GOBIN if set,
// else the first GOPATH element's bin directory.
func BinPath(ctx context.Context, name string) (string, error) {
	cmd, err := goCmd(ctx, "env", "GOBIN", "GOPATH")
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w%s", err, stderrOf(err))
	}
	// One value per line, in argument order. An unset GOBIN is an EMPTY first
	// line — trim per line, never the output as a whole (that would swallow
	// the empty line and misread GOPATH as GOBIN).
	lines := strings.Split(string(out), "\n")
	lineAt := func(i int) string {
		if i < len(lines) {
			return strings.TrimSpace(lines[i])
		}
		return ""
	}
	if gobin := lineAt(0); gobin != "" {
		return filepath.Join(gobin, name), nil
	}
	if gopath := lineAt(1); gopath != "" {
		// GOPATH can be a list; go install uses its first element.
		return filepath.Join(filepath.SplitList(gopath)[0], "bin", name), nil
	}
	return "", errors.New("go env reported neither GOBIN nor GOPATH")
}

// stderrOf extracts captured stderr from an exec .Output() error, prefixed
// with a newline, so command failures surface the go tool's actual complaint.
func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return "\n" + strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}
