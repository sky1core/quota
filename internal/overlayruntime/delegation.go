package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sky1core/quota/internal/atomicfile"
)

func PrepareDelegationInstructions(ctx context.Context, dir, agent string, createsWorktree bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inside, err := hasGitAncestor(dir)
	if err != nil || !inside {
		return err
	}
	if err := ValidateGitEnvironment(ctx, dir); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	sources, err := r.untrackedInstructionSources()
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	for _, rel := range sources {
		if err := r.prepareSharedInstructions(rel, agent, createsWorktree); err != nil {
			return err
		}
	}
	body, _, err := checkInstructionSource(r.localSource(), false)
	if err != nil {
		return err
	}
	if agent == "claude" && exceedsClaudeHookLimit(sessionStartContext(body, nil)) {
		return fmt.Errorf("%s and its header exceed Claude's %d UTF-16-unit hook limit", r.localSource(), claudeHookContextLimit)
	}
	return nil
}

func (r repoContext) untrackedInstructionSources() ([]string, error) {
	if r.Root == r.Common {
		return r.instructionFilesBelow(gitlinkSet{})
	}
	gitlinks, err := r.gitlinks()
	if err != nil {
		return nil, err
	}
	rels, err := r.instructionFilesBelow(gitlinks)
	if err != nil {
		return nil, err
	}
	var untracked []string
	for _, rel := range rels {
		other, err := r.untracked(rel)
		if err != nil {
			return nil, err
		}
		if other {
			untracked = append(untracked, rel)
		}
	}
	return untracked, nil
}

type gitlinkSet struct {
	paths      map[string]bool
	ignoreCase bool
}

func (g gitlinkSet) contains(rel string) bool {
	if g.ignoreCase {
		rel = asciiLower(rel)
	}
	return g.paths[rel]
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func (r repoContext) gitlinks() (gitlinkSet, error) {
	out, err := gitOutput(r.Context, r.Root, "config", "--type=bool", "--default=false", "core.ignorecase")
	if err != nil {
		return gitlinkSet{}, err
	}
	links := gitlinkSet{paths: map[string]bool{}, ignoreCase: string(out) == "true\n"}
	out, err = gitOutput(r.Context, r.Root, "ls-files", "-z", "--stage")
	if err != nil {
		return gitlinkSet{}, err
	}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if !bytes.HasPrefix(entry, []byte("160000 ")) {
			continue
		}
		tab := bytes.IndexByte(entry, '\t')
		if tab < 0 {
			return gitlinkSet{}, fmt.Errorf("git ls-files --stage under %s returned an entry without a path", r.Root)
		}
		rel := filepath.FromSlash(string(entry[tab+1:]))
		if links.ignoreCase {
			rel = asciiLower(rel)
		}
		links.paths[rel] = true
	}
	return links, nil
}

func (r repoContext) untracked(rel string) (bool, error) {
	out, err := gitOutput(r.Context, r.Root, "ls-files", "--others", "-z", "--", ":(literal)"+filepath.ToSlash(rel))
	if err != nil {
		return false, err
	}
	switch entries := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}); {
	case len(out) == 0:
		return false, nil
	case len(entries) == 1:
		return true, nil
	}
	return false, fmt.Errorf("git ls-files --others returned more than one entry for %s", filepath.Join(r.Root, rel))
}

var bareRepositoryEntries = map[string]bool{"branches": true, "common": true, "hooks": true, "info": true, "logs": true, "lost-found": true, "modules": true, "objects": true, "refs": true, "reftable": true, "remotes": true, "rr-cache": true, "svn": true, "worktrees": true}

func (r repoContext) instructionFilesBelow(gitlinks gitlinkSet) ([]string, error) {
	root, bare := r.Root, r.Root == r.Common
	var rels []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Dir(path) == root && ((bare && bareRepositoryEntries[d.Name()]) || (!bare && d.Name() == ".git")) {
				return filepath.SkipDir
			}
			if gitlinks.contains(rel) {
				return filepath.SkipDir
			}
			nested, err := r.repositoryBoundary(path)
			if err != nil {
				return err
			}
			if nested {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "AGENTS.md" {
			rels = append(rels, rel)
		}
		return nil
	})
	return rels, err
}

func (r repoContext) repositoryBoundary(dir string) (bool, error) {
	marker := filepath.Join(dir, ".git")
	present, err := entryPresent(marker)
	if err != nil {
		return false, err
	}
	if present {
		if _, err := gitOutput(r.Context, r.Start, "rev-parse", "--resolve-git-dir", marker); err != nil {
			return false, fmt.Errorf("%s is not a usable repository marker: %w", marker, err)
		}
		return true, nil
	}
	head, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if os.IsNotExist(err) || (err == nil && !head.Mode().IsRegular() && head.Mode()&os.ModeSymlink == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := accessible(filepath.Join(dir, "HEAD")); err != nil {
		return false, err
	}
	common := dir
	commonFile := filepath.Join(dir, "commondir")
	info, err := os.Stat(commonFile)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err == nil && info.Mode().IsRegular() {
		body, err := os.ReadFile(commonFile)
		if err != nil {
			return false, err
		}
		common = strings.TrimRight(string(body), "\r\n")
		if !filepath.IsAbs(common) {
			common = dir + string(os.PathSeparator) + common
		}
	}
	for _, name := range []string{"objects", "refs"} {
		if err := accessible(common + string(os.PathSeparator) + name); err != nil {
			return false, err
		}
	}
	_, stderr, err := gitOutputWithStderr(r.Context, r.Start, "rev-parse", "--resolve-git-dir", dir)
	if err == nil {
		return true, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == 128 {
		for _, line := range strings.Split(stderr, "\n") {
			if strings.HasPrefix(line, "fatal: not a gitdir ") {
				return false, nil
			}
		}
	}
	return false, err
}

func accessible(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := syscall.Access(path, 1); err != nil {
			return &os.PathError{Op: "access", Path: path, Err: err}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func entryPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (r repoContext) prepareSharedInstructions(rel, agent string, createsWorktree bool) error {
	source := filepath.Join(r.Root, rel)
	body, _, err := checkInstructionSource(source, true)
	if err != nil {
		return err
	}
	if createsWorktree {
		return fmt.Errorf("%s is not tracked by Git, so the worktree that %s --worktree creates after launch would start without it; create the worktree first and delegate into that directory", source, agent)
	}
	if r.Top == r.Root {
		return nil
	}
	target := filepath.Join(r.Top, rel)
	ours, err := r.claimTarget(target)
	if err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	if !ours {
		return nil
	}
	tracked, err := r.tracked(target)
	if err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	if tracked {
		return nil
	}
	current, targetPresent, err := checkInstructionSource(target, false)
	if err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	if targetPresent && current == body {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	lock, err := r.instructionLockPath()
	if err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	if err := atomicfile.SaveLocked(target, []byte(body), 0o644, lock); err != nil {
		return fmt.Errorf("cannot prepare %s from %s: %w", target, source, err)
	}
	written, _, err := checkInstructionSource(target, true)
	if err != nil {
		return fmt.Errorf("cannot verify %s prepared from %s: %w", target, source, err)
	}
	if written != body {
		return fmt.Errorf("%s does not match %s after preparation", target, source)
	}
	return nil
}

func (r repoContext) instructionLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "quota", "instruction-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(r.Common)))), nil
}

func checkInstructionSource(path string, required bool) (string, bool, error) {
	body, notice, present, err := readInstructions(path)
	if err != nil {
		return "", present, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if notice != "" {
		return "", present, errors.New(notice)
	}
	if required && !present {
		return "", false, fmt.Errorf("%s is missing", path)
	}
	return body, present, nil
}

func hasGitAncestor(dir string) (bool, error) {
	current, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return false, err
	}
	for {
		_, err := os.Lstat(filepath.Join(current, ".git"))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
}
