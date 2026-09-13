package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func CheckRepositoryAccountDestinations(ctx context.Context, dir, agent, sharedSource string, accountPaths []string, localFiles ...string) error {
	if !validRuntime(agent) {
		return fmt.Errorf("invalid agent %q", agent)
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return err
	}
	state, err := readState(r)
	if err != nil {
		return err
	}
	if sharedSource != "" {
		if sharedSource != "primary" && sharedSource != "checkout" {
			return fmt.Errorf("invalid shared source policy")
		}
		state.SharedSource = sharedSource
	}
	if err := registerLocalFiles(&state, localFiles); err != nil {
		return err
	}
	plans, err := r.managedExpectations(agent, state)
	if err != nil {
		return err
	}
	protected := make(map[string]plannedSettingsFile)
	for _, plan := range plans {
		protected[plan.Path] = plannedSettingsFile{Data: plan.Data, Remove: plan.Remove}
	}
	expected := make(map[string]sharedRuleExpectation)
	for _, worktree := range r.Checkouts() {
		expectation, err := r.sharedExpectation(worktree, state)
		if err != nil {
			return err
		}
		expected[worktree] = expectation
	}
	var local *string
	if exists(r.localSource()) {
		text, err := readRule(r.localSource())
		if err != nil {
			return err
		}
		local = &text
	}
	if err := r.completePlannedSettings(agent, state, expected, local, protected); err != nil {
		return err
	}
	for _, path := range append([]string{sharedRule, localRule}, state.LocalFiles...) {
		protected[filepath.Join(r.Root, path)] = plannedSettingsFile{}
	}
	for _, worktree := range r.Checkouts() {
		protected[filepath.Join(worktree, sharedRule)] = plannedSettingsFile{}
	}
	for path := range state.Generated {
		if _, planned := protected[path]; !planned {
			protected[path] = plannedSettingsFile{}
		}
	}
	protected[statePath(r)] = plannedSettingsFile{}
	for _, path := range accountPaths {
		target, err := plannedSettingsTarget(path, protected)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("account destination %s: %w", path, err)
		}
		if _, overlap := protected[target]; overlap || plannedSettingsDirectory(target, protected) {
			return fmt.Errorf("account destination %s overlaps repository instruction source or managed destination %s", path, target)
		}
	}
	return nil
}
