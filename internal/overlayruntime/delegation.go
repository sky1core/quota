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
	"unicode/utf8"

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
	var rels []string
	var err error
	if r.Root == r.Common {
		rels, err = instructionFilesBelow(r.Root)
	} else {
		rels, err = r.untrackedInstructionFiles()
	}
	if err != nil {
		return nil, err
	}
	var own []string
	for _, rel := range rels {
		ours, err := r.ownsDirectory(filepath.Join(r.Root, filepath.Dir(rel)))
		if err != nil {
			return nil, err
		}
		if ours {
			own = append(own, rel)
		}
	}
	return own, nil
}

func (r repoContext) ownsDirectory(dir string) (bool, error) {
	out, _, err := gitOutputWithEnv(r.Context, dir, []string{"GIT_DISCOVERY_ACROSS_FILESYSTEM=1"}, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return false, fmt.Errorf("cannot determine the repository owning %s: %w", dir, err)
	}
	gitDir, err := onePath(out)
	if err != nil {
		return false, fmt.Errorf("cannot determine the repository owning %s: %w", dir, err)
	}
	return gitDir == r.Common, nil
}

func (r repoContext) untrackedInstructionFiles() ([]string, error) {
	out, stderr, err := gitOutputWithStderr(r.Context, r.Root, "ls-files", "--others", "-z", "--", ":(glob)**/AGENTS.md")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "warning: could not open directory") {
			return nil, fmt.Errorf("git could not list every untracked file under %s: %s", r.Root, strings.TrimSpace(line))
		}
	}
	var rels []string
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if !utf8.Valid(entry) {
			return nil, fmt.Errorf("untracked AGENTS.md path under %s is not valid UTF-8", r.Root)
		}
		rels = append(rels, filepath.FromSlash(string(entry)))
	}
	return rels, nil
}

var bareRepositoryEntries = map[string]bool{"branches": true, "common": true, "hooks": true, "info": true, "logs": true, "lost-found": true, "modules": true, "objects": true, "refs": true, "reftable": true, "remotes": true, "rr-cache": true, "svn": true, "worktrees": true}

func instructionFilesBelow(root string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if (filepath.Dir(path) == root && bareRepositoryEntries[d.Name()]) || exists(filepath.Join(path, ".git")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "AGENTS.md" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		return nil
	})
	return rels, err
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
