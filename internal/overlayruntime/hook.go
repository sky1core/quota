package overlayruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const noticePrefix = "[quota instructions] "

type PrepareHookOptions struct {
	ClaudeConfigDir         string
	CodexHome               string
	CodexProjectDocMaxBytes *int64
	CodexNativeIssues       []string
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

func ValidPrepareEvent(agent, event string) bool {
	switch agent {
	case "claude":
		return event == "SessionStart" || event == "WorktreeCreate" || event == "WorktreeRemove"
	case "codex":
		return event == "SessionStart"
	}
	return false
}

// RunPrepareHook is the fixed `_prepare` entry point. Notices never block the
// session: they are delivered as context and the exit code stays 0.
func RunPrepareHook(ctx context.Context, agent, event string, stdin io.Reader, stdout, stderr io.Writer) int {
	return RunPrepareHookWithOptions(ctx, agent, event, stdin, stdout, stderr, PrepareHookOptions{})
}

func RunPrepareHookWithOptions(ctx context.Context, agent, event string, stdin io.Reader, stdout, stderr io.Writer, options PrepareHookOptions) int {
	if !ValidPrepareEvent(agent, event) {
		fmt.Fprintln(stderr, "unsupported instruction agent/event combination")
		return 2
	}
	var err error
	switch event {
	case "WorktreeCreate":
		err = createWorktree(ctx, stdin, stdout, stderr, options)
	case "WorktreeRemove":
		err = removeWorktree(ctx, stdin, stderr, options)
	default:
		err = sessionStart(ctx, agent, event, stdin, stdout, options)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cliName, err)
		return 1
	}
	return 0
}

// firstSessionBody returns what a new session must still receive after this
// call prepared native files too late for that session's first native scan.
func firstSessionBody(agent string, result PrepareResult, claudeExcludePatterns []string) string {
	switch agent {
	case "claude":
		return claudeFirstSessionBody(result, claudeExcludePatterns)
	case "codex":
		override := filepath.Join(result.Checkout, codexRule)
		if contains(result.Created, override) {
			return result.LocalBody
		}
		if contains(result.Updated, override) {
			return result.OverrideBody
		}
		if contains(result.Removed, override) {
			shared := filepath.Join(result.Checkout, sharedRule)
			if exists(shared) {
				if data, err := readRegular(shared); err == nil {
					if text, err := decodeRule(data, shared); err == nil {
						return text
					}
				}
			}
		}
	}
	return ""
}

func claudeFirstSessionBody(result PrepareResult, excludePatterns []string) string {
	beforeShared := claudePathLoaded(result.ClaudeSharedBefore, excludePatterns)
	beforeLocal := claudePathLoaded(result.ClaudeLocalBefore, excludePatterns)
	afterShared := claudePathLoaded(result.ClaudeSharedAfter, excludePatterns)
	afterLocal := claudePathLoaded(result.ClaudeLocalAfter, excludePatterns)
	if containsSkippedPath(result.Skipped, filepath.Join(result.Checkout, localRule)) {
		afterLocal = false
	}
	var parts []string
	if afterShared && !beforeShared && result.SharedBody != "" {
		parts = append(parts, result.SharedBody)
	}
	if afterLocal && result.LocalBody != "" && (!beforeLocal || result.Changed(filepath.Join(result.Checkout, localRule))) {
		parts = append(parts, result.LocalBody)
	}
	return strings.Join(parts, "\n\n")
}

func claudePathLoaded(paths []string, excludePatterns []string) bool {
	for _, path := range paths {
		if len(claudeExclusionFindings(excludePatterns, path)) == 0 {
			return true
		}
	}
	return false
}

func containsSkippedPath(skips []PrepareSkip, path string) bool {
	for _, skip := range skips {
		if skip.Path == path {
			return true
		}
	}
	return false
}

func sessionStart(ctx context.Context, agent, event string, stdin io.Reader, stdout io.Writer, options PrepareHookOptions) error {
	h, e := parseHook(stdin)
	if e != nil {
		return e
	}
	dir, e := hookString(h, "cwd")
	if e != nil {
		return e
	}
	source, _ := h["source"].(string)
	result, e := PrepareCheckoutWithOptions(ctx, dir, PrepareOptions{ClaudeConfigDir: options.ClaudeConfigDir, CodexHome: options.CodexHome})
	if errors.Is(e, ErrOutsideRepository) {
		return nil
	}
	if e != nil {
		return e
	}
	var parts []string
	var notices []string
	nativeBlocked := false
	var claudeExcludePatterns []string
	if agent == "claude" && (result.LocalPresent || exists(filepath.Join(result.Checkout, sharedRule))) {
		if notice := claudeNativeRefusal(); notice != "" {
			notices = append(notices, notice)
			nativeBlocked = true
		}
	}
	paths := NativeAccountPaths{
		ClaudeConfigDir: options.ClaudeConfigDir,
		CodexHome:       options.CodexHome,
		NeedClaude:      agent == "claude",
		NeedCodex:       agent == "codex",
	}
	if r, e := resolveContextWithNativePaths(ctx, dir, paths); e == nil {
		if problem := sharedRuleProblem(r.Top); problem != "" {
			notices = append(notices, problem)
			nativeBlocked = true
		}
		switch agent {
		case "claude":
			problems := claudeSettingsFindings(r)
			notices = append(notices, problems...)
			nativeBlocked = nativeBlocked || len(problems) > 0
			claudeExcludePatterns = claudeEffectiveExclusionPatterns(r)
		case "codex":
			problems, _ := codexSettingsFindings(r, options.CodexProjectDocMaxBytes)
			problems = append(problems, options.CodexNativeIssues...)
			notices = append(notices, problems...)
			nativeBlocked = nativeBlocked || len(problems) > 0
		}
	}
	if source == "startup" && !nativeBlocked {
		if body := firstSessionBody(agent, result, claudeExcludePatterns); body != "" {
			parts = append(parts, body)
		}
	}
	notices = uniqueStrings(notices...)
	notices = append(notices, result.SkipReasons()...)
	for i, n := range notices {
		notices[i] = noticePrefix + n
	}
	if len(notices) > 0 {
		parts = append(parts, strings.Join(notices, "\n"))
	}
	if len(parts) == 0 {
		return nil
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": strings.Join(parts, "\n\n")}})
}
