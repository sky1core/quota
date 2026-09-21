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
	source   decisionSource
}

type TestResult struct {
	PolicyID string `json:"policyId"`
	Name     string `json:"name"`
	Group    string `json:"group,omitempty"`
	Command  string `json:"command"`
	Want     string `json:"want"`
	Got      string `json:"got"`
	RuleID   string `json:"ruleId,omitempty"`
	Source   string `json:"source,omitempty"`
	Passed   bool   `json:"passed"`
	Error    string `json:"error,omitempty"`
}

type decisionSource string

const (
	decisionSourceAllow       decisionSource = "allow"
	decisionSourceRule        decisionSource = "rule"
	decisionSourceLiteralRule decisionSource = "literal-rule"
	decisionSourceUndecidable decisionSource = "undecidable"
)

var intArgRe = regexp.MustCompile(`^[0-9]+$`)

const (
	riskKillMultiplePIDs           = "kill-multiple-pids"
	riskKillMultiplePIDsWithSignal = "kill-multiple-pids-with-signal"
	riskKillZeroPID                = "kill-zero-pid"
	riskKillNegativePID            = "kill-negative-pid"
	riskKillNegativePIDAfterEnd    = "kill-negative-pid-after-end"
)

func EvaluateCommand(policies []Policy, command string) (Decision, error) {
	invocations, err := ParseShellInvocations(command)
	if err != nil {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   "shell command could not be parsed by policy evaluator: " + err.Error(),
			source:   decisionSourceUndecidable,
		}, nil
	}
	return evaluateInvocations(policies, invocations), nil
}

func evaluateInvocations(policies []Policy, invocations []Invocation) Decision {
	final := Decision{Decision: DecisionAllow, Allowed: true, source: decisionSourceAllow}
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
	for _, argv := range inv.auditArgv {
		audit := Invocation{Argv: append([]string(nil), argv...), command: parseCommand(argv)}
		decision := evaluateInvocationWithoutAudit(policies, audit)
		if !decision.Allowed {
			return decision
		}
	}
	return evaluateInvocationWithoutAudit(policies, inv)
}

func evaluateInvocationWithoutAudit(policies []Policy, inv Invocation) Decision {
	if inv.command.undecidable != "" {
		return Decision{Decision: DecisionDeny, Allowed: false, Reason: inv.command.undecidable, Command: visibleArgv(inv.Argv, inv.Dynamic), source: decisionSourceUndecidable}
	}
	if inv.DynamicCommand {
		reason := inv.DynamicReason
		if reason == "" {
			reason = "command cannot be determined by policy evaluator"
		}
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   reason,
			Command:  visibleArgv(inv.Argv, inv.Dynamic),
			source:   decisionSourceUndecidable,
		}
	}
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			if inv.Dynamic {
				if reason, ok := dynamicRuleUncertainty(rule, inv); ok {
					return Decision{Decision: DecisionDeny, Allowed: false, Reason: reason, Command: visibleArgv(inv.Argv, inv.Dynamic), source: decisionSourceUndecidable}
				}
			}
			if (inv.command.flagError != "" || inv.command.flagsUncertain) && ruleNeedsFlags(rule) {
				prefix := rule.Match
				prefix.HasFlag = nil
				if matchCommand(prefix, inv) {
					reason := inv.command.flagError
					if reason == "" {
						reason = "dynamic arguments cannot be checked against configured flag restrictions"
					}
					return Decision{Decision: DecisionDeny, Allowed: false, Reason: reason, Command: visibleArgv(inv.Argv, inv.Dynamic), source: decisionSourceUndecidable}
				}
			}
			if !matchCommand(rule.Match, inv) {
				continue
			}
			if matchAny(rule.Except, inv) {
				continue
			}
			decision := Decision{
				Decision: rule.Effect,
				Allowed:  rule.Effect == EffectAllow,
				RuleID:   rule.ID,
				PolicyID: policy.ID,
				Reason:   rule.Message,
				Command:  visibleArgv(inv.Argv, inv.Dynamic),
				source:   decisionSourceRule,
			}
			if decision.Reason == "" {
				decision.Reason = fmt.Sprintf("matched policy %s rule %s", policy.ID, rule.ID)
			}
			return decision
		}
	}

	return Decision{Decision: DecisionAllow, Allowed: true, source: decisionSourceAllow}
}

func ruleNeedsFlags(rule Rule) bool {
	if len(rule.Match.HasFlag) > 0 {
		return true
	}
	for _, except := range rule.Except {
		if len(except.HasFlag) > 0 {
			return true
		}
	}
	return false
}

func dynamicRuleUncertainty(rule Rule, inv Invocation) (string, bool) {
	if matchNeedsRisk(rule.Match) && dynamicRiskApplies(rule.Match, inv) {
		return "dynamic arguments cannot be checked against configured risk restrictions", true
	}
	for _, except := range rule.Except {
		if matchNeedsRisk(except) && dynamicRiskApplies(except, inv) {
			return "dynamic arguments cannot be checked against configured risk restrictions", true
		}
	}
	if matchNeedsContains(rule.Match) && dynamicContainsApplies(rule.Match, inv) {
		return "dynamic arguments cannot be checked against configured token restrictions", true
	}
	for _, except := range rule.Except {
		if matchNeedsContains(except) && dynamicContainsApplies(except, inv) {
			return "dynamic arguments cannot be checked against configured token restrictions", true
		}
	}
	return "", false
}

func matchNeedsRisk(match Match) bool { return match.Risk != "" }

func matchNeedsContains(match Match) bool { return len(match.Contains) > 0 }

func dynamicRiskApplies(match Match, inv Invocation) bool {
	prefix := match
	prefix.Risk = ""
	if !matchCommand(prefix, inv) {
		return false
	}
	if match.Risk == PolicyGroupRemoteCodeRefMutation {
		return !inv.command.allowDynamicArgs
	}
	if len(inv.Argv) == 0 || commandName(inv.Argv[0]) != "kill" {
		return false
	}
	for i := 1; i < len(inv.Argv); i++ {
		if inv.dynamicAt(i) || inv.maySplitAt(i) {
			return true
		}
	}
	return false
}

func dynamicContainsApplies(match Match, inv Invocation) bool {
	prefix := match
	prefix.Contains = nil
	if !matchCommand(prefix, inv) {
		return false
	}
	for i := range inv.Argv {
		if inv.maySplitAt(i) || inv.dynamicAt(i) && !inv.literalPrefixAt(i) {
			return true
		}
	}
	return false
}

func killRisk(argv []string) string {
	sawSignal := false
	sawEnd := false
	signalConsumed := false
	operands := make([]string, 0, len(argv))
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if sawEnd {
			operands = append(operands, arg)
			continue
		}
		switch {
		case arg == "--":
			sawEnd = true
		case !signalConsumed && (arg == "-s" || arg == "--signal" || arg == "-n"):
			sawSignal = true
			signalConsumed = true
			if i+1 < len(argv) {
				i++
			}
		case !signalConsumed && (strings.HasPrefix(arg, "--signal=") || strings.HasPrefix(arg, "-s") && len(arg) > 2 || strings.HasPrefix(arg, "-n") && len(arg) > 2):
			sawSignal = true
			signalConsumed = true
		case !signalConsumed && strings.HasPrefix(arg, "-") && len(arg) > 1 && i+1 < len(argv):
			sawSignal = true
			signalConsumed = true
		default:
			operands = append(operands, arg)
		}
	}
	pidOperands := 0
	zeroPID := false
	for _, operand := range operands {
		if negativeIntArg(operand) {
			if sawEnd {
				return riskKillNegativePIDAfterEnd
			}
			return riskKillNegativePID
		}
		if zeroIntArg(operand) {
			zeroPID = true
		}
		if intArgRe.MatchString(operand) {
			pidOperands++
		}
	}
	if pidOperands >= 2 {
		if sawSignal {
			return riskKillMultiplePIDsWithSignal
		}
		return riskKillMultiplePIDs
	}
	if zeroPID {
		return riskKillZeroPID
	}
	return ""
}

func negativeIntArg(arg string) bool {
	return strings.HasPrefix(arg, "-") && len(arg) > 1 && intArgRe.MatchString(arg[1:])
}

func zeroIntArg(arg string) bool {
	return intArgRe.MatchString(arg) && strings.TrimLeft(arg, "0") == ""
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
				Group:    test.Group,
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
			result.Source = string(decision.source)
			result.Passed = result.Got == test.Want && (test.RuleID == "" || test.RuleID == decision.RuleID) && (test.Source == "" || test.Source == result.Source)
			results = append(results, result)
		}
	}
	return results
}

func matchAny(matches []Match, inv Invocation) bool {
	for _, match := range matches {
		if matchCommand(match, inv) {
			return true
		}
	}
	return false
}

func matchCommand(match Match, inv Invocation) bool {
	argv := inv.Argv
	if len(argv) == 0 {
		return false
	}
	if len(match.Argv) > 0 {
		if len(argv) < len(match.Argv) {
			return false
		}
		if match.Exact && len(argv) != len(match.Argv) {
			return false
		}
		for i, pattern := range match.Argv {
			if !matchArg(pattern, argv[i], i == 0) {
				return false
			}
		}
	}
	for _, pattern := range match.Contains {
		if !argvContains(argv, pattern) {
			return false
		}
	}
	for _, flag := range match.HasFlag {
		if !invocationHasFlag(inv, flag) {
			return false
		}
	}
	if match.Risk != "" && !matchRisk(match.Risk, inv) {
		return false
	}
	return true
}

func matchRisk(risk string, inv Invocation) bool {
	if risk == PolicyGroupRemoteCodeRefMutation {
		return inv.command.risk == risk
	}
	if len(inv.Argv) == 0 || commandName(inv.Argv[0]) != "kill" {
		return false
	}
	switch risk {
	case riskKillMultiplePIDs, riskKillMultiplePIDsWithSignal, riskKillZeroPID, riskKillNegativePID, riskKillNegativePIDAfterEnd:
		return killRisk(inv.Argv) == risk
	default:
		return false
	}
}

func matchArg(pattern ArgPattern, arg string, command bool) bool {
	if command && ((pattern.Exact != "" && !strings.Contains(pattern.Exact, "/")) || (pattern.Glob != "" && !strings.Contains(pattern.Glob, "/"))) {
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
		return matchArgGlob(glob, arg)
	default:
		return false
	}
}

func matchArgGlob(pattern, arg string) bool {
	const slash = "\x00"
	pattern = strings.ReplaceAll(pattern, "/", slash)
	arg = strings.ReplaceAll(arg, "/", slash)
	ok, err := path.Match(pattern, arg)
	return err == nil && ok
}

func argvContains(argv []string, pattern ArgPattern) bool {
	for i, arg := range argv {
		if matchArg(pattern, arg, i == 0) {
			return true
		}
	}
	return false
}

func invocationHasFlag(inv Invocation, flag string) bool {
	for _, parsed := range inv.command.flags {
		if parsed.disabled && !strings.Contains(flag, "=") {
			continue
		}
		if parsed.name == flag || parsed.token == flag || strings.HasPrefix(parsed.token, flag+"=") || parsed.literal && longFlagAbbreviationMatches(parsed.token, flag) {
			return true
		}
	}
	return false
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

func protectedInvocation(inv Invocation) bool {
	if len(inv.Argv) == 0 {
		return false
	}
	switch commandName(inv.Argv[0]) {
	case "git", "gh", "dd", "kill":
		return true
	default:
		return false
	}
}

func knownGitSubcommand(subcommand string) bool {
	_, ok := knownGitSubcommands[subcommand]
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
// subcommand deny.
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
	"hook": {}, "http-backend": {}, "http-push": {}, "imap-send": {}, "index-pack": {}, "init": {}, "instaweb": {},
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
