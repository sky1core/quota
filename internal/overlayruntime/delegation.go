package overlayruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	rel, err := filepath.Rel(r.Top, r.Start)
	if err != nil {
		return err
	}
	paths := []string{"AGENTS.md"}
	for ; rel != "."; rel = filepath.Dir(rel) {
		paths = append(paths, filepath.Join(rel, "AGENTS.md"))
	}
	for _, rel := range paths {
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

func (r repoContext) prepareSharedInstructions(rel, agent string, createsWorktree bool) error {
	source := filepath.Join(r.Root, rel)
	if !exists(source) {
		return nil
	}
	tracked, err := r.primaryTracked(source)
	if err != nil {
		return err
	}
	if tracked {
		return nil
	}
	body, present, err := checkInstructionSource(source, false)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if createsWorktree {
		return fmt.Errorf("%s is not tracked by Git, so the worktree that %s --worktree creates after launch would start without it; create the worktree first and delegate into that directory", source, agent)
	}
	if r.Top == r.Root {
		return nil
	}
	target := filepath.Join(r.Top, rel)
	tracked, err = r.tracked(target)
	if err != nil {
		return err
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
	if err := atomicfile.Save(target, []byte(body), 0o644, true); err != nil {
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

func (r repoContext) primaryTracked(path string) (bool, error) {
	if r.Root == r.Common {
		return false, nil
	}
	return r.tracked(path)
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
