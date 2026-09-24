package overlayruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func parseHook(reader io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(reader)
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

func deliversFor(source string) bool {
	return source == "startup" || source == "clear" || source == "compact"
}

func RunSessionStartHook(ctx context.Context, agent string, stdin io.Reader, stdout, stderr io.Writer) int {
	if agent != "claude" && agent != "codex" {
		fmt.Fprintln(stderr, "unsupported instruction agent")
		return 2
	}
	if err := sessionStart(ctx, agent, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cliName, err)
		return 1
	}
	return 0
}

func RunClaudePromptHook(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := checkClaudePrompt(ctx, stdin); err != nil {
		reason := fmt.Sprintf("%s: instruction check failed. Required local instructions cannot be treated as fully loaded. Do not perform the user's requested task or call tools. In your next response, explain this instruction-loading problem and its cause to the user. Ask the user to resolve the instruction or inspection error and start a new session before work continues. Do not suggest splitting, truncating, or bypassing the hook limit. Leave instruction files unchanged. Do not claim the instructions loaded successfully.\nCause: %v", cliName, err)
		if err := json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": reason}}); err != nil {
			fmt.Fprintf(stderr, "%s\nCannot write hook response: %v\n", reason, err)
			return 2
		}
	}
	return 0
}

func checkClaudePrompt(ctx context.Context, stdin io.Reader) error {
	h, err := parseHook(stdin)
	if err != nil {
		return err
	}
	dir, err := hookString(h, "cwd")
	if err != nil {
		return err
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	r, err := resolveContext(ctx, dir)
	if errors.Is(err, ErrOutsideRepository) {
		return nil
	}
	if err != nil {
		return err
	}
	body, notice, _, err := readLocalInstructions(r.localSource())
	if err != nil {
		return err
	}
	if notice != "" {
		return errors.New(notice)
	}
	if exceedsClaudeHookLimit(sessionStartContext(body, nil)) {
		return fmt.Errorf("%s and its header exceed Claude's %d UTF-16-unit hook limit", r.localSource(), claudeHookContextLimit)
	}
	return nil
}

func sessionStart(ctx context.Context, agent string, stdin io.Reader, stdout io.Writer) error {
	h, err := parseHook(stdin)
	if err != nil {
		return err
	}
	dir, err := hookString(h, "cwd")
	if err != nil {
		return err
	}
	source, _ := h["source"].(string)
	if !deliversFor(source) {
		return nil
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	r, err := resolveContext(ctx, dir)
	if errors.Is(err, ErrOutsideRepository) {
		return nil
	}
	if err != nil {
		return err
	}
	notices, err := r.cleanupLegacy()
	if err != nil {
		return err
	}
	body, notice, _, err := readLocalInstructions(r.localSource())
	if err != nil {
		return err
	}
	if notice != "" {
		notices = append([]string{notice}, notices...)
	}
	out := sessionStartContext(body, notices)
	if agent == "claude" && exceedsClaudeHookLimit(out) {
		notice := fmt.Sprintf("Instruction hook context was not delivered in full because Claude's instruction hook output exceeds %d UTF-16 units. Tell the user about this failure; do not claim the full instruction context was loaded successfully.", claudeHookContextLimit)
		out = sessionStartContext("", append([]string{notice}, notices...))
	}
	if out == "" {
		return nil
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": out}})
}

func sessionStartContext(body string, notices []string) string {
	var sections []string
	if len(notices) > 0 {
		lines := make([]string, len(notices))
		for i, notice := range notices {
			lines[i] = noticePrefix + notice
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if body != "" {
		sections = append(sections, "AGENTS.local.md\n"+body)
	}
	if len(sections) == 0 {
		return ""
	}
	out := sections[0]
	for _, section := range sections[1:] {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n" + section
	}
	return out
}
