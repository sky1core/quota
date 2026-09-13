package overlayruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const cliName = "quota-cli agent instructions"
const usage = "usage: " + cliName + " setup|check [--runtime=all|claude|codex] [dir]\n"

func validRuntime(v string) bool { return v == "all" || v == "claude" || v == "codex" }
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runCore(ctx, args, stdin, stdout, stderr)
}
func reject(stderr io.Writer, form string) int {
	fmt.Fprintf(stderr, "%s: malformed %s invocation\n%s", cliName, form, usage)
	return 2
}
func runCore(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return reject(stderr, "")
	}
	command := args[0]
	rest := args[1:]
	agent, dir := "all", "."
	switch command {
	case "setup", "check", "verify":
		seen, dirs := false, 0
		for _, arg := range rest {
			if strings.HasPrefix(arg, "--runtime=") && !seen {
				agent = strings.TrimPrefix(arg, "--runtime=")
				seen = true
				if !validRuntime(agent) {
					return reject(stderr, command)
				}
			} else if strings.HasPrefix(arg, "-") {
				return reject(stderr, command)
			} else {
				dir = arg
				dirs++
			}
		}
		if dirs > 1 {
			return reject(stderr, command)
		}
	case "json":
		if len(rest) != 5 {
			return reject(stderr, command)
		}
	case "claude-worktree-create", "claude-worktree-remove":
		if len(rest) != 0 {
			return reject(stderr, command)
		}
	default:
		return reject(stderr, command)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	var e error
	code := 0
	switch command {
	case "setup", "check", "verify":
		var r repoContext
		r, e = resolveContext(ctx, dir)
		if e == nil {
			if command == "setup" {
				e = withRepositoryLock(r, func() error { var err error; code, err = setupRepository(r, agent, stdout, stderr); return err })
			} else {
				code, e = checkRepository(r, agent, stdout)
			}
		}
	case "json":
		e = runHook(ctx, rest[0], rest[4], stdin, stdout)
		if errors.Is(e, errOutsideRepository) {
			e = nil
		}
	case "claude-worktree-create":
		e = createWorktree(ctx, stdin, stdout)
	case "claude-worktree-remove":
		e = removeWorktree(ctx, stdin, stderr)
	}
	if e != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cliName, e)
		return 1
	}
	return code
}
func setupRepository(r repoContext, agent string, stdout, stderr io.Writer) (int, error) {
	state, e := readState(r)
	if e != nil {
		return 1, e
	}
	return setupRepositoryWithState(r, agent, state, stdout, stderr)
}

func setupRepositoryWithState(r repoContext, agent string, state RepositoryState, stdout, stderr io.Writer) (int, error) {
	var e error
	if p := r.preflight(agent, state); len(p) > 0 {
		for _, s := range p {
			fmt.Fprintln(stderr, "problem: "+s)
		}
		return 1, nil
	}
	var plans []managedInstructionFile
	if r.ValidatedCodexPlan != nil {
		plans = r.ValidatedCodexPlan.managed
	} else {
		plans, e = r.planManagedFiles(agent, state)
		if e != nil {
			return 1, e
		}
	}
	var changes, problems []string
	existingBridges := map[string]bool{}
	if agent == "claude" || agent == "all" {
		for _, w := range r.Checkouts() {
			for _, rel := range []string{sharedBridge, localBridge} {
				p := filepath.Join(w, rel)
				existingBridges[p] = exists(p)
			}
		}
	}
	r.ensureIgnores(agent, &changes, &problems, state.LocalFiles...)

	if len(problems) == 0 {
		if e = r.prepareShared(&state, &changes); e != nil {
			problems = append(problems, e.Error())
		}
	}
	if len(problems) == 0 {
		if e = r.applyManagedFiles(plans, &state, &changes); e != nil {
			problems = append(problems, e.Error())
		}
	}
	var local *string
	safe := true
	if r.ValidatedCodexPlan != nil {
		local = r.ValidatedCodexPlan.local
	} else if e = r.ensureLocalSource(); e != nil {
		problems = append(problems, e.Error())
		safe = false
	} else if exists(r.localSource()) {
		text, err := readRule(r.localSource())
		if err != nil {
			problems = append(problems, err.Error())
			safe = false
		} else {
			local = &text
		}
	}
	if (agent == "all" || agent == "claude") && len(problems) == 0 {
		for _, w := range r.Checkouts() {
			if exists(filepath.Join(w, sharedRule)) {
				ensureBridge(filepath.Join(w, sharedBridge), "@AGENTS.md", &changes, &problems)
			}
		}
		if local != nil && !r.Bare() {
			if e = r.ensureLocalTarget(r.Root); e != nil {
				problems = append(problems, e.Error())
			} else {
				ensureBridge(filepath.Join(r.Root, localBridge), "@AGENTS.local.md", &changes, &problems)
			}
		}
		if safe {
			for _, w := range r.Copies() {
				changed, err := r.refreshLocal(w, local, &state)
				if err != nil {
					problems = append(problems, err.Error())
				} else if changed {
					changes = append(changes, filepath.Join(w, localBridge)+": refreshed generated copy")
				}
			}
		}
	}
	if agent == "claude" || agent == "all" {
		for _, w := range r.Checkouts() {
			for _, rel := range []string{sharedBridge, localBridge} {
				p := filepath.Join(w, rel)
				if b, err := readRegular(p); err == nil {
					marker := "@AGENTS.md"
					if rel == localBridge {
						marker = "@AGENTS.local.md"
					}
					if (string(b) == marker+"\n" || string(b) == marker) && (!existingBridges[p] || state.Generated[p] != "") {
						if err := recordGeneratedFile(&state, p, b); err != nil {
							problems = append(problems, err.Error())
						}
					}
				}
			}
		}
	}
	if len(problems) == 0 {
		if agent == "codex" || agent == "all" {
			state.NativeCodex = true
		}
		if agent == "all" {
			delete(state.Disabled, "claude")
			delete(state.Disabled, "codex")
		} else {
			delete(state.Disabled, agent)
		}
	}
	for _, s := range changes {
		fmt.Fprintln(stdout, "changed: "+s)
	}
	for _, s := range problems {
		fmt.Fprintln(stderr, "problem: "+s)
	}
	if e = writeState(r, state); e != nil {
		return 1, e
	}
	if len(problems) > 0 {
		return 1, nil
	}
	return checkRepository(r, agent, stdout)
}
func checkRepository(r repoContext, agent string, stdout io.Writer) (int, error) {
	state, e := readState(r)
	if e != nil {
		return 1, e
	}
	problems := r.topologyProblems()
	if err := r.requiredSourceProblem(state); err != nil {
		problems = append(problems, err.Error())
	}
	var warnings []string
	for _, a := range []string{"claude", "codex"} {
		if (agent == "all" || agent == a) && state.Disabled[a] {
			problems = append(problems, a+" instructions are disabled for this repository")
		}
	}
	var sharedWorktrees []string
	for _, w := range r.Checkouts() {
		p := filepath.Join(w, sharedRule)
		if exists(p) {
			sharedWorktrees = append(sharedWorktrees, w)
			if _, e := readRule(p); e != nil {
				problems = append(problems, e.Error())
			}
		}
	}
	var local *string
	if exists(r.localSource()) {
		text, e := readRule(r.localSource())
		if e != nil {
			problems = append(problems, e.Error())
		} else {
			local = &text
		}
	}
	if len(sharedWorktrees) > 0 {
		for _, w := range r.Checkouts() {
			if !contains(sharedWorktrees, w) {
				problems = append(problems, w+" has no AGENTS.md; each worktree needs the shared source in its checkout")
			}
		}
	}
	if state.SharedSource == "primary" {
		for _, w := range r.Checkouts() {
			expectation, err := r.sharedExpectation(w, state)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			actual, err := readRegular(expectation.Path)
			if w != r.Root && err == nil {
				actual, err = r.inspectSharedCopy(expectation, state)
				if err != nil {
					problems = append(problems, err.Error())
				}
			}
			if !expectation.Present || err != nil || !bytes.Equal(actual, expectation.Data) {
				problems = append(problems, w+" primary shared source copy is missing or stale; run setup")
			}
		}
	}
	if agent == "all" || agent == "claude" {
		for _, w := range r.Checkouts() {
			if !exists(filepath.Join(w, sharedRule)) {
				path := filepath.Join(w, sharedBridge)
				if bridge, err := inspectBridgeWithOwnership(path, "@AGENTS.md", state); err != nil {
					problems = append(problems, err.Error())
				} else if bridge.Normalizable {
					problems = append(problems, path+" is a dangling bridge: AGENTS.md source is missing")
				}
			}
		}
		if !r.Bare() && !exists(r.localSource()) {
			path := filepath.Join(r.Root, localBridge)
			if bridge, err := inspectBridgeWithOwnership(path, "@AGENTS.local.md", state); err != nil {
				problems = append(problems, err.Error())
			} else if bridge.Normalizable {
				problems = append(problems, path+" is a dangling bridge: AGENTS.local.md source is missing")
			}
		}
		for _, w := range sharedWorktrees {
			checkBridge(filepath.Join(w, sharedBridge), "@AGENTS.md", state, &problems)
		}
		if exists(r.localSource()) && !r.Bare() {
			checkBridge(filepath.Join(r.Root, localBridge), "@AGENTS.local.md", state, &problems)
		}
		for _, w := range r.Copies() {
			p := filepath.Join(w, localBridge)
			if !exists(p) {
				if local != nil {
					problems = append(problems, p+" generated copy is missing; run setup")
				}
				continue
			}
			b, e := r.inspectLocalCopy(w, state)
			if e != nil {
				problems = append(problems, e.Error())
				continue
			}
			if local == nil {
				problems = append(problems, p+" is a generated copy but the primary AGENTS.local.md source is missing")
			} else if string(b) != generatedLocal(*local) {
				problems = append(problems, p+" is stale; run setup")
			}
		}
		if len(sharedWorktrees) > 0 || exists(r.localSource()) {
			if notice := claudeNativeRefusal(); notice != "" {
				problems = append(problems, notice)
			}
			p, w := claudeSettingsFindings(r)
			problems = append(problems, p...)
			warnings = append(warnings, w...)
		}
	}
	problems = append(problems, r.checkManagedFiles(agent, state)...)
	for _, rel := range append(ignoreTargets(agent), state.LocalFiles...) {
		for _, w := range r.Checkouts() {
			tracked, e := r.tracked(w, rel)
			if e != nil {
				problems = append(problems, e.Error())
			} else if tracked {
				problems = append(problems, fmt.Sprintf("%s is tracked in %s; it must stay local-only", rel, w))
			} else {
				if rel == codexRule && !exists(filepath.Join(w, rel)) && !exists(r.localSource()) {
					continue
				}
				ignored, e := r.ignored(w, rel)
				if e != nil {
					problems = append(problems, e.Error())
				} else if !ignored {
					problems = append(problems, fmt.Sprintf("%s is not git-ignored in %s; run setup", rel, w))
				}
			}
		}
	}
	if agent == "all" || agent == "codex" {
		p, w := codexSettingsFindings(r)
		problems = append(problems, p...)
		warnings = append(warnings, w...)
	}

	for _, p := range problems {
		fmt.Fprintln(stdout, "FAIL: "+p)
	}
	for _, w := range warnings {
		fmt.Fprintln(stdout, "WARN: "+w)
	}
	if len(problems) > 0 {
		return 1, nil
	}
	fmt.Fprintln(stdout, "check: OK")
	return 0, nil
}
func checkBridge(p, marker string, state RepositoryState, problems *[]string) {
	s, e := inspectBridgeWithOwnership(p, marker, state)
	if e != nil {
		*problems = append(*problems, e.Error())
	} else if !s.Exists {
		*problems = append(*problems, p+" bridge is missing; run setup")
	} else if !s.Exact {
		*problems = append(*problems, p+" is not the byte-exact single-line "+marker+" bridge; run setup")
	}
}
func claudeNativeRefusal() string {
	if os.Getenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS") == "1" {
		return "Rules not delivered: CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 disables the native CLAUDE.md channel"
	}
	return ""
}
func parseHook(reader io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(io.LimitReader(reader, 1<<20))
	var h map[string]any
	if e := dec.Decode(&h); e != nil {
		return nil, fmt.Errorf("requires JSON hook input: %w", e)
	}
	if h == nil {
		return nil, fmt.Errorf("requires object hook input")
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return nil, fmt.Errorf("invalid trailing JSON hook input")
	}
	return h, nil
}
func hookString(h map[string]any, key string) (string, error) {
	v, ok := h[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("hook input missing %s", key)
	}
	return v, nil
}
func noticeText(notices []string) string {
	for i, s := range notices {
		notices[i] = "[agents-local-overlay] " + s
	}
	return strings.Join(notices, "\n")
}
func hookPreconditions(r repoContext) []string {
	var out []string
	state, err := readState(r)
	if err != nil {
		return []string{err.Error()}
	}
	if path := filepath.Join(r.Top, codexRule); exists(path) {
		body, err := readRegular(path)
		if err != nil || state.Generated[path] == "" || state.Generated[path] != digest(body) {
			out = append(out, path+" has user edits or unknown ownership; run setup")
		}
	}
	p := filepath.Join(r.Top, sharedRule)
	if !exists(p) {
		if r.Top != r.Root && exists(filepath.Join(r.Root, sharedRule)) {
			out = append(out, r.Top+" has no AGENTS.md but the primary worktree does; put the shared source in this checkout")
		}
	} else if _, e := readRule(p); e != nil {
		out = append(out, "Shared rules may be missing: "+e.Error())
	}
	if path := filepath.Join(r.Top, localRule); r.Top != r.Root && exists(path) {
		body, err := readRegular(path)
		if err != nil || state.Generated[path] == "" || state.Generated[path] != digest(body) {
			out = append(out, path+" has user edits or unknown ownership; the source lives in the primary worktree")
		}
	}
	return out
}
func runHook(ctx context.Context, event, policy string, stdin io.Reader, stdout io.Writer) error {
	if policy != "claude-session" && policy != "claude-subagent" && policy != "codex-session" && policy != "codex-subagent" {
		return fmt.Errorf("unknown policy %s", policy)
	}
	h, e := parseHook(stdin)
	if e != nil {
		return e
	}
	if policy == "claude-subagent" {
		return nil
	}
	dir, e := hookString(h, "cwd")
	if e != nil {
		return e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return e
	}
	return withRepositoryLock(r, func() error {
		state, e := readState(r)
		if e != nil {
			return e
		}
		agent := "codex"
		if strings.HasPrefix(policy, "claude") {
			agent = "claude"
		}
		if state.Disabled[agent] {
			return nil
		}
		var notices []string
		if e = r.ensureLocalSource(); e != nil {
			notices = append(notices, "Rules not delivered: "+e.Error())
		} else if agent == "codex" {
			if !exists(r.localSource()) && !exists(filepath.Join(r.Top, localRule)) && !exists(filepath.Join(r.Top, codexRule)) {
				return nil
			}
			notices = append(notices, r.checkManagedFiles("codex", state)...)
		} else {
			opted := exists(r.localSource()) || exists(filepath.Join(r.Top, localRule)) || exists(filepath.Join(r.Top, localBridge))
			if !opted {
				return nil
			}
			if refusal := claudeNativeRefusal(); refusal != "" {
				notices = append(notices, refusal)
			}
			notices = append(notices, r.topologyProblems()...)
			notices = append(notices, hookPreconditions(r)...)
			if exists(filepath.Join(r.Top, sharedRule)) {
				checkBridge(filepath.Join(r.Top, sharedBridge), "@AGENTS.md", state, &notices)
			}
			var local *string
			safe := true
			if exists(r.localSource()) {
				text, e := readRule(r.localSource())
				if e != nil {
					notices = append(notices, e.Error())
					safe = false
				} else {
					local = &text
				}
				if r.Top == r.Root && !r.Bare() {
					checkBridge(filepath.Join(r.Root, localBridge), "@AGENTS.local.md", state, &notices)
				}
			}
			if safe && contains(r.Copies(), r.Top) {
				changed, e := r.refreshLocal(r.Top, local, &state)
				if e != nil {
					notices = append(notices, "Rules not delivered: "+e.Error())
				} else if changed {
					if e = writeState(r, state); e != nil {
						return e
					}
				}
			}
		}
		if len(notices) == 0 {
			return nil
		}
		text := noticeText(notices)
		if agent == "claude" {
			_, e = fmt.Fprintln(stdout, text)
			return e
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}})
	})
}
