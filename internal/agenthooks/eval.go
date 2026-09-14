package agenthooks

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

type Decision struct {
	Decision string   `json:"decision"`
	Allowed  bool     `json:"allowed"`
	RuleID   string   `json:"ruleId,omitempty"`
	PolicyID string   `json:"policyId,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Command  []string `json:"command,omitempty"`
}

type TestResult struct {
	PolicyID string `json:"policyId"`
	Name     string `json:"name"`
	Command  string `json:"command"`
	Want     string `json:"want"`
	Got      string `json:"got"`
	RuleID   string `json:"ruleId,omitempty"`
	Passed   bool   `json:"passed"`
	Error    string `json:"error,omitempty"`
}

var intArgRe = regexp.MustCompile(`^[0-9]+$`)

func EvaluateCommand(policies []Policy, command string) (Decision, error) {
	invocations, err := ParseShellInvocations(command)
	if err != nil {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   "shell command could not be parsed by policy evaluator: " + err.Error(),
		}, nil
	}
	return EvaluateInvocations(policies, invocations), nil
}

func EvaluateInvocations(policies []Policy, invocations []Invocation) Decision {
	final := Decision{Decision: DecisionAllow, Allowed: true}
	for _, inv := range invocations {
		decision := evaluateInvocation(policies, inv)
		if !decision.Allowed {
			return decision
		}
		if final.RuleID == "" && decision.RuleID != "" {
			final = decision
		}
	}
	return final
}

func evaluateInvocation(policies []Policy, inv Invocation) Decision {
	if inv.DynamicCommand && hasEnabledDenyRules(policies) {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   "dynamic command name for protected policy is blocked",
			Command:  visibleArgv(inv.Argv, inv.Dynamic),
		}
	}
	if inv.Dynamic && protectedDynamicInvocation(policies, inv) {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   "dynamic arguments for protected command are blocked",
			Command:  visibleArgv(inv.Argv, inv.Dynamic),
		}
	}
	if len(inv.Argv) > 0 && hasEnabledDenyRulesForCommand(policies, commandName(inv.Argv[0])) {
		if _, _, err := commandFlags(inv.Argv); err != nil {
			return Decision{Decision: DecisionDeny, Allowed: false, Reason: err.Error(), Command: visibleArgv(inv.Argv, inv.Dynamic)}
		}
	}
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			matched, err := matchCommand(rule.Match, inv.Argv)
			if err != nil {
				return Decision{Decision: DecisionDeny, Allowed: false, Reason: err.Error(), Command: visibleArgv(inv.Argv, inv.Dynamic)}
			}
			if !matched {
				continue
			}
			excepted, err := matchAny(rule.Except, inv.Argv)
			if err != nil {
				return Decision{Decision: DecisionDeny, Allowed: false, Reason: err.Error(), Command: visibleArgv(inv.Argv, inv.Dynamic)}
			}
			if excepted {
				continue
			}
			decision := Decision{
				Decision: rule.Effect,
				Allowed:  rule.Effect == EffectAllow,
				RuleID:   rule.ID,
				PolicyID: policy.ID,
				Reason:   rule.Message,
				Command:  visibleArgv(inv.Argv, inv.Dynamic),
			}
			if decision.Reason == "" {
				decision.Reason = fmt.Sprintf("matched policy %s rule %s", policy.ID, rule.ID)
			}
			return decision
		}
	}
	if deny, reason := unknownAliasableInvocation(policies, inv.Argv); deny {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   reason,
			Command:  visibleArgv(inv.Argv, inv.Dynamic),
		}
	}
	return Decision{Decision: DecisionAllow, Allowed: true}
}

func EvaluateHookEvent(policies []Policy, input []byte) (Decision, error) {
	command, err := CommandFromHookEvent(input)
	if err != nil {
		return Decision{}, err
	}
	return EvaluateCommand(policies, command)
}

func CommandFromHookEvent(input []byte) (string, error) {
	var event struct {
		ToolInput map[string]json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &event); err != nil {
		return "", err
	}
	if len(event.ToolInput) == 0 {
		return "", fmt.Errorf("hook event has no tool_input")
	}
	for _, key := range []string{"command", "cmd"} {
		raw, ok := event.ToolInput[key]
		if !ok {
			continue
		}
		var command string
		if err := json.Unmarshal(raw, &command); err != nil {
			return "", fmt.Errorf("tool_input.%s is not a string", key)
		}
		if strings.TrimSpace(command) == "" {
			return "", fmt.Errorf("tool_input.%s is empty", key)
		}
		return command, nil
	}
	return "", fmt.Errorf("hook event has no tool_input.command")
}

func RunPolicyTests(policies []Policy) []TestResult {
	var results []TestResult
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, test := range policy.Tests {
			decision, err := EvaluateCommand(policies, test.Command)
			result := TestResult{
				PolicyID: policy.ID,
				Name:     test.Name,
				Command:  test.Command,
				Want:     test.Want,
			}
			if err != nil {
				result.Error = err.Error()
				result.Got = DecisionDeny
				results = append(results, result)
				continue
			}
			result.Got = decision.Decision
			result.RuleID = decision.RuleID
			result.Passed = result.Got == test.Want && (test.RuleID == "" || test.RuleID == decision.RuleID)
			results = append(results, result)
		}
	}
	return results
}

func matchAny(matches []Match, argv []string) (bool, error) {
	for _, match := range matches {
		matched, err := matchCommand(match, argv)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

func matchCommand(match Match, argv []string) (bool, error) {
	if len(argv) == 0 {
		return false, nil
	}
	if len(match.Argv) > 0 {
		if len(argv) < len(match.Argv) {
			return false, nil
		}
		if match.Exact && len(argv) != len(match.Argv) {
			return false, nil
		}
		for i, pattern := range match.Argv {
			if !matchArg(pattern, argv[i], i == 0) {
				return false, nil
			}
		}
	}
	for _, pattern := range match.Contains {
		if !argvContains(argv, pattern) {
			return false, nil
		}
	}
	for _, flag := range match.HasFlag {
		matched, err := argvHasFlag(argv, flag)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

func matchArg(pattern ArgPattern, arg string, command bool) bool {
	if command && pattern.Exact != "" && !strings.Contains(pattern.Exact, "/") {
		arg = commandName(arg)
	}
	switch {
	case pattern.Exact != "":
		if pattern.Fold {
			return strings.EqualFold(arg, pattern.Exact)
		}
		return arg == pattern.Exact
	case pattern.Type == "int":
		return intArgRe.MatchString(arg)
	case pattern.Type == "nonempty":
		return arg != ""
	case pattern.Glob != "":
		glob := pattern.Glob
		if pattern.Fold {
			glob = strings.ToLower(glob)
			arg = strings.ToLower(arg)
		}
		ok, err := path.Match(glob, arg)
		return err == nil && ok
	default:
		return false
	}
}

func argvContains(argv []string, pattern ArgPattern) bool {
	for i, arg := range argv {
		if matchArg(pattern, arg, i == 0) {
			return true
		}
	}
	return false
}

func argvHasFlag(argv []string, flag string) (bool, error) {
	if len(argv) == 0 {
		return false, nil
	}
	if flags, supported, err := commandFlags(argv); supported {
		if err != nil {
			return false, err
		}
		for _, parsed := range flags {
			if parsed.disabled && !strings.Contains(flag, "=") {
				continue
			}
			if parsed.name == flag || parsed.token == flag || strings.HasPrefix(parsed.token, flag+"=") {
				return true, nil
			}
		}
		return false, nil
	}
	args := argv[1:]
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") || shortFlagGroupMatches(arg, flag) || longFlagAbbreviationMatches(arg, flag) {
			return true, nil
		}
	}
	return false, nil
}

func shortFlagGroupMatches(arg, flag string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(flag) != 2 || !strings.HasPrefix(flag, "-") {
		return false
	}
	return strings.ContainsRune(arg[1:], rune(flag[1]))
}

func longFlagAbbreviationMatches(arg, flag string) bool {
	if !strings.HasPrefix(arg, "--") || len(arg) == 2 || !strings.HasPrefix(flag, "--") {
		return false
	}
	if strings.Contains(arg, "=") || len(arg) >= len(flag) {
		return false
	}
	return strings.HasPrefix(flag, arg)
}

func protectedDynamicInvocation(policies []Policy, inv Invocation) bool {
	if !inv.Dynamic || len(inv.Argv) == 0 || inv.Argv[0] == "" {
		return false
	}
	first := commandName(inv.Argv[0])
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			if rule.Effect != EffectDeny || len(rule.Match.Argv) == 0 {
				continue
			}
			if matchArg(rule.Match.Argv[0], first, true) {
				return true
			}
		}
	}
	return false
}

func hasEnabledDenyRules(policies []Policy) bool {
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			if rule.Effect == EffectDeny {
				return true
			}
		}
	}
	return false
}

func unknownAliasableInvocation(policies []Policy, argv []string) (bool, string) {
	if len(argv) < 2 {
		return false, ""
	}
	name := commandName(argv[0])
	subcommand := argv[1]
	if strings.HasPrefix(subcommand, "-") {
		return false, ""
	}
	switch name {
	case "git":
		if !hasEnabledDenyRulesForCommand(policies, "git") || knownGitSubcommand(subcommand) {
			return false, ""
		}
		return true, "unknown git subcommand can resolve to a git alias or external helper"
	case "gh":
		if !hasEnabledDenyRulesForCommand(policies, "gh") || knownGhCommand(subcommand) {
			return false, ""
		}
		return true, "unknown gh command can resolve to a gh alias or extension"
	default:
		return false, ""
	}
}

func hasEnabledDenyRulesForCommand(policies []Policy, command string) bool {
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			if rule.Effect != EffectDeny || len(rule.Match.Argv) == 0 {
				continue
			}
			if matchArg(rule.Match.Argv[0], command, true) {
				return true
			}
		}
	}
	return false
}

func knownGitSubcommand(subcommand string) bool {
	_, ok := knownGitSubcommands[subcommand]
	return ok
}

func knownGhCommand(command string) bool {
	_, ok := knownGhCommands[command]
	return ok
}

// knownGitSubcommands lists git subcommands guaranteed to exist on any git 2.30+:
// compiled builtins plus the helper scripts that ship with core git. git ignores
// an alias that shadows an existing command, so for these a pre-existing alias
// cannot silently redirect `git <subcommand>`, and it is safe to skip the
// unknown-alias deny. Optional or separately packaged commands (git-svn, git-p4,
// git-cvs*, git-archimport, gui/gitk/citool, gitweb, git-send-email) and commands
// first shipped after git 2.30 (scalar, replay, repo, refs, diagnose, backfill,
// last-modified, diff-pairs) are intentionally excluded: they may be absent, so
// an alias could shadow them, and they must stay fail-closed via the unknown-
// subcommand deny. `hook` (git 2.36) is listed only so its dedicated deny rule
// supplies the block message; the deny rule matches before the unknown-alias check.
var knownGitSubcommands = map[string]struct{}{
	"add": {}, "am": {}, "annotate": {}, "apply": {}, "archive": {}, "bisect": {}, "blame": {},
	"branch": {}, "bugreport": {}, "bundle": {}, "cat-file": {}, "check-attr": {}, "check-ignore": {},
	"check-mailmap": {}, "check-ref-format": {}, "checkout": {}, "checkout-index": {}, "cherry": {},
	"cherry-pick": {}, "clean": {}, "clone": {}, "column": {}, "commit": {}, "commit-graph": {},
	"commit-tree": {}, "config": {}, "count-objects": {}, "credential": {}, "credential-cache": {},
	"credential-store": {}, "daemon": {}, "describe": {}, "diff": {}, "diff-files": {}, "diff-index": {},
	"diff-tree": {}, "difftool": {}, "fast-export": {}, "fast-import": {}, "fetch": {}, "fetch-pack": {},
	"filter-branch": {}, "fmt-merge-msg": {}, "for-each-ref": {}, "for-each-repo": {}, "format-patch": {},
	"fsck": {}, "gc": {}, "get-tar-commit-id": {}, "grep": {}, "hash-object": {}, "help": {},
	"hook": {}, "http-backend": {}, "imap-send": {}, "index-pack": {}, "init": {}, "instaweb": {},
	"interpret-trailers": {}, "log": {}, "ls-files": {}, "ls-remote": {}, "ls-tree": {}, "mailinfo": {},
	"mailsplit": {}, "maintenance": {}, "merge": {}, "merge-base": {}, "merge-file": {},
	"merge-index": {}, "merge-one-file": {}, "merge-tree": {}, "mergetool": {}, "mktag": {}, "mktree": {},
	"multi-pack-index": {}, "mv": {}, "name-rev": {}, "notes": {}, "pack-objects": {},
	"pack-redundant": {}, "pack-refs": {}, "patch-id": {}, "prune": {}, "prune-packed": {}, "pull": {},
	"push": {}, "quiltimport": {}, "range-diff": {}, "read-tree": {}, "rebase": {}, "reflog": {},
	"remote": {}, "repack": {}, "replace": {}, "request-pull": {}, "rerere": {}, "reset": {},
	"restore": {}, "rev-list": {}, "rev-parse": {}, "revert": {}, "rm": {}, "send-pack": {},
	"shortlog": {}, "show": {}, "show-branch": {}, "show-index": {}, "show-ref": {},
	"sparse-checkout": {}, "stash": {}, "status": {}, "stripspace": {}, "submodule": {}, "switch": {},
	"symbolic-ref": {}, "tag": {}, "unpack-file": {}, "unpack-objects": {}, "update-index": {},
	"update-ref": {}, "update-server-info": {}, "var": {}, "verify-commit": {}, "verify-pack": {},
	"verify-tag": {}, "version": {}, "whatchanged": {}, "worktree": {}, "write-tree": {},
}

var knownGhCommands = map[string]struct{}{
	"agent-task": {}, "alias": {}, "api": {}, "attestation": {}, "auth": {}, "browse": {},
	"cache": {}, "co": {}, "codespace": {}, "completion": {}, "config": {}, "copilot": {},
	"extension": {}, "gist": {}, "gpg-key": {}, "help": {}, "issue": {}, "label": {},
	"licenses": {}, "org": {}, "pr": {}, "preview": {}, "project": {}, "release": {},
	"repo": {}, "ruleset": {}, "run": {}, "search": {}, "secret": {}, "skill": {},
	"ssh-key": {}, "stack": {}, "status": {}, "variable": {}, "workflow": {},
}

func visibleArgv(argv []string, dynamic bool) []string {
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "" && dynamic {
			out = append(out, "<dynamic>")
			continue
		}
		out = append(out, arg)
	}
	return out
}
