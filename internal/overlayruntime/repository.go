package overlayruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/childprocess"
)

const localInstructions = "AGENTS.local.md"
const maxInstructionBytes = 8 << 20
const cliName = "quota-cli agent instructions"
const noticePrefix = "[quota instructions] "

var ErrOutsideRepository = errors.New("not inside a Git worktree")

type repoContext struct {
	Start, Top, Root, Common string
	Context                  context.Context
}

func (r repoContext) localSource() string { return filepath.Join(r.Root, localInstructions) }

func gitOutput(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := childprocess.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := childprocess.Run(cmd)
	if err != nil {
		if strings.HasPrefix(stderr.String(), "fatal: not a git repository (or any") {
			return out.Bytes(), fmt.Errorf("%w: %s", ErrOutsideRepository, strings.TrimSpace(stderr.String()))
		}
		return out.Bytes(), fmt.Errorf("git %s failed: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return out.Bytes(), nil
}

func gitBoolean(e error) (bool, error) {
	if e == nil {
		return true, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(e, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, e
}

func (r repoContext) tracked(path string) (bool, error) {
	_, e := gitOutput(r.Context, filepath.Dir(path), "ls-files", "--error-unmatch", "--", path)
	return gitBoolean(e)
}

func resolvePath(p string) string {
	a, e := filepath.Abs(p)
	if e == nil {
		p = a
	}
	v, e := filepath.EvalSymlinks(p)
	if e == nil {
		return v
	}
	parent := filepath.Dir(p)
	if parent != p {
		return filepath.Join(resolvePath(parent), filepath.Base(p))
	}
	return p
}

func onePath(data []byte) (string, error) {
	if !bytes.HasSuffix(data, []byte("\n")) || !utf8.Valid(data) {
		return "", fmt.Errorf("Git did not return a newline-terminated UTF-8 path")
	}
	return resolvePath(string(data[:len(data)-1])), nil
}

func resolveContext(ctx context.Context, dir string) (repoContext, error) {
	r := repoContext{Context: ctx}
	p, e := filepath.EvalSymlinks(dir)
	if e != nil {
		return r, fmt.Errorf("cannot enter project directory: %s: %w", dir, e)
	}
	r.Start = resolvePath(p)
	info, e := os.Stat(r.Start)
	if e != nil || !info.IsDir() {
		return r, fmt.Errorf("project directory is not a directory: %s", dir)
	}
	b, e := gitOutput(ctx, r.Start, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if e != nil {
		return r, e
	}
	r.Top, e = onePath(b)
	if e != nil {
		return r, e
	}
	b, e = gitOutput(ctx, r.Top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return r, e
	}
	r.Common, e = onePath(b)
	if e != nil {
		return r, e
	}
	b, e = gitOutput(ctx, r.Top, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return r, e
	}
	for _, v := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(v, []byte("worktree ")) {
			p := string(v[9:])
			if !utf8.ValidString(p) || !filepath.IsAbs(p) {
				return r, fmt.Errorf("worktree path is not absolute UTF-8")
			}
			r.Root = resolvePath(p)
			break
		}
	}
	if r.Root == "" {
		return r, fmt.Errorf("could not resolve primary worktree")
	}
	info, e = os.Stat(r.Root)
	if e != nil || !info.IsDir() {
		return r, fmt.Errorf("primary worktree path is not a directory")
	}
	return r, nil
}

func exists(p string) bool { _, e := os.Lstat(p); return e == nil || !os.IsNotExist(e) }

func readLocalInstructions(path string) (body string, notice string, present bool, err error) {
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return "", "", false, nil
	}
	if e != nil {
		return "", "", true, e
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", path + " is a symlink; not a regular file", true, nil
	}
	if !info.Mode().IsRegular() {
		return "", path + " is not a regular file", true, nil
	}
	if info.Size() > maxInstructionBytes {
		return "", fmt.Sprintf("%s exceeds the %d-byte size limit", path, maxInstructionBytes), true, nil
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return "", "", true, e
	}
	if len(data) > maxInstructionBytes {
		return "", fmt.Sprintf("%s exceeds the %d-byte size limit", path, maxInstructionBytes), true, nil
	}
	if !utf8.Valid(data) {
		return "", path + " is not valid UTF-8", true, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", path + " contains a NUL byte", true, nil
	}
	return string(data), "", true, nil
}
