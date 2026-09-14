package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
var slugReplace = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func worktreeRepoDir(r repoContext) (string, error) {
	base := os.Getenv("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR")
	if base == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(home, ".cache", "agents-local-overlay", "claude-worktrees")
	}
	if strings.HasPrefix(base, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(home, base[2:])
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("AGENTS_OVERLAY_CLAUDE_WORKTREE_DIR must be an absolute path")
	}
	slug := strings.Trim(slugReplace.ReplaceAllString(filepath.Base(r.Root), "-"), ".-")
	if slug == "" {
		slug = "repo"
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.Common)))[:12]
	return filepath.Join(base, slug+"-"+key), nil
}
func (r repoContext) hasExistingGeneratedFiles(state RepositoryState) bool {
	for _, checkout := range r.Checkouts() {
		for path := range state.Generated {
			if instructionPathWithin(path, checkout) && exists(path) {
				return true
			}
		}
	}
	return false
}

func createWorktree(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	return createWorktreeWithNativeCodexPreflight(ctx, stdin, stdout, nil)
}

func CreateWorktreeWithNativeCodexPreflight(ctx context.Context, stdin io.Reader, stdout io.Writer, preflight func(context.Context, CodexSetupPlan) (ValidatedCodexSetup, error)) error {
	if preflight == nil {
		return fmt.Errorf("native Codex preflight is required")
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	return createWorktreeWithNativeCodexPreflight(ctx, stdin, stdout, preflight)
}

func createWorktreeWithNativeCodexPreflight(ctx context.Context, stdin io.Reader, stdout io.Writer, nativePreflight func(context.Context, CodexSetupPlan) (ValidatedCodexSetup, error)) error {
	h, e := parseHook(stdin)
	if e != nil {
		return e
	}
	dir, e := hookString(h, "cwd")
	if e != nil {
		return e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return e
	}
	name, e := hookString(h, "name")
	if e != nil {
		return e
	}
	if !slugPattern.MatchString(name) || strings.HasSuffix(name, ".") {
		return fmt.Errorf("WorktreeCreate name must be a simple slug and not dot-like")
	}
	repoDir, e := worktreeRepoDir(r)
	if e != nil {
		return e
	}
	target := resolvePath(filepath.Join(repoDir, name))
	branch := "agents-overlay/" + name
	return withRepositoryLock(r, func() error {
		state, e := readState(r)
		if e != nil {
			return e
		}
		if exists(target) {
			return fmt.Errorf("worktree target already exists: %s", target)
		}
		if contains(r.Worktrees, resolvePath(target)) {
			return fmt.Errorf("%s is still registered as a worktree but its directory is gone; run git worktree prune", target)
		}
		for _, w := range r.Worktrees {
			if within(target, w) {
				return fmt.Errorf("worktree target must be outside every existing worktree: %s", target)
			}
		}
		_, e = gitOutput(ctx, r.Top, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		present, e := gitBoolean(e)
		if e != nil {
			return e
		}
		if present {
			return fmt.Errorf("branch %s already exists from an earlier worktree named %s", branch, name)
		}
		var local *string
		if !state.Disabled["claude"] {
			if refusal := claudeNativeRefusal(); refusal != "" {
				return fmt.Errorf("%s", refusal)
			}
			if problems, _ := claudeSettingsFindings(r); len(problems) > 0 {
				return fmt.Errorf("%s", strings.Join(problems, "; "))
			}
			if e = r.ensureLocalSource(); e != nil {
				return e
			}
			if exists(r.localSource()) {
				text, e := readRule(r.localSource())
				if e != nil {
					return e
				}
				local = &text
			}
		}
		if e = safeDirectory(filepath.Dir(repoDir)); e != nil {
			return e
		}
		if e = safeDirectory(repoDir); e != nil {
			return e
		}
		base, ok := os.LookupEnv("AGENTS_OVERLAY_CLAUDE_WORKTREE_BASE_REF")
		if !ok {
			base = "HEAD"
		}
		if strings.HasPrefix(base, "-") || base == "" {
			return fmt.Errorf("invalid worktree base ref")
		}
		if _, e = gitOutput(ctx, r.Top, "worktree", "add", "-q", "-b", branch, target, base); e != nil {
			return e
		}
		prepareErr := func() error {
			prepared, err := resolveContext(ctx, target)
			if err != nil {
				return err
			}
			if !state.NativeCodex && state.SharedSource == "checkout" && !r.hasExistingGeneratedFiles(state) && len(state.LocalFiles) == 0 &&
				!exists(r.localSource()) && !exists(filepath.Join(r.Root, sharedRule)) && !exists(filepath.Join(target, sharedRule)) &&
				!exists(filepath.Join(target, localRule)) && !exists(filepath.Join(target, localBridge)) {
				if !state.Disabled["claude"] {
					if problems, _ := claudeSettingsFindings(prepared); len(problems) > 0 {
						return fmt.Errorf("%s", strings.Join(problems, "; "))
					}
				}
				return nil
			}
			managed := prepared
			managed.Worktrees = []string{resolvePath(target)}
			agent := "claude"
			codexEnabled := state.NativeCodex && !state.Disabled["codex"]
			if codexEnabled {
				agent = "codex"
				if !state.Disabled["claude"] {
					agent = "all"
				}
			}
			if !state.Disabled["claude"] || codexEnabled {
				var plans []managedInstructionFile
				if codexEnabled && nativePreflight != nil {
					plan, err := planCodexDocuments(ctx, target, agent, "", true)
					if err != nil {
						return err
					}
					managed.NativeCodexSettings = true
					if problems := managed.preflight(agent, state); len(problems) > 0 {
						return fmt.Errorf("%s", strings.Join(problems, "; "))
					}
					validated, err := nativePreflight(ctx, plan)
					if err != nil {
						return err
					}
					if err := validated.CheckConsistency(ctx); err != nil {
						return err
					}
					managed.NativeCodexHookFiles = validated.ActiveHookFiles
					managed.ValidatedCodexPlan = &validated.Plan
					plans, local = validated.Plan.managed, validated.Plan.local
				} else {
					if problems := managed.preflight(agent, state); len(problems) > 0 {
						return fmt.Errorf("%s", strings.Join(problems, "; "))
					}
					var err error
					plans, err = managed.planManagedFiles(agent, state)
					if err != nil {
						return err
					}
				}
				var changes, problems []string
				managed.ensureIgnores(agent, &changes, &problems, state.LocalFiles...)
				if len(problems) > 0 {
					return fmt.Errorf("%s", strings.Join(problems, "; "))
				}
				if err := managed.prepareShared(&state, &changes); err != nil {
					return err
				}
				err := managed.applyManagedFiles(plans, &state, &changes)
				if saveErr := writeState(r, state); saveErr != nil {
					return saveErr
				}
				if err != nil {
					return err
				}
			}
			if state.Disabled["claude"] {
				return nil
			}
			{
				for _, rel := range ignoreTargets("claude") {
					tracked, e := r.tracked(target, rel)
					if e != nil {
						return e
					}
					if tracked {
						return fmt.Errorf("%s is tracked in %s", rel, target)
					}
					ignored, e := r.ignored(target, rel)
					if e != nil {
						return e
					}
					if !ignored {
						if e = r.appendExclude(rel); e != nil {
							return e
						}
						ignored, e = r.ignored(target, rel)
						if e != nil {
							return e
						}
						if !ignored {
							return fmt.Errorf("%s is still not ignored in %s; check negated ignore rules", rel, target)
						}
					}
				}
				if _, e = r.refreshLocal(target, local, &state); e != nil {
					return e
				}
			}
			shared := filepath.Join(target, sharedRule)
			if !exists(shared) && exists(filepath.Join(r.Root, sharedRule)) {
				return fmt.Errorf("%s has no AGENTS.md; checkout shared-source policy requires its own source", target)
			}
			if exists(shared) {
				if _, e = readRule(shared); e != nil {
					return e
				}
				bridge := filepath.Join(target, sharedBridge)
				wasPresent := exists(bridge)
				var changes, problems []string
				ensureBridge(bridge, "@AGENTS.md", &changes, &problems)
				if len(problems) > 0 {
					return fmt.Errorf("%s", problems[0])
				}
				if !wasPresent {
					b, e := readRegular(bridge)
					if e != nil {
						return e
					}
					if err := recordGeneratedFile(&state, bridge, b); err != nil {
						return err
					}
				}
			}
			prepared, e = resolveContext(ctx, target)
			if e != nil {
				return e
			}
			if problems, _ := claudeSettingsFindings(prepared); len(problems) > 0 {
				return fmt.Errorf("%s", strings.Join(problems, "; "))
			}
			return writeState(r, state)
		}()
		if prepareErr != nil {
			leftovers := discardWorktree(r, target, branch)
			if len(leftovers) > 0 {
				return fmt.Errorf("%w; rollback incomplete: %s", prepareErr, strings.Join(leftovers, ", "))
			}
			return prepareErr
		}
		_, e = fmt.Fprintln(stdout, resolvePath(target))
		return e
	})
}
func removalFile(path string) ([]byte, error) {
	var before unix.Stat_t
	if e := unix.Lstat(path, &before); e != nil {
		return nil, fmt.Errorf("%s: %w", path, e)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("%s is not a regular file owned by the current user", path)
	}
	b, e := readRegular(path)
	if e != nil {
		return nil, e
	}
	var after unix.Stat_t
	if e = unix.Lstat(path, &after); e != nil {
		return nil, e
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return nil, fmt.Errorf("%s changed during removal inspection", path)
	}
	return b, nil
}
func ensureWorktreeRemovable(r repoContext, target string) error {
	if e := inspectRemoval(r, target); e != nil {
		return fmt.Errorf("refusing to remove worktree %s: %w", target, e)
	}
	return nil
}
func inspectRemoval(r repoContext, target string) error {
	data, e := gitOutput(r.Context, target, "ls-files", "--cached", "-v", "-z")
	if e != nil {
		return e
	}
	tracked := map[string]bool{}
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if !bytes.HasPrefix(entry, []byte("H ")) {
			return fmt.Errorf("index flags prevent reliable change detection; restore ordinary index flags before removal")
		}
		if !utf8.Valid(entry) {
			return fmt.Errorf("inspection found a non-UTF8 name or rule")
		}
		tracked[string(entry[2:])] = true
	}
	state, e := readState(r)
	if e != nil {
		return e
	}
	managed := r
	managed.Top, managed.Start = target, target
	managed.Worktrees = []string{target}
	status, e := gitOutput(r.Context, target, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if e != nil {
		return e
	}
	if len(status) > 0 {
		return fmt.Errorf("worktree has modified or untracked files")
	}
	managedData := make(map[string][]byte)
	hasManaged := false
	managedAgent := "claude"
	for path := range state.Generated {
		if _, rel, err := managed.managedDestination(path, state); err == nil && exists(path) {
			hasManaged = true
			if rel == codexRule {
				managedAgent = "all"
			}
		}
	}
	if hasManaged {
		expected, err := managed.managedExpectations(managedAgent, state)
		if err != nil {
			return err
		}
		for _, plan := range expected {
			if state.Generated[plan.Path] != "" {
				previous, err := managed.inspectManagedFile(plan, state, false)
				if err != nil {
					return err
				}
				if previous != nil && (plan.Remove || !bytes.Equal(previous, plan.Data)) {
					return fmt.Errorf("generated file %s differs from its current source", plan.Path)
				}
				if previous != nil {
					if err := checkRecordedGeneratedMode(plan.Path, state); err != nil {
						return err
					}
					info, err := os.Lstat(plan.Path)
					if err != nil {
						return err
					}
					if (info.Mode().Perm()&0o100 != 0) != plan.OwnerExecutable {
						return fmt.Errorf("generated file %s execution permission differs from its current source", plan.Path)
					}
				}
			}
			if !plan.Remove {
				managedData[plan.Path] = plan.Data
			}
		}
	}
	var walk func(string) error
	walk = func(dir string) error {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		if len(entries) == 0 && dir != target {
			rel, _ := filepath.Rel(target, dir)
			ignored, e := r.ignored(target, filepath.ToSlash(rel)+"/")
			if e != nil {
				return e
			}
			if ignored {
				return fmt.Errorf("ignored user directory %s is not regeneratable", dir)
			}
		}
		for _, entry := range entries {
			p := filepath.Join(dir, entry.Name())
			rel, _ := filepath.Rel(target, p)
			rel = filepath.ToSlash(rel)
			if !utf8.ValidString(rel) {
				return fmt.Errorf("inspection found a non-UTF8 name or rule")
			}
			if rel == ".git" {
				continue
			}
			if entry.IsDir() {
				if e = walk(p); e != nil {
					return e
				}
				continue
			}
			if tracked[rel] {
				continue
			}
			ignored, e := r.ignored(target, rel)
			if e != nil {
				return e
			}
			if !ignored && state.Generated[p] == "" {
				continue
			}
			managedPath := p
			if _, known := managedData[p]; !known {
				for candidate := range managedData {
					same, _, err := sameExistingSettingsEntry(p, candidate)
					if err != nil {
						return err
					}
					if same {
						managedPath = candidate
						break
					}
				}
			}
			if expected, ok := managedData[managedPath]; ok {
				body, err := removalFile(p)
				if err != nil {
					return err
				}
				if state.Generated[managedPath] == "" || state.Generated[managedPath] != digest(body) || !bytes.Equal(body, expected) {
					return fmt.Errorf("ignored generated file %s differs from its current source", p)
				}
				continue
			}
			if state.Generated[p] != "" && (rel == sharedRule || rel == sharedBridge || rel == localBridge) {
				if err := checkRecordedGeneratedMode(p, state); err != nil {
					return err
				}
			}
			if rel == sharedBridge {
				state, e := readState(r)
				if e != nil {
					return e
				}
				b, e := removalFile(p)
				if e != nil {
					return e
				}
				if state.Generated[p] == "" || state.Generated[p] != digest(b) || (string(b) != "@AGENTS.md\n" && string(b) != "@AGENTS.md") {
					return fmt.Errorf("ignored generated file %s differs from its current source", p)
				}
				continue
			}
			if rel == sharedRule {
				state, e := readState(r)
				if e != nil {
					return e
				}
				data, e := removalFile(p)
				if e != nil {
					return e
				}
				source, e := removalFile(filepath.Join(r.Root, sharedRule))
				if e != nil {
					return e
				}
				if state.Generated[p] == "" || state.Generated[p] != digest(data) || !bytes.Equal(data, source) {
					return fmt.Errorf("ignored generated file %s differs from its current source", p)
				}
				continue
			}
			if rel != localBridge {
				return fmt.Errorf("ignored user file %s is not regeneratable", p)
			}
			if e = r.ensureLocalSource(); e != nil {
				return e
			}
			source, e := removalFile(r.localSource())
			if e != nil {
				return e
			}
			text, e := decodeRule(source, r.localSource())
			if e != nil {
				return e
			}
			copy, e := removalFile(p)
			if e != nil {
				return e
			}
			if state.Generated[p] == "" || state.Generated[p] != digest(copy) || string(copy) != generatedLocal(text) {
				return fmt.Errorf("ignored generated file %s differs from its current source", p)
			}
		}
		return nil
	}
	return walk(target)
}
func discardWorktree(r repoContext, target, branch string) []string {
	if e := ensureWorktreeRemovable(r, target); e != nil {
		return []string{fmt.Sprintf("worktree %s and branch %s kept: %v", target, branch, e)}
	}
	if e := removeManagedWorktree(r, target); e != nil {
		return []string{fmt.Sprintf("worktree %s and branch %s kept: %v", target, branch, e)}
	}
	refs, e := gitOutput(r.Context, r.Root, "for-each-ref", "--contains", "refs/heads/"+branch, "--format=%(refname)", "refs/heads", "refs/remotes")
	if e != nil {
		return []string{"branch " + branch + " kept: " + e.Error()}
	}
	shared := false
	for _, ref := range strings.Split(string(refs), "\n") {
		if ref != "" && ref != "refs/heads/"+branch {
			shared = true
			break
		}
	}
	if !shared {
		return []string{"branch " + branch + " kept: its commits are on no other branch"}
	}
	if _, e := gitOutput(r.Context, r.Root, "branch", "-D", branch); e != nil {
		return []string{"branch " + branch + " still exists"}
	}
	return nil
}
func removeWorktree(ctx context.Context, stdin io.Reader, stderr io.Writer) error {
	h, e := parseHook(stdin)
	if e != nil {
		return e
	}
	path, e := hookString(h, "worktree_path")
	if e != nil {
		return e
	}
	r, e := resolveContext(ctx, path)
	if e != nil {
		return e
	}
	target := r.Top
	if r.Start != target || target == r.Root || !contains(r.Worktrees, target) {
		return fmt.Errorf("%s is not a linked worktree root", path)
	}
	repoDir, e := worktreeRepoDir(r)
	if e != nil {
		return e
	}
	if filepath.Dir(target) != resolvePath(repoDir) {
		return fmt.Errorf("%s was not created by the WorktreeCreate hook; remove it with git worktree remove", target)
	}
	branch := "agents-overlay/" + filepath.Base(target)
	return withRepositoryLock(r, func() error {
		if _, e := readState(r); e != nil {
			return e
		}
		head, e := gitOutput(ctx, target, "symbolic-ref", "--quiet", "HEAD")
		if e != nil || strings.TrimSpace(string(head)) != "refs/heads/"+branch {
			return fmt.Errorf("%s is not on branch %s", target, branch)
		}
		if e = ensureWorktreeRemovable(r, target); e != nil {
			return e
		}
		if e = removeManagedWorktree(r, target); e != nil {
			return e
		}
		refs, e := gitOutput(ctx, r.Root, "for-each-ref", "--contains", "refs/heads/"+branch, "--format=%(refname)", "refs/heads", "refs/remotes")
		if e != nil {
			return e
		}
		for _, ref := range strings.Split(string(refs), "\n") {
			if ref != "" && ref != "refs/heads/"+branch {
				_, e = gitOutput(ctx, r.Root, "branch", "-D", branch)
				return e
			}
		}
		fmt.Fprintf(stderr, "%s: kept branch %s; its commits are on no other branch\n", cliName, branch)
		return nil
	})
}

func removeManagedWorktree(r repoContext, target string) error {
	state, e := readState(r)
	if e != nil {
		return e
	}
	var patterns []string
	for _, rel := range append([]string{sharedRule, sharedBridge, localBridge, localRule, codexRule}, state.LocalFiles...) {
		p := filepath.Join(target, rel)
		known := state.Generated[p]
		if known == "" || !exists(p) {
			continue
		}
		tracked, e := r.tracked(target, rel)
		if e != nil {
			return e
		}
		if tracked {
			continue
		}
		data, e := removalFile(p)
		if e != nil {
			return e
		}
		if digest(data) != known {
			return fmt.Errorf("generated file %s has user edits; preserved", p)
		}
		if rel == sharedRule {
			source, e := removalFile(filepath.Join(r.Root, sharedRule))
			if e != nil {
				return e
			}
			if !bytes.Equal(data, source) {
				return fmt.Errorf("generated file %s differs from its current source", p)
			}
		}
		patterns = append(patterns, "/"+strings.ReplaceAll(rel, " ", "\\ "))
	}
	forgetRemovedFiles := func() error {
		for path := range state.Generated {
			if instructionPathWithin(path, target) {
				delete(state.Generated, path)
			}
		}
		for path := range state.GeneratedModes {
			if instructionPathWithin(path, target) {
				delete(state.GeneratedModes, path)
			}
		}
		return writeState(r, state)
	}
	if len(patterns) == 0 {
		if _, e = gitOutput(r.Context, r.Root, "worktree", "remove", target); e != nil {
			return e
		}
		return forgetRemovedFiles()
	}
	f, e := os.CreateTemp(r.Common, "quota-instructions-remove-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(strings.Join(patterns, "\n") + "\n"); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if _, e = gitOutput(r.Context, r.Root, "-c", "core.excludesFile="+f.Name(), "worktree", "remove", target); e != nil {
		return e
	}
	return forgetRemovedFiles()
}
