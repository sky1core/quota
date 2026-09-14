package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type bridgeStatus struct {
	Exists, Exact, Normalizable bool
	Data                        []byte
}

func inspectBridge(p, marker string) (bridgeStatus, error) {
	s := bridgeStatus{}
	if !exists(p) {
		return s, nil
	}
	s.Exists = true
	b, e := readRegular(p)
	if e != nil {
		return s, e
	}
	s.Data = b
	s.Exact = bytes.Equal(b, []byte(marker)) || bytes.Equal(b, []byte(marker+"\n"))
	s.Normalizable = s.Exact
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return s, nil
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " \t"))
		}
	}
	if len(lines) == 1 && (lines[0] == marker || lines[0] == "@./"+marker[1:]) {
		s.Normalizable = true
	}
	return s, nil
}
func generatedLocal(text string) string { return localOwner + "\n\n" + text + "\n" }
func inspectBridgeWithOwnership(path, marker string, state RepositoryState) (bridgeStatus, error) {
	bridge, err := inspectBridge(path, marker)
	if err != nil || !bridge.Exists || state.Generated[path] == "" {
		return bridge, err
	}
	if state.Generated[path] != digest(bridge.Data) {
		return bridge, fmt.Errorf("%s has user edits; generated bridge preserved", path)
	}
	return bridge, checkRecordedGeneratedMode(path, state)
}
func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func appendPattern(path, pattern string) error {
	var b []byte
	var e error
	if exists(path) {
		b, e = readRegular(path)
		if e != nil {
			return e
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("%s is not UTF-8", path)
		}
	}
	next := withIgnorePattern(b, pattern)
	if bytes.Equal(b, next) {
		return nil
	}
	return atomicWrite(path, next, false, b)
}
func withIgnorePattern(data []byte, pattern string) []byte {
	if contains(strings.Split(string(data), "\n"), pattern) {
		return data
	}
	next := append([]byte{}, data...)
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	return append(next, []byte(pattern+"\n")...)
}
func (r repoContext) appendExclude(rel string) error {
	p := filepath.Join(r.Common, "info")
	if e := safeDirectory(p); e != nil {
		return e
	}
	return appendPattern(filepath.Join(p, "exclude"), rel)
}
func ignoreTargets(agent string) []string {
	out := []string{localRule}
	if agent == "all" || agent == "codex" {
		out = append(out, codexRule)
	}
	if agent == "all" || agent == "claude" {
		out = append(out, localBridge)
	}
	return out
}
func (r repoContext) ensureIgnores(agent string, changes *[]string, problems *[]string, localFiles ...string) {
	for _, rel := range append(ignoreTargets(agent), localFiles...) {
		var uncovered []string
		safe := true
		for _, w := range r.Checkouts() {
			tracked, e := r.tracked(w, rel)
			if e != nil {
				*problems = append(*problems, e.Error())
				safe = false
				continue
			}
			if tracked {
				*problems = append(*problems, fmt.Sprintf("%s is tracked in %s; untrack it before ignoring", rel, w))
				safe = false
				continue
			}
			ignored, e := r.ignored(w, rel)
			if e != nil {
				*problems = append(*problems, e.Error())
				safe = false
			} else if !ignored {
				uncovered = append(uncovered, w)
			}
		}
		if !safe {
			continue
		}
		if !r.Bare() && r.Top == r.Root && contains(uncovered, r.Root) && !contains(localFiles, rel) {
			p := filepath.Join(r.Root, ".gitignore")
			if e := appendPattern(p, rel); e != nil {
				*problems = append(*problems, e.Error())
				continue
			}
			*changes = append(*changes, p+": added ignore pattern "+rel)
		}
		pattern := rel
		if contains(localFiles, rel) {
			pattern = "/" + strings.ReplaceAll(rel, " ", "\\ ")
		}
		exclude := filepath.Join(r.Common, "info", "exclude")
		before, e := os.ReadFile(exclude)
		if e != nil && !os.IsNotExist(e) {
			*problems = append(*problems, e.Error())
			continue
		}
		if e := r.appendExclude(pattern); e != nil {
			*problems = append(*problems, e.Error())
			continue
		}
		after, e := readRegular(exclude)
		if e != nil {
			*problems = append(*problems, e.Error())
			continue
		}
		if !bytes.Equal(before, after) {
			*changes = append(*changes, exclude+": added ignore pattern "+rel)
		}
		for _, w := range r.Checkouts() {
			ignored, e := r.ignored(w, rel)
			if e != nil {
				*problems = append(*problems, e.Error())
			} else if !ignored {
				*problems = append(*problems, fmt.Sprintf("%s is still not ignored in %s; check negated ignore rules", rel, w))
			}
		}
	}
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
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
func ensureBridge(path, marker string, changes, problems *[]string) {
	s, e := inspectBridge(path, marker)
	if e != nil {
		*problems = append(*problems, e.Error())
		return
	}
	if s.Exists && !s.Normalizable {
		*problems = append(*problems, fmt.Sprintf("%s contains content beyond the %s import; move that content to the rule source file and leave the one-line bridge", path, marker))
		return
	}
	if s.Exact {
		return
	}
	if e = atomicWrite(path, []byte(marker+"\n"), false, s.Data); e != nil {
		*problems = append(*problems, e.Error())
		return
	}
	*changes = append(*changes, path+": created or normalized "+marker+" bridge")
}
func (r repoContext) refreshLocal(w string, text *string, state *RepositoryState) (bool, error) {
	p := filepath.Join(w, localBridge)
	present := exists(p)
	if !present && text == nil {
		return false, nil
	}
	if e := r.ensureLocalTarget(w); e != nil {
		return false, e
	}
	previous, e := r.inspectLocalCopy(w, *state)
	if e != nil {
		return false, e
	}
	if text == nil {
		if e := os.Remove(p); e != nil {
			return false, e
		}
		delete(state.Generated, p)
		delete(state.GeneratedModes, p)
		return true, nil
	}
	expected := []byte(generatedLocal(*text))
	if len(expected) > maxRuleBytes {
		return false, fmt.Errorf("%s exceeds %d-byte file size limit", p, maxRuleBytes)
	}
	if present && bytes.Equal(previous, expected) {
		info, e := os.Stat(p)
		if e != nil {
			return false, e
		}
		if info.Mode().Perm()&^0o600 == 0 {
			return false, recordGeneratedFile(state, p, expected)
		}
	}
	if e := atomicWrite(p, expected, true, previous); e != nil {
		return false, e
	}
	return true, recordGeneratedFile(state, p, expected)
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
func (r repoContext) prepareShared(state *RepositoryState, changes *[]string) error {
	if state.SharedSource != "primary" || len(r.Copies()) == 0 {
		return nil
	}
	var source sharedRuleExpectation
	if r.ValidatedCodexPlan != nil {
		source = r.ValidatedCodexPlan.shared[r.Root]
	} else {
		var err error
		source, err = r.sharedExpectation(r.Root, *state)
		if err != nil {
			return err
		}
	}
	if !source.Present {
		return fmt.Errorf("primary shared source is missing: %s", source.Source)
	}
	for _, w := range r.Copies() {
		var expectation sharedRuleExpectation
		var e error
		if r.ValidatedCodexPlan != nil {
			expectation = r.ValidatedCodexPlan.shared[w]
		} else {
			expectation, e = r.sharedExpectation(w, *state)
			if e != nil {
				return e
			}
		}
		previous, e := r.inspectSharedCopy(expectation, *state)
		if e != nil {
			return e
		}
		if previous != nil && bytes.Equal(previous, expectation.Data) {
			if state.Generated[expectation.Path] != "" {
				if err := recordGeneratedFile(state, expectation.Path, expectation.Data); err != nil {
					return err
				}
			}
			continue
		}
		if e = atomicWrite(expectation.Path, expectation.Data, false, previous); e != nil {
			return e
		}
		if err := recordGeneratedFile(state, expectation.Path, expectation.Data); err != nil {
			return err
		}
		*changes = append(*changes, expectation.Path+": copied primary shared source")
	}
	return nil
}
func (r repoContext) preflight(agent string, state RepositoryState) []string {
	out := r.topologyProblems()
	if err := r.requiredSourceProblem(state); err != nil {
		out = append(out, err.Error())
	}
	if err := r.ensureLocalSource(); err != nil {
		out = append(out, err.Error())
	}
	var local *string
	if exists(r.localSource()) {
		text, err := readRule(r.localSource())
		if err != nil {
			out = append(out, err.Error())
		} else {
			local = &text
		}
	}
	plannedFiles := map[string]plannedSettingsFile{}
	plans, err := r.planManagedFiles(agent, state)
	if err != nil {
		out = append(out, err.Error())
	} else {
		for _, plan := range plans {
			plannedFiles[plan.Path] = plannedSettingsFile{Data: plan.Data, Remove: plan.Remove}
		}
	}
	expected := map[string]sharedRuleExpectation{}
	for _, w := range r.Checkouts() {
		expectation, err := r.sharedExpectation(w, state)
		if err != nil {
			out = append(out, err.Error())
		} else {
			expected[w] = expectation
			if state.SharedSource == "primary" && !expectation.Present && len(r.Copies()) > 0 {
				out = append(out, "primary shared source is missing: "+expectation.Source)
			}
			if state.SharedSource == "primary" && w != r.Root {
				if _, err := r.inspectSharedCopy(expectation, state); err != nil {
					out = append(out, err.Error())
				}
			}
			if (expectation.Present || state.Generated[filepath.Join(w, sharedBridge)] != "") && (agent == "all" || agent == "claude") {
				bridge, err := inspectBridgeWithOwnership(filepath.Join(w, sharedBridge), "@AGENTS.md", state)
				if err != nil {
					out = append(out, err.Error())
				} else if bridge.Exists && !bridge.Normalizable {
					out = append(out, filepath.Join(w, sharedBridge)+" contains content beyond the @AGENTS.md import")
				}
			}
			if !expectation.Present && state.SharedSource == "checkout" && exists(filepath.Join(r.Root, sharedRule)) {
				out = append(out, w+" has no AGENTS.md; checkout policy requires its own source")
			}
		}
		for _, rel := range ignoreTargets(agent) {
			tracked, err := r.tracked(w, rel)
			if err != nil {
				out = append(out, err.Error())
			} else if tracked {
				out = append(out, fmt.Sprintf("%s is tracked in %s", rel, w))
			}
		}
	}
	for _, path := range []string{filepath.Join(r.Common, "info", "exclude"), filepath.Join(r.Root, ".gitignore")} {
		if exists(path) {
			if _, err := readRegular(path); err != nil {
				out = append(out, err.Error())
			}
		}
	}
	if agent == "all" || agent == "claude" {
		if !r.Bare() && (local != nil || state.Generated[filepath.Join(r.Root, localBridge)] != "") {
			path := filepath.Join(r.Root, localBridge)
			bridge, err := inspectBridgeWithOwnership(path, "@AGENTS.local.md", state)
			if err != nil {
				out = append(out, err.Error())
			} else if bridge.Exists && !bridge.Normalizable {
				out = append(out, path+" contains content beyond the @AGENTS.local.md import")
			}
		}
		for _, w := range r.Copies() {
			if _, err := r.inspectLocalCopy(w, state); err != nil {
				out = append(out, err.Error())
			}
		}
		if notice := claudeNativeRefusal(); notice != "" {
			out = append(out, notice)
		}
	}
	if err := r.completePlannedSettings(agent, state, expected, local, plannedFiles); err != nil {
		out = append(out, err.Error())
	}
	out = append(out, r.plannedIgnoreProblems(agent, state, plannedFiles)...)
	if agent == "all" || agent == "claude" {
		problems, _ := claudeSettingsFindingsWithPlannedFiles(r, plannedFiles, expected)
		out = append(out, problems...)
	}
	if agent == "all" || agent == "codex" {
		problems, _ := codexSettingsFindingsWithPlannedFiles(r, plannedFiles, expected)
		out = append(out, problems...)
	}
	return out
}
func PlanRepository(ctx context.Context, dir, agent string) ([]string, error) {
	if !validRuntime(agent) {
		return nil, fmt.Errorf("invalid agent %q", agent)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		return nil, e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return nil, e
	}
	state, e := readState(r)
	if e != nil {
		return nil, e
	}
	if problems := r.preflight(agent, state); len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return r.plannedPaths(agent, state)
}

func PlanRepositoryWithPolicy(ctx context.Context, dir, agent, sharedSource string, localFiles ...string) ([]string, error) {
	return PlanRepositoryWithCodexHookRemovals(ctx, dir, agent, sharedSource, nil, localFiles...)
}

func PlanRepositoryWithCodexHookRemovals(ctx context.Context, dir, agent, sharedSource string, removals []string, localFiles ...string) ([]string, error) {
	if !validRuntime(agent) {
		return nil, fmt.Errorf("invalid agent %q", agent)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		return nil, e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return nil, e
	}
	r.CodexHookRemovals = removals
	state, e := readState(r)
	if e != nil {
		return nil, e
	}
	if sharedSource != "" {
		if sharedSource != "primary" && sharedSource != "checkout" {
			return nil, fmt.Errorf("invalid shared source policy")
		}
		state.SharedSource = sharedSource
	}
	if e = registerLocalFiles(&state, localFiles); e != nil {
		return nil, e
	}
	if p := r.preflight(agent, state); len(p) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(p, "; "))
	}
	return r.plannedPaths(agent, state)
}
func SetupRepository(ctx context.Context, dir, agent, sharedSource string, localFiles ...string) error {
	return SetupRepositoryWithCodexHookRemovals(ctx, dir, agent, sharedSource, nil, localFiles...)
}

func SetupRepositoryWithCodexHookRemovals(ctx context.Context, dir, agent, sharedSource string, removals []string, localFiles ...string) error {
	if !validRuntime(agent) {
		return fmt.Errorf("invalid agent %q", agent)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		return e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return e
	}
	r.CodexHookRemovals = removals
	return withRepositoryLock(r, func() error {
		s, e := readState(r)
		if e != nil {
			return e
		}
		if sharedSource != "" {
			if sharedSource != "primary" && sharedSource != "checkout" {
				return fmt.Errorf("invalid shared source policy")
			}
			s.SharedSource = sharedSource
		}
		if e = registerLocalFiles(&s, localFiles); e != nil {
			return e
		}
		if p := r.preflight(agent, s); len(p) > 0 {
			return fmt.Errorf("%s", strings.Join(p, "; "))
		}
		if e = writeState(r, s); e != nil {
			return e
		}
		var out bytes.Buffer
		code, e := setupRepository(r, agent, &out, &out)
		if e != nil {
			return fmt.Errorf("%s%w", out.String(), e)
		}
		if code != 0 {
			return fmt.Errorf("%s", strings.TrimSpace(out.String()))
		}
		return nil
	})
}
func registerLocalFiles(state *RepositoryState, paths []string) error {
	var unique []string
	for _, path := range paths {
		if !contains(unique, path) {
			unique = append(unique, path)
		}
	}
	paths = unique
	if err := validateLocalFiles(paths); err != nil {
		return err
	}
	for _, path := range paths {
		if !contains(state.LocalFiles, path) {
			state.LocalFiles = append(state.LocalFiles, path)
		}
	}
	sort.Strings(state.LocalFiles)
	return nil
}
func UninstallRepository(ctx context.Context, dir, agent string) error {
	return uninstallRepository(ctx, dir, agent, false)
}

func ValidateRepositoryUninstall(ctx context.Context, dir, agent string) error {
	return uninstallRepository(ctx, dir, agent, true)
}

func uninstallRepository(ctx context.Context, dir, agent string, dryRun bool) error {
	if !validRuntime(agent) {
		return fmt.Errorf("invalid agent %q", agent)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		return e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return e
	}
	uninstall := func() error {
		s, e := readState(r)
		if e != nil {
			return e
		}
		if agent == "codex" || agent == "all" {
			s.NativeCodex = false
		}
		if agent == "all" {
			s.Disabled["claude"] = true
			s.Disabled["codex"] = true
		} else {
			s.Disabled[agent] = true
		}
		if !dryRun {
			if e = writeState(r, s); e != nil {
				return e
			}
		}
		removeManaged := func(copies bool) error {
			plans, err := r.planManagedInstructionRemovals(s, copies)
			if err != nil || dryRun {
				return err
			}
			var changes []string
			return r.applyManagedFiles(plans, &s, &changes)
		}
		var problems []string
		if agent == "all" || agent == "claude" {
			for _, w := range r.Checkouts() {
				for _, rel := range []string{localBridge, sharedBridge} {
					p := filepath.Join(w, rel)
					if !exists(p) {
						continue
					}
					tracked, e := r.tracked(w, rel)
					if e != nil {
						problems = append(problems, e.Error())
						continue
					}
					if tracked {
						problems = append(problems, p+" preserved: tracked native bridge can continue loading instructions")
						continue
					}
					b, e := readRegular(p)
					if e != nil {
						problems = append(problems, e.Error())
						continue
					}
					known := s.Generated[p]
					if known == "" || known != digest(b) {
						problems = append(problems, p+" preserved: ownership or unchanged content could not be verified")
						continue
					}
					if e = checkRecordedGeneratedMode(p, s); e != nil {
						problems = append(problems, e.Error())
						continue
					}
					if !dryRun {
						if e = os.Remove(p); e != nil {
							problems = append(problems, e.Error())
							continue
						}
					}
					delete(s.Generated, p)
					delete(s.GeneratedModes, p)
				}
			}
		}
		if s.Disabled["claude"] && s.Disabled["codex"] {
			if err := removeManaged(true); err != nil {
				problems = append(problems, err.Error())
			} else {
				s.LocalFiles = nil
			}
			for _, w := range r.Copies() {
				p := filepath.Join(w, sharedRule)
				known := s.Generated[p]
				if known == "" || !exists(p) {
					continue
				}
				tracked, e := r.tracked(w, sharedRule)
				if e != nil {
					problems = append(problems, e.Error())
					continue
				}
				if tracked {
					continue
				}
				data, e := readRegular(p)
				if e != nil {
					problems = append(problems, e.Error())
					continue
				}
				if digest(data) != known {
					problems = append(problems, p+" preserved: generated source has user edits")
					continue
				}
				if e = checkRecordedGeneratedMode(p, s); e != nil {
					problems = append(problems, e.Error())
					continue
				}
				if !dryRun {
					if e = os.Remove(p); e != nil {
						problems = append(problems, e.Error())
						continue
					}
				}
				delete(s.Generated, p)
				delete(s.GeneratedModes, p)
			}
		} else if agent == "all" || agent == "codex" {
			if err := removeManaged(false); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if !dryRun {
			if e = writeState(r, s); e != nil {
				return e
			}
		}
		if len(problems) > 0 {
			if dryRun {
				return fmt.Errorf("repository uninstall conflicts; %s", strings.Join(problems, "; "))
			}
			return fmt.Errorf("repository disabled; %s", strings.Join(problems, "; "))
		}
		return nil
	}
	if dryRun {
		return uninstall()
	}
	return withRepositoryLock(r, uninstall)
}

func (r repoContext) planManagedInstructionRemovals(state RepositoryState, copies bool) ([]managedInstructionFile, error) {
	var plans []managedInstructionFile
	for _, worktree := range r.Checkouts() {
		paths := []string{codexRule}
		if copies && worktree != r.Root {
			paths = append(paths, localRule)
			paths = append(paths, state.LocalFiles...)
		}
		for _, rel := range paths {
			path := filepath.Join(worktree, rel)
			if !exists(path) && state.Generated[path] == "" {
				continue
			}
			plan := managedInstructionFile{Path: path, Remove: true}
			var err error
			plan.Previous, err = r.inspectManagedFile(plan, state, true)
			if err != nil {
				return nil, err
			}
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

func (r repoContext) plannedPaths(agent string, state RepositoryState) ([]string, error) {
	plans, err := r.managedExpectations(agent, state)
	if err != nil {
		return nil, err
	}
	removals := make(map[string]bool)
	for _, plan := range plans {
		removals[plan.Path] = plan.Remove
	}
	paths := []string{"repository: " + r.Top, "shared source policy: " + state.SharedSource, "repository state: " + statePath(r)}
	for _, w := range r.Checkouts() {
		if (agent == "codex" || agent == "all") && (exists(r.localSource()) || state.Generated[filepath.Join(w, codexRule)] != "") {
			operation := "native merged instructions: "
			if !exists(r.localSource()) {
				operation = "remove native merged instructions: "
			}
			paths = append(paths, operation+filepath.Join(w, codexRule))
		}
		if w != r.Root {
			if exists(r.localSource()) {
				paths = append(paths, "managed local source copy: "+filepath.Join(w, localRule))
			} else if state.Generated[filepath.Join(w, localRule)] != "" {
				paths = append(paths, "remove managed local source copy: "+filepath.Join(w, localRule))
			}
			for _, rel := range state.LocalFiles {
				paths = append(paths, "managed local file: "+filepath.Join(w, rel))
			}
		}
		if state.SharedSource == "primary" && w != r.Root {
			paths = append(paths, "managed shared copy: "+filepath.Join(w, sharedRule))
		}
		if agent == "all" || agent == "claude" {
			if removals[filepath.Join(w, sharedBridge)] {
				paths = append(paths, "remove native shared bridge: "+filepath.Join(w, sharedBridge))
			} else if exists(filepath.Join(w, sharedRule)) || state.SharedSource == "primary" && exists(filepath.Join(r.Root, sharedRule)) {
				paths = append(paths, "native shared bridge: "+filepath.Join(w, sharedBridge))
			}
			if exists(r.localSource()) || exists(filepath.Join(w, localBridge)) {
				paths = append(paths, "native private bridge or copy: "+filepath.Join(w, localBridge))
			}
		}
	}
	needExclude := false
	exclude := filepath.Join(r.Common, "info", "exclude")
	excludeBytes, excludeErr := readRegular(exclude)
	for _, rel := range append(ignoreTargets(agent), state.LocalFiles...) {
		pattern := rel
		if contains(state.LocalFiles, rel) {
			pattern = "/" + strings.ReplaceAll(rel, " ", "\\ ")
		}
		if excludeErr != nil || !contains(strings.Split(string(excludeBytes), "\n"), pattern) {
			needExclude = true
		}
		for _, w := range r.Checkouts() {
			ignored, e := r.ignored(w, rel)
			if e != nil || !ignored {
				if r.Top == r.Root && !r.Bare() && w == r.Root && !contains(state.LocalFiles, rel) {
					p := "ignore patterns: " + filepath.Join(r.Root, ".gitignore")
					if !contains(paths, p) {
						paths = append(paths, p)
					}
				}
			}
		}
	}
	if needExclude {
		paths = append(paths, "ignore patterns: "+exclude)
	}
	return paths, nil
}
