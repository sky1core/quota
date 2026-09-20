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
	decision := evaluateInvocations(policies, invocations)
	if !decision.Allowed {
		return decision, nil
	}
	if decision, ok := literalDenyDecision(policies, command, invocations); ok {
		return decision, nil
	}
	return decision, nil
}

func literalDenyDecision(policies []Policy, command string, invocations []Invocation) (Decision, bool) {
	candidates := literalDenyCandidates(command, invocations)
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
			if rule.Effect != EffectDeny {
				continue
			}
			for _, candidate := range candidates {
				seq, ok := literalDenyMatch(rule, candidate)
				if !ok {
					continue
				}
				reason := rule.Message
				if reason == "" {
					reason = fmt.Sprintf("matched policy %s rule %s", policy.ID, rule.ID)
				}
				return Decision{
					Decision: rule.Effect,
					Allowed:  false,
					RuleID:   rule.ID,
					PolicyID: policy.ID,
					Reason:   reason,
					Command:  seq,
					source:   decisionSourceLiteralRule,
				}, true
			}
		}
	}
	return Decision{}, false
}

func literalDenyCandidates(command string, invocations []Invocation) []string {
	if len(invocations) == 0 {
		return []string{command}
	}
	protectedSpans := protectedInvocationSourceSpans(invocations)
	seen := map[string]bool{}
	var candidates []string
	for _, inv := range invocations {
		if protectedInvocation(inv) {
			continue
		}
		for _, candidate := range []string{literalSourceCandidate(inv, protectedSpans), strings.Join(inv.literalArgv, " ")} {
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

type invocationSourceSpan struct {
	sourceID int
	start    int
	end      int
}

func protectedInvocationSourceSpans(invocations []Invocation) []invocationSourceSpan {
	spans := make([]invocationSourceSpan, 0, len(invocations))
	for _, inv := range invocations {
		if protectedInvocation(inv) && inv.sourceEnd > inv.sourceStart {
			spans = append(spans, invocationSourceSpan{sourceID: inv.sourceID, start: inv.sourceStart, end: inv.sourceEnd})
		}
	}
	return spans
}

func literalSourceCandidate(inv Invocation, spans []invocationSourceSpan) string {
	if inv.source == "" || inv.sourceEnd <= inv.sourceStart {
		return inv.source
	}
	b := []byte(inv.source)
	for _, span := range spans {
		if span.sourceID != inv.sourceID || span.start < inv.sourceStart || span.end > inv.sourceEnd {
			continue
		}
		if span.start == inv.sourceStart && span.end == inv.sourceEnd {
			continue
		}
		for i := span.start - inv.sourceStart; i < span.end-inv.sourceStart; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

func literalDenyMatch(rule Rule, command string) ([]string, bool) {
	if len(rule.Match.Argv) == 0 || len(rule.Match.Contains) > 0 || rule.Match.Risk != "" {
		return nil, false
	}
	seq, ok := exactArgSequence(rule.Match.Argv)
	if !ok {
		return nil, false
	}
	first := seq[0]
	if first != "git" && first != "gh" {
		return nil, false
	}
	spans := literalMatchSpans(command, rule.Match, false)
	for i, span := range spans {
		scopeEnd := literalSpanScopeEnd(command, spans, i)
		if len(rule.Match.HasFlag) > 0 && !literalAnyFlagInCommand(command[span.end:scopeEnd], rule.Match.HasFlag) {
			continue
		}
		if literalAnyMatchAtStart(command[span.start:scopeEnd], rule.Except) {
			continue
		}
		return seq, true
	}
	return nil, false
}

func exactArgSequence(patterns []ArgPattern) ([]string, bool) {
	seq := make([]string, len(patterns))
	for i, arg := range patterns {
		if arg.Exact == "" {
			return nil, false
		}
		seq[i] = arg.Exact
	}
	return seq, true
}

type literalSpan struct {
	start int
	end   int
}

func literalAnyMatchAtStart(command string, matches []Match) bool {
	for _, match := range matches {
		if len(match.HasFlag) > 0 {
			continue
		}
		for _, span := range literalMatchSpans(command, match, match.Exact) {
			if span.start == 0 {
				return true
			}
		}
	}
	return false
}

func literalMatchSpans(command string, match Match, exact bool) []literalSpan {
	if len(match.Argv) == 0 || len(match.Contains) > 0 || match.Risk != "" {
		return nil
	}
	pattern, ok := literalMatchPattern(match.Argv, exact)
	if !ok {
		return nil
	}
	locs := regexp.MustCompile(pattern).FindAllStringIndex(command, -1)
	spans := make([]literalSpan, 0, len(locs))
	for _, loc := range locs {
		spans = append(spans, literalSpan{start: loc[0], end: loc[1]})
	}
	return spans
}

func literalSpanScopeEnd(command string, spans []literalSpan, index int) int {
	if index+1 < len(spans) {
		return spans[index+1].start
	}
	return len(command)
}

func literalMatchPattern(args []ArgPattern, exact bool) (string, bool) {
	var b strings.Builder
	b.WriteString(literalPrefixBoundary())
	for i, arg := range args {
		if i > 0 {
			b.WriteString(`[[:space:]]+`)
		}
		token, ok := literalArgPattern(arg)
		if !ok {
			return "", false
		}
		b.WriteString(token)
	}
	if exact {
		b.WriteString(literalExactSuffixBoundary())
	} else {
		b.WriteString(literalSuffixBoundary())
	}
	return b.String(), true
}

func literalArgPattern(arg ArgPattern) (string, bool) {
	switch {
	case arg.Exact != "":
		return regexp.QuoteMeta(arg.Exact), true
	case arg.Type == "int":
		return `[0-9]+`, true
	case arg.Type == "nonempty":
		return `[^[:space:];|&()'"` + "`" + `]+`, true
	default:
		return "", false
	}
}

func literalAnyFlagInCommand(command string, flags []string) bool {
	for _, flag := range flags {
		if literalFlagInCommand(command, flag) {
			return true
		}
	}
	return false
}

func literalFlagInCommand(command, flag string) bool {
	pattern := literalPrefixBoundary() + regexp.QuoteMeta(flag)
	if strings.HasPrefix(flag, "--") {
		pattern += `($|[=[:space:];|&()'"` + "`" + `])`
	} else {
		pattern += literalSuffixBoundary()
	}
	return regexp.MustCompile(pattern).MatchString(command)
}

func literalPrefixBoundary() string {
	return `(^|[[:space:];|&()'"` + "`" + `])`
}

func literalSuffixBoundary() string {
	return `($|[[:space:];|&()'"` + "`" + `])`
}

func literalExactSuffixBoundary() string {
	return `([[:space:]]*($|[;|&()'"` + "`" + `]))`
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
	if inv.Dynamic && protectedInvocation(inv) {
		return Decision{
			Decision: DecisionDeny,
			Allowed:  false,
			Reason:   "dynamic arguments for protected command are blocked",
			Command:  visibleArgv(inv.Argv, inv.Dynamic),
			source:   decisionSourceUndecidable,
		}
	}

	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		for _, rule := range policy.Rules {
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
