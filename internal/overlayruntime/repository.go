package overlayruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/childprocess"
	"golang.org/x/sys/unix"
)

const sharedRule = "AGENTS.md"
const localRule = "AGENTS.local.md"
const localBridge = "CLAUDE.local.md"
const codexRule = "AGENTS.override.md"
const localBridgeBody = "@" + localRule + "\n"
const maxRuleBytes = 8 << 20
const cliName = "quota-cli agent instructions"

var ErrOutsideRepository = errors.New("not inside a Git worktree")

type repoContext struct {
	Start, Top, Root, Common string
	Worktrees                []string
	Context                  context.Context
}

func (r repoContext) Bare() bool          { return r.Root == r.Common }
func (r repoContext) localSource() string { return filepath.Join(r.Root, localRule) }

func gitOutput(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := childprocess.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := childprocess.Run(cmd)
	if err != nil {
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
func (r repoContext) tracked(w, rel string) (bool, error) {
	_, e := gitOutput(r.Context, w, "ls-files", "--error-unmatch", "--", ":(icase,literal)"+rel)
	return gitBoolean(e)
}
func (r repoContext) ignored(w, rel string) (bool, error) {
	_, e := gitOutput(r.Context, w, "check-ignore", "-q", "--", rel)
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
		return r, ErrOutsideRepository
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
			r.Worktrees = append(r.Worktrees, resolvePath(p))
		}
	}
	if len(r.Worktrees) == 0 {
		return r, fmt.Errorf("could not resolve primary worktree")
	}
	r.Root = r.Worktrees[0]
	info, e = os.Stat(r.Root)
	if e != nil || !info.IsDir() {
		return r, fmt.Errorf("primary worktree path is not a directory")
	}
	return r, nil
}
func (r repoContext) topologyProblems() []string {
	var out []string
	for i, w := range r.Worktrees {
		info, e := os.Stat(w)
		if e != nil || !info.IsDir() {
			out = append(out, fmt.Sprintf("%s is registered as a worktree but does not exist; run git worktree prune or repair", w))
		}
		for _, other := range r.Worktrees[i+1:] {
			if w != other && (within(w, other) || within(other, w)) {
				out = append(out, fmt.Sprintf("worktrees must not nest: %s and %s", w, other))
			}
		}
	}
	if !contains(r.Worktrees, r.Top) {
		out = append(out, r.Top+" is not in git worktree list; run git worktree repair")
	}
	return out
}
func exists(p string) bool { _, e := os.Lstat(p); return e == nil || !os.IsNotExist(e) }
func within(p, root string) bool {
	rel, e := filepath.Rel(resolvePath(root), resolvePath(p))
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func uniqueStrings(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func readRegular(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; not a regular file", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	var before unix.Stat_t
	if e = unix.Lstat(path, &before); e != nil {
		return nil, e
	}
	if before.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("%s is not owned by current user", path)
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, fmt.Errorf("could not read %s: %w", path, e)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	opened, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed during inspection", path)
	}
	if opened.Size() > maxRuleBytes {
		return nil, fmt.Errorf("%s exceeds %d-byte file size limit", path, maxRuleBytes)
	}
	data, e := io.ReadAll(io.LimitReader(f, maxRuleBytes+1))
	if e != nil {
		return nil, e
	}
	if len(data) > maxRuleBytes {
		return nil, fmt.Errorf("%s exceeds file size limit", path)
	}
	var afterStat, openedAfter unix.Stat_t
	if e = unix.Lstat(path, &afterStat); e != nil {
		return nil, e
	}
	if e = unix.Fstat(fd, &openedAfter); e != nil {
		return nil, e
	}
	if !sameFileSnapshot(before, afterStat) || !sameFileSnapshot(before, openedAfter) {
		return nil, fmt.Errorf("%s changed during inspection", path)
	}
	return data, nil
}
func sameFileSnapshot(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func decodeRule(data []byte, path string) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("rule file is not UTF-8: %s", path)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("rule file contains NUL: %s", path)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// atomicWriteFile replaces path with data. expected is the content observed at
// planning time (nil when the file did not exist); any difference aborts the write.
func atomicWriteFile(path string, data []byte, private, ownerExecutable bool, expected []byte) error {
	mode := os.FileMode(0o644)
	before, e := os.Lstat(path)
	if e == nil {
		if !before.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		var ownership unix.Stat_t
		if e = unix.Lstat(path, &ownership); e != nil {
			return e
		}
		if ownership.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("%s is not owned by current user", path)
		}
		mode = before.Mode().Perm()
		if expected == nil {
			return fmt.Errorf("%s appeared before write", path)
		}
		current, e := readRegular(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(current, expected) {
			return fmt.Errorf("%s changed before write", path)
		}
	} else if !os.IsNotExist(e) {
		return e
	} else if expected != nil {
		return fmt.Errorf("%s changed before write", path)
	}
	if private {
		allowed := os.FileMode(0o600)
		if ownerExecutable {
			allowed |= 0o100
			mode |= 0o100
		}
		mode &= allowed
	}
	f, e := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if before == nil {
		probe, e := os.OpenFile(tmp+".mode", os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		pi, e := probe.Stat()
		probe.Close()
		os.Remove(tmp + ".mode")
		if e != nil {
			return e
		}
		mode = pi.Mode().Perm()
	}
	if e = os.Chmod(tmp, mode); e != nil {
		return e
	}
	after, e := os.Lstat(path)
	if before == nil {
		if !os.IsNotExist(e) {
			return fmt.Errorf("%s changed during write", path)
		}
	} else if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("%s changed during write", path)
	}
	return os.Rename(tmp, path)
}
func safeDirectory(p string) error {
	if exists(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a regular directory", p)
		}
		return nil
	}
	return os.MkdirAll(p, 0o700)
}
func claudeNativeRefusal() string {
	if os.Getenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS") == "1" {
		return "Rules not delivered: CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 disables the native CLAUDE.md channel"
	}
	return ""
}
