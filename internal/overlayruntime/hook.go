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
		err = createWorktree(ctx, stdin, stdout, stderr)
	case "WorktreeRemove":
		err = removeWorktree(ctx, stdin, stderr)
	default:
		err = sessionStart(ctx, agent, event, stdin, stdout, options)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cliName, err)
		return 1
	}
	return 0
}

func preparedPathReady(result PrepareResult, path string) bool {
	for _, skipped := range result.Skipped {
		if skipped.Path == path {
			return false
		}
	}
	return exists(path)
}

// firstSessionBody returns what a new session must still receive after this
// call successfully prepared the native files that future sessions will read.
func firstSessionBody(agent string, result PrepareResult) string {
	switch agent {
	case "claude":
		bridge := filepath.Join(result.Checkout, localBridge)
		localCopy := filepath.Join(result.Checkout, localRule)
		if !preparedPathReady(result, bridge) {
			return ""
		}
		if result.Checkout != result.Primary && !preparedPathReady(result, localCopy) {
			return ""
		}
		if contains(result.Created, bridge) || result.Checkout != result.Primary && result.Changed(localCopy) {
			return result.LocalBody
		}
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
	result, e := PrepareCheckout(ctx, dir)
	if errors.Is(e, ErrOutsideRepository) {
		return nil
	}
	if e != nil {
		return e
	}
	var parts []string
	var notices []string
	nativeBlocked := false
	if agent == "claude" && (result.LocalPresent || exists(filepath.Join(result.Checkout, sharedRule))) {
		if notice := claudeNativeRefusal(); notice != "" {
			notices = append(notices, notice)
			nativeBlocked = true
		}
	}
	if r, e := resolveContext(ctx, dir); e == nil {
		if problem := sharedRuleProblem(r.Top); problem != "" {
			notices = append(notices, problem)
			nativeBlocked = true
		}
		switch agent {
		case "claude":
			problems := claudeSettingsFindings(r)
			notices = append(notices, problems...)
			nativeBlocked = nativeBlocked || len(problems) > 0
		case "codex":
			problems, _ := codexSettingsFindings(r, options.CodexProjectDocMaxBytes)
			problems = append(problems, options.CodexNativeIssues...)
			notices = append(notices, problems...)
			nativeBlocked = nativeBlocked || len(problems) > 0
		}
	}
	if source == "startup" && !nativeBlocked {
		if body := firstSessionBody(agent, result); body != "" {
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
