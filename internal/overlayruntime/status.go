package overlayruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
)

type RepositoryStatus struct {
	Checkout  string   `json:"checkout"`
	Primary   string   `json:"primary"`
	StatePath string   `json:"statePath"`
	Generated []string `json:"generated,omitempty"`
	Problems  []string `json:"problems,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type CheckOptions struct {
	ClaudeConfigDir string
	CodexHome       string
	// CodexProjectDocMaxBytes is the effective limit reported by the native
	// Codex inspection; nil when that inspection was unavailable.
	CodexProjectDocMaxBytes *int64
}

// CheckRepository inspects sources, generated files, ignore state, and the
// effective native settings of the checkout containing dir without writing.
func CheckRepository(ctx context.Context, dir, agent string, options CheckOptions) (RepositoryStatus, error) {
	var status RepositoryStatus
	if agent != "all" && agent != "claude" && agent != "codex" {
		return status, fmt.Errorf("invalid agent %q", agent)
	}
	if e := ValidateGitEnvironment(ctx); e != nil {
		return status, e
	}
	paths := NativeAccountPaths{
		ClaudeConfigDir: options.ClaudeConfigDir,
		CodexHome:       options.CodexHome,
		NeedClaude:      agent == "all" || agent == "claude",
		NeedCodex:       agent == "all" || agent == "codex",
	}
	r, e := resolveContextWithNativePaths(ctx, dir, paths)
	if e != nil {
		return status, e
	}
	status.Checkout, status.Primary = r.Top, r.Root
	status.StatePath, e = statePath(r)
	if e != nil {
		return status, e
	}
	problems := r.topologyProblems()
	state, e := readState(r)
	if e != nil {
		problems = append(problems, e.Error())
	}
	actions, local, _, e := r.evaluateWithSource(r.Top, state)
	if e != nil {
		problems = append(problems, e.Error())
	}
	actions = r.statusActions(r.Top, state, actions)
	if problem := sharedRuleProblem(r.Top); problem != "" {
		problems = append(problems, problem)
	}
	for _, a := range actions {
		rel, _ := filepath.Rel(r.Top, a.Path)
		if !generatedVisibleToAgent(rel, agent) {
			continue
		}
		switch a.Action {
		case actionSkip:
			problems = append(problems, a.Path+": "+a.Reason)
		case actionCreate:
			problems = append(problems, a.Path+" is not prepared yet; the next session start creates it")
		case actionUpdate:
			problems = append(problems, a.Path+" is stale; the next session start refreshes it")
		case actionRemove:
			problems = append(problems, a.Path+" is a stale generated file; the next session start removes it")
		}
	}
	status.Generated = existingGeneratedFiles(r.Top, agent, state, actions)
	if agent == "all" || agent == "claude" {
		if local != nil || exists(filepath.Join(r.Top, sharedRule)) {
			if notice := claudeNativeRefusal(); notice != "" {
				problems = append(problems, notice)
			}
			problems = append(problems, claudeSettingsFindings(r)...)
		}
	}
	if agent == "all" || agent == "codex" {
		p, w := codexSettingsFindings(r, options.CodexProjectDocMaxBytes)
		problems = append(problems, p...)
		status.Warnings = append(status.Warnings, w...)
	}
	sort.Strings(status.Generated)
	status.Problems = uniqueStrings(problems...)
	return status, nil
}

func generatedVisibleToAgent(rel, agent string) bool {
	switch agent {
	case "claude":
		return rel != codexRule
	case "codex":
		return rel != claudeAgentsRule && rel != claudeBridgeRule && rel != "CLAUDE.md" && rel != "CLAUDE.local.md"
	default:
		return true
	}
}

func existingGeneratedFiles(checkout, agent string, state RepositoryState, actions []plannedAction) []string {
	problems := map[string]bool{}
	for _, a := range actions {
		if a.Action != actionNone {
			problems[a.Path] = true
		}
	}
	var paths []string
	for path := range state.Generated {
		if problems[path] {
			continue
		}
		if !within(path, checkout) {
			continue
		}
		rel, err := filepath.Rel(checkout, path)
		if err != nil || !generatedVisibleToAgent(rel, agent) {
			continue
		}
		current, owned, problem := inspectGenerated(path, state)
		if current != nil && owned && problem == nil {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}
