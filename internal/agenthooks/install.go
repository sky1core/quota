package agenthooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const hookStatusMessage = "Checking agent command policy"

type HookPlan struct {
	Runtime   string   `json:"runtime"`
	Path      string   `json:"path"`
	Command   string   `json:"command"`
	Binary    string   `json:"binary,omitempty"`
	PolicyDir string   `json:"policyDir,omitempty"`
	Present   bool     `json:"present"`
	Reasons   []string `json:"reasons,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func HookCommand(runtime, binary, policyDir string) string {
	binary = hookBinary(binary)
	args := []string{binary, "agent", "hooks", "eval", "--runtime=" + runtime}
	if strings.TrimSpace(policyDir) != "" {
		args = append(args, "--policy-dir", policyDir)
	}
	return ShellQuote(args)
}

func hookBinary(binary string) string {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return "quota-cli"
	}
	return absolutizeHookBinary(binary)
}

// absolutizeHookBinary resolves a binary given as a path (containing a path
// separator) to an absolute path so the stored hook command works from any cwd.
// A bare command name is left untouched for PATH resolution.
func absolutizeHookBinary(binary string) string {
	if !strings.ContainsRune(binary, os.PathSeparator) {
		return binary
	}
	if abs, err := filepath.Abs(binary); err == nil {
		return abs
	}
	return binary
}

func CheckHookBinary(binary string) error {
	binary = hookBinary(binary)
	path, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("hook binary %q is not executable: %w", binary, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("hook binary %q cannot be inspected: %w", binary, err)
	}
	if info.IsDir() {
		return fmt.Errorf("hook binary %q is a directory", binary)
	}
	return nil
}

func ClaudeSettingsPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func CodexHooksPath() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "hooks.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "hooks.json")
}

func Plans(binary, policyDir string) []HookPlan {
	return []HookPlan{
		{Runtime: "claude", Path: ClaudeSettingsPath(), Command: HookCommand("claude", binary, policyDir), Binary: hookBinary(binary), PolicyDir: policyDir},
		{Runtime: "codex", Path: CodexHooksPath(), Command: HookCommand("codex", binary, policyDir), Binary: hookBinary(binary), PolicyDir: policyDir},
	}
}

func Apply(runtime, binary, policyDir string) (HookPlan, error) {
	switch runtime {
	case "claude":
		return applyClaude(binary, policyDir)
	case "codex":
		return applyCodex(binary, policyDir)
	default:
		return HookPlan{}, fmt.Errorf("unsupported runtime %q", runtime)
	}
}

func applyClaude(binary, policyDir string) (HookPlan, error) {
	return applyHook("claude", ClaudeSettingsPath(), binary, policyDir)
}

func applyCodex(binary, policyDir string) (HookPlan, error) {
	return applyHook("codex", CodexHooksPath(), binary, policyDir)
}

func applyHook(runtime, path, binary, policyDir string) (HookPlan, error) {
	plan := HookPlan{Runtime: runtime, Path: path, Command: HookCommand(runtime, binary, policyDir), Binary: hookBinary(binary), PolicyDir: policyDir}
	root, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error {
		if v, exists := root["hooks"]; exists {
			if _, ok := v.(map[string]any); !ok {
				return fmt.Errorf("hooks must be an object")
			}
		}
		if runtime == "codex" {
			if _, exists := root["description"]; !exists {
				root["description"] = "quota agent hook policy"
			}
		}
		hooks := objectAt(root, "hooks")
		if v, exists := hooks["PreToolUse"]; exists {
			if _, ok := v.([]any); !ok {
				return fmt.Errorf("hooks.PreToolUse must be an array")
			}
		}
		pre := appendWithoutManagedHook(hookGroups(hooks["PreToolUse"]), plan.Command)
		entry := map[string]any{"type": "command", "command": plan.Command, "statusMessage": hookStatusMessage}
		if runtime == "codex" {
			entry["timeout"] = 30
		}
		hooks["PreToolUse"] = append(pre, map[string]any{"matcher": "Bash", "hooks": []any{entry}})
		return nil
	})
	if err != nil {
		return plan, err
	}
	inspectHookPlan(&plan, root, runtime, binary, policyDir)
	if plan.Error != "" {
		return plan, fmt.Errorf("saved %s hook at %s, but its configuration could not be read: %s", runtime, plan.Path, plan.Error)
	}
	if !plan.Present {
		return plan, fmt.Errorf("saved hook did not pass installation inspection")
	}
	if len(plan.Reasons) > 0 {
		return plan, fmt.Errorf("saved %s hook at %s, but diagnostics found blockers: %s", runtime, plan.Path, strings.Join(plan.Reasons, "; "))
	}
	return plan, nil
}

func Detect(runtime, binary, policyDir string) HookPlan {
	var plan HookPlan
	switch runtime {
	case "claude":
		plan = HookPlan{Runtime: runtime, Path: ClaudeSettingsPath(), Command: HookCommand(runtime, binary, policyDir), Binary: hookBinary(binary), PolicyDir: policyDir}
	case "codex":
		plan = HookPlan{Runtime: runtime, Path: CodexHooksPath(), Command: HookCommand(runtime, binary, policyDir), Binary: hookBinary(binary), PolicyDir: policyDir}
	default:
		return HookPlan{Runtime: runtime}
	}
	root, err := ReadJSONObject(plan.Path)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	inspectHookPlan(&plan, root, runtime, binary, policyDir)
	return plan
}

func inspectHookPlan(plan *HookPlan, root map[string]any, runtime, binary, policyDir string) {
	if hookMaps, foundBinary, ok := findManagedHook(root, runtime, binary, policyDir); ok {
		plan.Present = true
		plan.Binary = foundBinary
		for _, hookMap := range hookMaps {
			plan.Reasons = append(plan.Reasons, unsupportedHookVariants(hookMap)...)
		}
	}
	if runtime == "claude" {
		plan.Reasons = append(plan.Reasons, claudeDisableReasons(root)...)
	}
	if runtime == "codex" {
		config, err := readCodexConfig(codexConfigPath())
		if err != nil {
			plan.Error = err.Error()
			return
		}
		plan.Reasons = append(plan.Reasons, codexConfigReasons(config)...)
	}
}

// writeUniqueBackup writes content to a fresh path.bak.<nanosecond-timestamp>,
// creating the file with O_EXCL so a name collision (coarse clocks, repeated or
// concurrent writes) can never overwrite an earlier backup; on collision it
// retries with an increasing -N suffix.
func writeUniqueBackup(path string, content []byte) error {
	base := fmt.Sprintf("%s.bak.%s", path, time.Now().Format("20060102-150405.000000000"))
	candidate := base
	for i := 1; ; i++ {
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := f.Write(content); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		}
		if !os.IsExist(err) {
			return err
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func objectAt(root map[string]any, key string) map[string]any {
	if v, ok := root[key].(map[string]any); ok {
		return v
	}
	obj := map[string]any{}
	root[key] = obj
	return obj
}

func hookGroups(v any) []any {
	if groups, ok := v.([]any); ok {
		return groups
	}
	return []any{}
}

func appendWithoutManagedHook(groups []any, command string) []any {
	var out []any
	for _, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			if hookEntryIsManaged(group, command) {
				continue
			}
			out = append(out, group)
			continue
		}
		hooks, ok := groupMap["hooks"].([]any)
		if !ok {
			out = append(out, group)
			continue
		}
		filtered := make([]any, 0, len(hooks))
		for _, hook := range hooks {
			if hookEntryIsManaged(hook, command) {
				continue
			}
			filtered = append(filtered, hook)
		}
		if len(filtered) == 0 {
			continue
		}
		next := make(map[string]any, len(groupMap))
		for key, value := range groupMap {
			next[key] = value
		}
		next["hooks"] = filtered
		out = append(out, next)
	}
	return out
}

func hookEntryIsManaged(entry any, command string) bool {
	switch h := entry.(type) {
	case string:
		return h == command || isReplacedEvaluatorCommand(h, command)
	case map[string]any:
		cmd, ok := h["command"].(string)
		if !ok {
			return false
		}
		return cmd == command || isReplacedEvaluatorCommand(cmd, command)
	default:
		return false
	}
}

func isReplacedEvaluatorCommand(command, replacement string) bool {
	target, ok := parseDirectShellInvocation(replacement)
	if !ok || len(target.Argv) < 5 {
		return false
	}
	invocations, err := ParseShellInvocations(command)
	if err != nil {
		return false
	}
	for _, inv := range invocations {
		argv := inv.Argv
		if len(argv) < 5 {
			continue
		}
		if argv[1] != "agent" || argv[2] != "hooks" || argv[3] != "eval" {
			continue
		}
		if !managedHookBinaryMatches(absolutizeHookBinary(argv[0]), target.Argv[0]) && !managedHookBinaryMatches(argv[0], "") {
			continue
		}
		if managedHookStrictOptionsMatch(argv[4:], "claude", "", false) || managedHookStrictOptionsMatch(argv[4:], "codex", "", false) {
			return true
		}
	}
	return false
}

func containsManagedHook(v any, runtime, binary, policyDir string) bool {
	_, _, ok := findManagedHook(v, runtime, binary, policyDir)
	return ok
}

func findManagedHook(v any, runtime, binary, policyDir string) ([]map[string]any, string, bool) {
	root, ok := v.(map[string]any)
	if !ok {
		return nil, "", false
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return nil, "", false
	}
	return findManagedPreToolUseBashHook(hooks["PreToolUse"], runtime, binary, policyDir)
}

func findManagedPreToolUseBashHook(v any, runtime, binary, policyDir string) ([]map[string]any, string, bool) {
	var entries []map[string]any
	var firstBinary string
	for _, group := range hookGroups(v) {
		groupMap, ok := group.(map[string]any)
		if !ok || groupMap["matcher"] != "Bash" {
			continue
		}
		hooks, ok := groupMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, hook := range hooks {
			hookMap, ok := hook.(map[string]any)
			if !ok || hookMap["type"] != "command" {
				continue
			}
			command, ok := hookMap["command"].(string)
			if !ok {
				continue
			}
			if foundBinary, ok := managedHookCommandBinaryStrict(command, runtime, binary, policyDir, true); ok {
				entries = append(entries, hookMap)
				if firstBinary == "" {
					firstBinary = foundBinary
				}
			}
		}
	}
	return entries, firstBinary, len(entries) > 0
}

func containsCommandString(v any, command string) bool {
	switch x := v.(type) {
	case string:
		return x == command
	case []any:
		for _, item := range x {
			if containsCommandString(item, command) {
				return true
			}
		}
	case map[string]any:
		for _, item := range x {
			if containsCommandString(item, command) {
				return true
			}
		}
	}
	return false
}

func isManagedHookCommandStrict(command, runtime, binary, policyDir string, exactPolicyDir bool) bool {
	_, ok := managedHookCommandBinaryStrict(command, runtime, binary, policyDir, exactPolicyDir)
	return ok
}

func managedHookCommandBinaryStrict(command, runtime, binary, policyDir string, exactPolicyDir bool) (string, bool) {
	inv, ok := parseDirectShellInvocation(command)
	if !ok {
		return "", false
	}
	argv := inv.Argv
	if len(argv) < 4 {
		return "", false
	}
	if !managedHookBinaryMatches(argv[0], binary) || argv[1] != "agent" || argv[2] != "hooks" || argv[3] != "eval" {
		return "", false
	}
	if !managedHookStrictOptionsMatch(argv[4:], runtime, policyDir, exactPolicyDir) {
		return "", false
	}
	return argv[0], true
}

func managedHookStrictOptionsMatch(args []string, runtime, policyDir string, exactPolicyDir bool) bool {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--runtime" || arg == "--policy-dir":
			if i+1 >= len(args) {
				return false
			}
			if _, ok := values[arg]; ok {
				return false
			}
			values[arg] = args[i+1]
			i++
		case strings.HasPrefix(arg, "--runtime="):
			if _, ok := values["--runtime"]; ok {
				return false
			}
			values["--runtime"] = strings.TrimPrefix(arg, "--runtime=")
		case strings.HasPrefix(arg, "--policy-dir="):
			if _, ok := values["--policy-dir"]; ok {
				return false
			}
			values["--policy-dir"] = strings.TrimPrefix(arg, "--policy-dir=")
		default:
			return false
		}
	}
	if runtime != "" && values["--runtime"] != runtime {
		return false
	}
	if !exactPolicyDir {
		return true
	}
	if strings.TrimSpace(policyDir) == "" {
		_, ok := values["--policy-dir"]
		return !ok
	}
	return values["--policy-dir"] == policyDir
}

func managedHookBinaryMatches(arg, binary string) bool {
	if strings.TrimSpace(binary) != "" {
		return arg == absolutizeHookBinary(strings.TrimSpace(binary))
	}
	return commandName(arg) == "quota-cli"
}
