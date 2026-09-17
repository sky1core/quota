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
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
var slugReplace = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

const (
	worktreeBranchPrefix = "quota-instructions/"
	worktreeDirEnv       = "QUOTA_INSTRUCTIONS_CLAUDE_WORKTREE_DIR"
	worktreeBaseRefEnv   = "QUOTA_INSTRUCTIONS_CLAUDE_WORKTREE_BASE_REF"
)

func worktreeRepoDir(r repoContext) (string, error) {
	base := os.Getenv(worktreeDirEnv)
	if base == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(home, ".cache", "quota", "instructions", "claude-worktrees")
	}
	if strings.HasPrefix(base, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(home, base[2:])
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("%s must be an absolute path", worktreeDirEnv)
	}
	slug := strings.Trim(slugReplace.ReplaceAllString(filepath.Base(r.Root), "-"), ".-")
	if slug == "" {
		slug = "repo"
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.Common)))[:12]
	return filepath.Join(base, slug+"-"+key), nil
}

func createWorktree(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	h, e := parseHook(stdin)
	if e != nil {
		return e
	}
	dir, e := hookString(h, "cwd")
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
	if e := ValidateGitEnvironment(ctx); e != nil {
		return e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return e
	}
	repoDir, e := worktreeRepoDir(r)
	if e != nil {
		return e
	}
	target := resolvePath(filepath.Join(repoDir, name))
	branch := worktreeBranchPrefix + name
	if exists(target) {
		return fmt.Errorf("worktree target already exists: %s", target)
	}
	if contains(r.Worktrees, target) {
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
	if e = verifyStateReadable(r); e != nil {
		return e
	}
	if e = safeDirectory(filepath.Dir(repoDir)); e != nil {
		return e
	}
	if e = safeDirectory(repoDir); e != nil {
		return e
	}
	base, ok := os.LookupEnv(worktreeBaseRefEnv)
	if !ok {
		base = "HEAD"
	}
	if strings.HasPrefix(base, "-") || base == "" {
		return fmt.Errorf("invalid worktree base ref")
	}
	if _, e = gitOutput(ctx, r.Top, "worktree", "add", "-q", "-b", branch, target, base); e != nil {
		return e
	}
	result, prepareErr := PrepareCheckout(ctx, target)
	if prepareErr != nil {
		leftovers := discardWorktree(r, target, branch)
		if len(leftovers) > 0 {
			return fmt.Errorf("%w; rollback incomplete: %s", prepareErr, strings.Join(leftovers, ", "))
		}
		return prepareErr
	}
	for _, reason := range result.SkipReasons() {
		fmt.Fprintln(stderr, cliName+": "+reason)
	}
	_, e = fmt.Fprintln(stdout, target)
	return e
}

func discardWorktree(r repoContext, target, branch string) []string {
	leftovers, e := removeLinkedWorktreeAndBranch(r, target, branch, nil)
	if e != nil {
		return []string{fmt.Sprintf("worktree %s and branch %s kept: %v", target, branch, e)}
	}
	return leftovers
}

func verifyStateReadable(r repoContext) error {
	return withStateLock(r, func() error {
		_, e := readState(r)
		return e
	})
}

func deleteWorktreeBranch(r repoContext, branch string, stderr io.Writer) []string {
	refs, e := gitOutput(r.Context, r.Root, "for-each-ref", "--contains", "refs/heads/"+branch, "--format=%(refname)", "refs/heads", "refs/remotes")
	if e != nil {
		return []string{"branch " + branch + " kept: " + e.Error()}
	}
	for _, ref := range strings.Split(string(refs), "\n") {
		if ref != "" && ref != "refs/heads/"+branch {
			if _, e := gitOutput(r.Context, r.Root, "branch", "-D", branch); e != nil {
				return []string{"branch " + branch + " still exists"}
			}
			return nil
		}
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "%s: kept branch %s; its commits are on no other branch\n", cliName, branch)
		return nil
	}
	return []string{"branch " + branch + " kept: its commits are on no other branch"}
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
	if e := ValidateGitEnvironment(ctx); e != nil {
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
	branch := worktreeBranchPrefix + filepath.Base(target)
	head, e := gitOutput(ctx, target, "symbolic-ref", "--quiet", "HEAD")
	if e != nil || strings.TrimSpace(string(head)) != "refs/heads/"+branch {
		return fmt.Errorf("%s is not on branch %s", target, branch)
	}
	if leftovers, e := removeLinkedWorktreeAndBranch(r, target, branch, stderr); e != nil {
		return e
	} else if len(leftovers) > 0 {
		return fmt.Errorf("%s", strings.Join(leftovers, ", "))
	}
	return nil
}

// removeLinkedWorktreeAndBranch removes target only when Git reports no user
// change, every ignored file is a quota-generated file, and every generated
// file passes the same guards preparation applies. The branch preservation
// check and branch deletion run under the same repository state lock so
// concurrent removals cannot both delete the final refs to the same commit.
func removeLinkedWorktreeAndBranch(r repoContext, target, branch string, stderr io.Writer) ([]string, error) {
	var leftovers []string
	err := withStateLock(r, func() error {
		state, e := readState(r)
		if e != nil {
			return e
		}
		if e = inspectRemoval(r, target, state); e != nil {
			return fmt.Errorf("refusing to remove worktree %s: %w", target, e)
		}
		actions := r.evaluateCheckoutRemoval(target, state)
		for _, a := range actions {
			if a.Action == actionSkip {
				return fmt.Errorf("refusing to remove worktree %s: %s: %s", target, a.Path, a.Reason)
			}
		}
		var res PrepareResult
		for _, a := range actions {
			r.applyAction(target, a, &state, &res)
		}
		if len(res.Skipped) > 0 {
			return fmt.Errorf("refusing to remove worktree %s: %s", target, strings.Join(res.SkipReasons(), "; "))
		}
		if _, e = gitOutput(r.Context, r.Root, "worktree", "remove", target); e != nil {
			return e
		}
		for path := range state.Generated {
			if within(path, target) {
				forgetGeneratedFile(&state, path)
			}
		}
		if e = writeState(r, state); e != nil {
			return e
		}
		leftovers = deleteWorktreeBranch(r, branch, stderr)
		return nil
	})
	return leftovers, err
}

// inspectRemoval refuses when the index skips change detection, when Git
// reports modified or untracked files, when a recorded generated file (the
// legacy CLAUDE.md bridge included) no longer matches its record, or when an
// ignored file is not a recorded quota-generated file.
func inspectRemoval(r repoContext, target string, state RepositoryState) error {
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
	status, e := gitOutput(r.Context, target, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if e != nil {
		return e
	}
	var dirty []string
	for _, entry := range bytes.Split(status, []byte{0}) {
		if len(entry) > 3 {
			dirty = append(dirty, string(entry[3:]))
		}
	}
	if len(dirty) > 0 {
		return fmt.Errorf("worktree has modified or untracked files: %s", strings.Join(dirty, ", "))
	}
	var walk func(string) error
	walk = func(dir string) error {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		if len(entries) == 0 && dir != target {
			return nil
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
			if state.Generated[p] != "" {
				if _, _, problem := inspectGenerated(p, state); problem != nil {
					return fmt.Errorf("generated file %s %v", p, problem)
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
			if ignored {
				return fmt.Errorf("ignored user file %s is not regeneratable", p)
			}
		}
		return nil
	}
	return walk(target)
}
