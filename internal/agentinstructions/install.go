package agentinstructions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/config"
)

const instructionEvent = "SessionStart"

type InstallTargets struct {
	ClaudeAccount   string `json:"claude_account,omitempty"`
	ClaudeConfigDir string `json:"claude_config_dir,omitempty"`
	ClaudeSettings  string `json:"claude_settings"`
	CodexAccount    string `json:"codex_account,omitempty"`
	CodexHome       string `json:"codex_home,omitempty"`
	CodexHooks      string `json:"codex_hooks"`
	CodexConfig     string `json:"codex_config"`
}
type InstallChange struct {
	Account   string `json:"account,omitempty"`
	Agent     string `json:"agent"`
	Path      string `json:"path"`
	Changed   bool   `json:"changed"`
	Operation string `json:"operation"`
}
type InstallPlan struct {
	Changes   []InstallChange `json:"changes"`
	Agents    []string        `json:"agents"`
	Uninstall bool            `json:"uninstall"`
}
type InstallResult struct {
	Applied    []string `json:"applied"`
	Unchanged  []string `json:"unchanged"`
	FailedPath string   `json:"failed_path,omitempty"`
}
type InstallStatus struct {
	Account    string   `json:"account,omitempty"`
	Agent      string   `json:"agent"`
	Paths      []string `json:"paths"`
	Configured bool     `json:"configured"`
	Problems   []string `json:"problems,omitempty"`
}
type Installation struct {
	executable string
	targets    InstallTargets
}

func CurrentInstallTargets() (InstallTargets, error) {
	claude, err := config.DefaultAccountDirectory("claude")
	if err != nil {
		return InstallTargets{}, err
	}
	codex, err := config.DefaultAccountDirectory("codex")
	if err != nil {
		return InstallTargets{}, err
	}
	return InstallTargetsForAccounts("claude", claude, "codex", codex)
}

func InstallTargetsForAccounts(claudeAccount, claudeConfigDir, codexAccount, codexHome string) (InstallTargets, error) {
	var targets InstallTargets
	if strings.TrimSpace(claudeConfigDir) != "" {
		dir, err := config.CanonicalAccountDirectory(claudeConfigDir)
		if err != nil {
			return targets, err
		}
		targets.ClaudeAccount = claudeAccount
		targets.ClaudeConfigDir = dir
		targets.ClaudeSettings = filepath.Join(dir, "settings.json")
	}
	if strings.TrimSpace(codexHome) != "" {
		dir, err := config.CanonicalAccountDirectory(codexHome)
		if err != nil {
			return targets, err
		}
		targets.CodexAccount = codexAccount
		targets.CodexHome = dir
		targets.CodexHooks = filepath.Join(dir, "hooks.json")
		targets.CodexConfig = filepath.Join(dir, "config.toml")
	}
	return targets, nil
}
func NewInstallation(executable string, targets InstallTargets) (*Installation, error) {
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return nil, fmt.Errorf("executable must be an absolute path")
	}
	return &Installation{executable, targets}, nil
}
func (i *Installation) resolveSelectedTargets(agents []string) (*Installation, error) {
	resolved := *i
	paths := []*string{}
	for _, agent := range agents {
		switch agent {
		case "claude":
			paths = append(paths, &resolved.targets.ClaudeSettings)
		case "codex":
			paths = append(paths, &resolved.targets.CodexHooks, &resolved.targets.CodexConfig)
		default:
			return nil, fmt.Errorf("unsupported agent %q", agent)
		}
	}
	seen := map[string]bool{}
	for _, target := range paths {
		path := *target
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return nil, fmt.Errorf("account target must be an absolute path: %q", path)
		}
		canonical, err := canonicalInstallPath(path)
		if err != nil {
			return nil, err
		}
		if seen[canonical] {
			return nil, fmt.Errorf("selected account targets must be distinct")
		}
		seen[canonical] = true
		*target = canonical
	}
	return &resolved, nil
}
func installAgents(agents []string) ([]string, error) {
	if len(agents) == 0 {
		return nil, fmt.Errorf("at least one agent is required")
	}
	seen := map[string]bool{}
	for _, a := range agents {
		if a != "claude" && a != "codex" {
			return nil, fmt.Errorf("unsupported agent %q", a)
		}
		seen[a] = true
	}
	out := []string{}
	for _, a := range []string{"claude", "codex"} {
		if seen[a] {
			out = append(out, a)
		}
	}
	return out, nil
}
func (i *Installation) command(agent, event string) string {
	args := []string{i.executable, "agent", "instructions", "_prepare", "--agent=" + agent, "--event=" + event}
	return agenthooks.ShellQuote(args)
}
func instructionEvents(agent string) []string {
	if agent == "claude" {
		return []string{instructionEvent, "UserPromptSubmit"}
	}
	return []string{instructionEvent}
}
func (i *Installation) expectedHook(agent, event string) map[string]any {
	hook := map[string]any{"type": "command", "command": i.command(agent, event)}
	if agent == "codex" {
		hook["additionalContextLimit"] = 0
	}
	return hook
}
func (i *Installation) owns(command, agent, event string) bool {
	return agenthooks.OwnsInstructionCommand(command, i.executable, agent, event)
}
func suspiciousInstructionCommand(command string, knownExecutables ...string) bool {
	return agenthooks.SuspiciousInstructionCommand(command, knownExecutables...)
}
func canonicalInstallPath(path string) (string, error) {
	missing := []string{}
	p := filepath.Clean(path)
	for {
		_, err := os.Lstat(p)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return "", err
			}
			for n := len(missing) - 1; n >= 0; n-- {
				resolved = filepath.Join(resolved, missing[n])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		missing = append(missing, filepath.Base(p))
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		p = parent
	}
}
func installEqual(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}
func installArray(v any) ([]any, bool) {
	switch a := v.(type) {
	case []any:
		return a, true
	case []map[string]any:
		r := make([]any, len(a))
		for n := range a {
			r[n] = a[n]
		}
		return r, true
	default:
		return nil, false
	}
}
func (i *Installation) transformHooks(root map[string]any, agent string, uninstall bool) error {
	hooks := map[string]any{}
	if raw, ok := root["hooks"]; ok {
		var valid bool
		hooks, valid = raw.(map[string]any)
		if !valid {
			return fmt.Errorf("hooks must be an object")
		}
	}
	for event, raw := range hooks {
		if agent == "codex" && event == "state" {
			if err := agenthooks.ValidateCodexHookState(raw); err != nil {
				return err
			}
			continue
		}
		groups, ok := installArray(raw)
		if !ok {
			return fmt.Errorf("hooks.%s must be an array", event)
		}
		filtered := []any{}
		removed := false
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return fmt.Errorf("hooks.%s group must be an object", event)
			}
			entries, ok := installArray(group["hooks"])
			if !ok {
				return fmt.Errorf("hooks.%s group hooks must be an array", event)
			}
			keep := []any{}
			groupRemoved := false
			for _, entry := range entries {
				hook, ok := entry.(map[string]any)
				if !ok {
					return fmt.Errorf("hooks.%s hook must be an object", event)
				}
				command, _ := hook["command"].(string)
				if i.owns(command, agent, event) {
					removed = true
					groupRemoved = true
					continue
				}
				if suspiciousInstructionCommand(command, i.executable) {
					return fmt.Errorf("hooks.%s has an instruction command with unknown ownership: %q", event, command)
				}
				keep = append(keep, entry)
			}
			if !groupRemoved {
				filtered = append(filtered, rawGroup)
			} else if len(keep) > 0 {
				next := map[string]any{}
				for k, v := range group {
					next[k] = v
				}
				next["hooks"] = keep
				filtered = append(filtered, next)
			}
		}
		if removed {
			if len(filtered) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = filtered
			}
		}
	}
	if !uninstall {
		for _, event := range instructionEvents(agent) {
			groups, _ := installArray(hooks[event])
			hooks[event] = append(groups, map[string]any{"hooks": []any{i.expectedHook(agent, event)}})
		}
	}
	if len(hooks) > 0 {
		root["hooks"] = hooks
	} else if _, ok := root["hooks"]; ok {
		delete(root, "hooks")
	}
	return nil
}
func checkInstallPath(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink account path is unsupported: %s", p)
			}
			if p == path && !info.Mode().IsRegular() {
				return fmt.Errorf("account target must be a regular file: %s", p)
			}
			if p != path && !info.IsDir() {
				return fmt.Errorf("account parent is not a directory: %s", p)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func (i *Installation) jsonPath(agent string) string {
	if agent == "claude" {
		return i.targets.ClaudeSettings
	}
	return i.targets.CodexHooks
}
func (i *Installation) Plan(agents []string, uninstall bool) (InstallPlan, error) {
	selected, err := installAgents(agents)
	plan := InstallPlan{Agents: selected, Uninstall: uninstall}
	if err != nil {
		return plan, err
	}
	i, err = i.resolveSelectedTargets(selected)
	if err != nil {
		return plan, err
	}
	op := "install"
	if uninstall {
		op = "uninstall"
	}
	for _, agent := range selected {
		operation := op
		path := i.jsonPath(agent)
		if err := checkInstallPath(path); err != nil {
			return plan, err
		}
		root, err := agenthooks.ReadJSONObject(path)
		if err != nil {
			return plan, fmt.Errorf("%s: %w", path, err)
		}
		before, _ := json.Marshal(root)
		if err := i.transformHooks(root, agent, uninstall); err != nil {
			return plan, fmt.Errorf("%s: %w", path, err)
		}
		after, _ := json.Marshal(root)
		if agent == "codex" {
			path := i.targets.CodexConfig
			if err := checkInstallPath(path); err != nil {
				return plan, err
			}
			before, err := readInstallFile(path)
			if err != nil {
				return plan, err
			}
			after, err := i.migrateTOML(before)
			if err != nil {
				return plan, fmt.Errorf("%s: %w", path, err)
			}
			plan.Changes = append(plan.Changes, InstallChange{Account: i.targets.CodexAccount, Agent: agent, Path: path, Changed: string(before) != string(after), Operation: "remove legacy instruction hooks"})
		}
		account := i.targets.ClaudeAccount
		if agent == "codex" {
			account = i.targets.CodexAccount
		}
		plan.Changes = append(plan.Changes, InstallChange{Account: account, Agent: agent, Path: path, Changed: string(before) != string(after), Operation: operation})
	}
	return plan, nil
}
func (i *Installation) Apply(plan InstallPlan) (InstallResult, error) {
	result := InstallResult{}
	selected, err := installAgents(plan.Agents)
	if err != nil {
		return result, err
	}
	i, err = i.resolveSelectedTargets(selected)
	if err != nil {
		return result, err
	}
	fresh, err := i.Plan(plan.Agents, plan.Uninstall)
	if err != nil {
		return result, err
	}
	for _, change := range fresh.Changes {
		result.FailedPath = change.Path
		changed := false
		var expectedJSON []byte
		if change.Path == i.targets.CodexConfig {
			changed, err = i.updateTOML(change.Path)
		} else {
			_, err = agenthooks.UpdateJSONObjectWithBackup(change.Path, func(root map[string]any) error {
				if err := checkInstallPath(change.Path); err != nil {
					return err
				}
				before, _ := json.Marshal(root)
				if err := i.transformHooks(root, change.Agent, plan.Uninstall); err != nil {
					return err
				}
				after, _ := json.Marshal(root)
				changed = string(before) != string(after)
				expectedJSON = after
				return nil
			})
		}
		if err != nil {
			if changed {
				if change.Path == i.targets.CodexConfig {
					result.Applied = append(result.Applied, change.Path)
				} else if saved, readErr := agenthooks.ReadJSONObject(change.Path); readErr == nil {
					actual, _ := json.Marshal(saved)
					if string(actual) == string(expectedJSON) {
						result.Applied = append(result.Applied, change.Path)
					}
				}
			}
			return result, fmt.Errorf("account update failed at %s (applied %d files): %w", change.Path, len(result.Applied), err)
		}
		if changed {
			result.Applied = append(result.Applied, change.Path)
		} else {
			result.Unchanged = append(result.Unchanged, change.Path)
		}
	}
	result.FailedPath = ""
	return result, nil
}
func readInstallFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}
func (i *Installation) Inspect(agents []string) ([]InstallStatus, error) {
	selected, err := installAgents(agents)
	if err != nil {
		return nil, err
	}
	i, err = i.resolveSelectedTargets(selected)
	if err != nil {
		return nil, err
	}
	statuses := []InstallStatus{}
	for _, agent := range selected {
		path := i.jsonPath(agent)
		account := i.targets.ClaudeAccount
		if agent == "codex" {
			account = i.targets.CodexAccount
		}
		s := InstallStatus{Account: account, Agent: agent, Paths: []string{path}, Configured: true}
		problem := func(msg string) { s.Configured = false; s.Problems = append(s.Problems, msg) }
		root, err := readInstallJSON(path)
		if err != nil {
			problem(err.Error())
			statuses = append(statuses, s)
			continue
		}
		cloneBytes, err := json.Marshal(root)
		clone := map[string]any{}
		if err == nil {
			decoder := json.NewDecoder(bytes.NewReader(cloneBytes))
			decoder.UseNumber()
			err = decoder.Decode(&clone)
		}
		if err != nil {
			problem(err.Error())
			statuses = append(statuses, s)
			continue
		}
		if err := i.transformHooks(clone, agent, true); err != nil {
			problem(err.Error())
		}
		if v, ok := root["disableAllHooks"]; agent == "claude" && ok && v != false {
			problem("disableAllHooks blocks hook execution or is invalid")
		}
		events := instructionEvents(agent)
		expected := map[string]map[string]any{}
		for _, event := range events {
			expected[event] = i.expectedHook(agent, event)
		}
		counts := map[string]int{}
		hooks, _ := root["hooks"].(map[string]any)
		for event, raw := range hooks {
			if agent == "codex" && event == "state" {
				continue
			}
			groups, _ := installArray(raw)
			for _, rawGroup := range groups {
				g, _ := rawGroup.(map[string]any)
				entries, _ := installArray(g["hooks"])
				for _, e := range entries {
					h, _ := e.(map[string]any)
					c, _ := h["command"].(string)
					if !i.owns(c, agent, event) {
						continue
					}
					want, current := expected[event]
					if !current || c != want["command"] {
						problem(fmt.Sprintf("%s: obsolete managed hook; run setup again", event))
						continue
					}
					counts[event]++
					if !installEqual(h, want) {
						problem(fmt.Sprintf("%s: managed hook differs from the fixed execution settings", event))
					}
					for k := range g {
						if k != "hooks" {
							problem(fmt.Sprintf("%s: unsupported group fields or matcher", event))
							break
						}
					}
				}
			}
		}
		for _, event := range events {
			if count := counts[event]; count != 1 {
				problem(fmt.Sprintf("%s: expected one managed hook, found %d", event, count))
			}
		}
		if agent == "codex" {
			s.Paths = append(s.Paths, i.targets.CodexConfig)
			for _, msg := range i.inspectCodexConfig() {
				problem(msg)
			}
		}
		sort.Strings(s.Problems)
		s.Problems = uniqueInstallStrings(s.Problems)
		statuses = append(statuses, s)
	}
	return statuses, nil
}
func readInstallJSON(path string) (map[string]any, error) {
	if err := checkInstallPath(path); err != nil {
		return nil, err
	}
	return agenthooks.ReadJSONObject(path)
}
func (i *Installation) inspectCodexConfig() []string {
	problems := []string{}
	if err := checkInstallPath(i.targets.CodexConfig); err != nil {
		return []string{err.Error()}
	}
	b, err := readInstallFile(i.targets.CodexConfig)
	if err != nil {
		return []string{err.Error()}
	}
	_, err = parseInstallTOML(b)
	if err != nil {
		return []string{err.Error()}
	}
	after, err := i.migrateTOML(b)
	if err != nil {
		problems = append(problems, err.Error())
	} else if string(after) != string(b) {
		problems = append(problems, "managed config.toml instruction hooks need removal")
	}
	return problems
}
func uniqueInstallStrings(v []string) []string {
	out := []string{}
	for _, s := range v {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func (i *Installation) ExpectedCodexHooks() map[string]string {
	return map[string]string{"sessionStart": i.command("codex", instructionEvent)}
}
