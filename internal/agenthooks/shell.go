package agenthooks

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type Invocation struct {
	Argv           []string `json:"argv,omitempty"`
	Dynamic        bool     `json:"dynamic,omitempty"`
	DynamicCommand bool     `json:"dynamicCommand,omitempty"`
	DynamicReason  string   `json:"dynamicReason,omitempty"`
}

func ParseShellInvocations(command string) ([]Invocation, error) {
	return parseShellInvocations(command, 0, "")
}

func parseDirectShellInvocation(command string) (Invocation, bool) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return Invocation{}, false
	}
	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
		return Invocation{}, false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok {
		return Invocation{}, false
	}
	if len(call.Assigns) > 0 {
		return Invocation{}, false
	}
	inv := callInvocation(call)
	if inv.Dynamic {
		return Invocation{}, false
	}
	return inv, true
}

func parseShellInvocations(command string, depth int, inheritedShellStartup string) ([]Invocation, error) {
	if depth > 8 {
		return nil, fmt.Errorf("nested shell command depth exceeded")
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, err
	}
	var invocations []Invocation
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		inv := callInvocation(call)
		if len(inv.Argv) == 0 && !inv.Dynamic {
			return true
		}
		gitConfigDispatch, gitConfigReason := gitConfigAssignmentCanChangeCommandDispatch(call.Assigns)
		if envDispatch, envReason := gitConfigEnvArgsCanChangeCommandDispatch(inv.Argv); envDispatch {
			gitConfigDispatch = true
			gitConfigReason = envReason
		}
		if wrapperDispatch, wrapperReason := gitConfigWrapperCanChangeCommandDispatch(inv.Argv); wrapperDispatch {
			gitConfigDispatch = true
			gitConfigReason = wrapperReason
		}
		ghConfigDispatch, ghConfigReason := ghConfigAssignmentCanChangeCommandDispatch(call.Assigns)
		if envDispatch, envReason := ghConfigEnvArgsCanChangeCommandDispatch(inv.Argv); envDispatch {
			ghConfigDispatch = true
			ghConfigReason = envReason
		}
		if sudoDispatch, sudoReason := ghConfigSudoArgsCanChangeCommandDispatch(inv.Argv); sudoDispatch {
			ghConfigDispatch = true
			ghConfigReason = sudoReason
		}
		if wrapperDispatch, wrapperReason := ghConfigWrapperCanChangeCommandDispatch(inv.Argv); wrapperDispatch {
			ghConfigDispatch = true
			ghConfigReason = wrapperReason
		}
		if inv.Dynamic && len(inv.Argv) > 0 && isCommandWrapper(inv.Argv[0]) {
			inv.DynamicCommand = true
		}
		shellStartupDispatch, shellStartupReason := shellStartupAssignmentCanExecuteHiddenScript(call.Assigns, inv.Argv)
		if inheritedShellStartup != "" {
			shellStartupDispatch, shellStartupReason = true, inheritedShellStartup
		}
		norm, dynamicCommand, dynamicReason := normalizeArgv(inv.Argv)
		if gitConfigDispatch && len(norm) > 0 && commandName(norm[0]) == "git" {
			dynamicCommand = true
			dynamicReason = gitConfigReason
		}
		if ghConfigDispatch && len(norm) > 0 && commandName(norm[0]) == "gh" {
			dynamicCommand = true
			dynamicReason = ghConfigReason
		}
		if !inv.Dynamic {
			if script, ok := evalScript(norm); ok {
				nested, err := parseShellInvocations(script, depth+1, shellStartupReason)
				if err != nil {
					invocations = append(invocations, Invocation{Argv: inv.Argv, Dynamic: true, DynamicCommand: true, DynamicReason: "eval script is not statically parseable"})
					return false
				}
				nested = markInvocationsDynamicForCommand(nested, "git", gitConfigDispatch, gitConfigReason)
				nested = markInvocationsDynamicForCommand(nested, "gh", ghConfigDispatch, ghConfigReason)
				invocations = append(invocations, nested...)
				return true
			}
		}
		if !inv.Dynamic {
			if script, ok := envSplitStringScript(norm); ok {
				nested, err := parseShellInvocations(script, depth+1, shellStartupReason)
				if err != nil {
					invocations = append(invocations, Invocation{Argv: inv.Argv, Dynamic: true, DynamicReason: "env split string is not statically parseable"})
					return false
				}
				nested = markInvocationsDynamicForCommand(nested, "git", gitConfigDispatch, gitConfigReason)
				nested = markInvocationsDynamicForCommand(nested, "gh", ghConfigDispatch, ghConfigReason)
				invocations = append(invocations, nested...)
				return true
			}
		}
		if len(norm) > 0 && isShellCommand(norm[0]) {
			if shellStartupDispatch && shellCanExecuteScript(norm) {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: shellStartupReason})
				return true
			}
			if shellInteractiveOption(norm) && shellCanExecuteScript(norm) {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: "interactive shell startup files can execute hidden script content"})
				return true
			}
			if shellLoginOption(norm) && shellCanExecuteScript(norm) {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: "login shell startup files can execute hidden script content"})
				return true
			}
			if wrapperLoginShellStartupCanExecuteHiddenScript(inv.Argv) && shellCanExecuteScript(norm) {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: "wrapper login shell startup files can execute hidden script content"})
				return true
			}
			if shellStartupFileOption(norm) && shellCanExecuteScript(norm) {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: "shell startup file can execute hidden script content"})
				return true
			}
		}
		if scriptIndex, ok := shellScriptArgIndex(norm); ok && !inv.Dynamic {
			nested, err := parseShellInvocations(norm[scriptIndex], depth+1, shellStartupReason)
			if err != nil {
				invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicReason: "nested shell command is not statically parseable"})
				return false
			}
			nested = markInvocationsDynamicForCommand(nested, "git", gitConfigDispatch, gitConfigReason)
			nested = markInvocationsDynamicForCommand(nested, "gh", ghConfigDispatch, ghConfigReason)
			invocations = append(invocations, nested...)
			return true
		}
		if shellInterpreterWithoutVisibleScript(norm) {
			invocations = append(invocations, Invocation{Argv: norm, Dynamic: true, DynamicCommand: true, DynamicReason: "shell interpreter script is not visible to policy evaluator"})
			return true
		}
		inv.Argv = norm
		if dynamicCommand {
			inv.Dynamic = true
			inv.DynamicCommand = true
			if inv.DynamicReason == "" {
				inv.DynamicReason = dynamicReason
			}
		}
		invocations = append(invocations, inv)
		return true
	})
	return invocations, nil
}

func callInvocation(call *syntax.CallExpr) Invocation {
	var inv Invocation
	for i, word := range call.Args {
		value, ok := staticWord(word)
		if !ok {
			inv.Dynamic = true
			inv.DynamicReason = "command contains shell expansion"
			if i == 0 {
				inv.DynamicCommand = true
			}
			inv.Argv = append(inv.Argv, "")
			continue
		}
		inv.Argv = append(inv.Argv, value)
	}
	return inv
}

func staticWord(word *syntax.Word) (string, bool) {
	var b strings.Builder
	var metaScan strings.Builder
	for _, part := range word.Parts {
		value, ok := staticWordPart(part, false)
		if !ok {
			return "", false
		}
		b.WriteString(value)
		// A brace group can be assembled across quoting boundaries ({g'it',}
		// expands to git), so expansion detection must run on the whole word.
		// Quoted characters cannot act as expansion metacharacters, so they are
		// masked with a neutral byte for the scan.
		if lit, isLit := part.(*syntax.Lit); isLit {
			metaScan.WriteString(lit.Value)
		} else {
			metaScan.WriteString(strings.Repeat("A", len(value)))
		}
	}
	if litHasExpansionMeta(metaScan.String()) {
		return "", false
	}
	return b.String(), true
}

func staticWordPart(part syntax.WordPart, quoted bool) (string, bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		if !quoted && litHasExpansionMeta(p.Value) {
			return "", false
		}
		return staticLitValue(p.Value, quoted), true
	case *syntax.SglQuoted:
		if p.Dollar && p.Value != "" {
			return "", false
		}
		return p.Value, true
	case *syntax.DblQuoted:
		var b strings.Builder
		for _, nested := range p.Parts {
			value, ok := staticWordPart(nested, true)
			if !ok {
				return "", false
			}
			b.WriteString(value)
		}
		return b.String(), true
	default:
		return "", false
	}
}

// litHasExpansionMeta reports whether an unquoted literal would be changed by
// bash brace expansion or globbing, so the evaluator cannot treat it as a plain
// literal and handles it as dynamic. Backslash-escaped characters are literal.
// Unescaped `*`, `?`, and `[` are glob metacharacters. An unescaped `{` counts
// only when it opens a brace group that bash actually expands — one whose top
// level holds an unescaped `,` or a `..` sequence — so reflog-style selectors
// like HEAD@{u} and stash@{0} stay literal while g{i..i}t and g{i,j}t do not.
func litHasExpansionMeta(value string) bool {
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++ // escaped character is literal
		case '*', '?', '[':
			return true
		case '{':
			if braceGroupExpands(value[i:]) {
				return true
			}
		}
	}
	return false
}

// braceGroupExpands reports whether s, which begins at an unescaped '{', opens a
// balanced brace group that bash expands: its top level contains an unescaped
// comma or a `..` sequence. Backslash escapes are honored and an unterminated
// group does not expand.
func braceGroupExpands(s string) bool {
	depth := 0
	expands := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // escaped character cannot open, close, or separate a group
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return expands
			}
		case ',':
			if depth == 1 {
				expands = true
			}
		case '.':
			if depth == 1 && i+1 < len(s) && s[i+1] == '.' {
				expands = true
				i++
			}
		}
	}
	return false
}

func staticLitValue(value string, quoted bool) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch != '\\' {
			b.WriteByte(ch)
			continue
		}
		if i+1 >= len(value) {
			continue
		}
		next := value[i+1]
		if !quoted || next == '$' || next == '`' || next == '"' || next == '\\' || next == '\n' {
			b.WriteByte(next)
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func gitConfigAssignmentCanChangeCommandDispatch(assigns []*syntax.Assign) (bool, string) {
	for _, assign := range assigns {
		if assign.Name == nil {
			continue
		}
		name := assign.Name.Value
		if name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_GLOBAL" || name == "GIT_CONFIG_SYSTEM" || name == "GIT_EXEC_PATH" {
			return true, "git environment can change command dispatch"
		}
		if !strings.HasPrefix(name, "GIT_CONFIG_KEY_") {
			continue
		}
		if assign.Value == nil {
			continue
		}
		value, ok := staticWord(assign.Value)
		if !ok {
			return true, "git config key assignment is not statically parseable"
		}
		if gitConfigKeyCanChangeCommandDispatch(value) {
			return true, "git environment can change command dispatch"
		}
	}
	return false, ""
}

func ghConfigAssignmentCanChangeCommandDispatch(assigns []*syntax.Assign) (bool, string) {
	for _, assign := range assigns {
		if assign.Name == nil {
			continue
		}
		if assign.Name.Value == "GH_CONFIG_DIR" {
			return true, "gh configuration directory can change command dispatch"
		}
	}
	return false, ""
}

func assignmentNameCanChangeGhCommandDispatch(name string) bool {
	return name == "GH_CONFIG_DIR"
}

func shellStartupAssignmentCanExecuteHiddenScript(assigns []*syntax.Assign, argv []string) (bool, string) {
	for _, assign := range assigns {
		if assign.Name == nil {
			continue
		}
		if shellStartupEnvName(assign.Name.Value) {
			return true, "shell startup environment can execute hidden script content"
		}
	}
	return wrapperEnvCanSetShellStartup(argv)
}

func wrapperEnvCanSetShellStartup(argv []string) (bool, string) {
	out := argv
	for {
		if len(out) == 0 {
			return false, ""
		}
		switch commandName(out[0]) {
		case "command":
			next := normalizeCommandArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "builtin":
			if len(out) == 1 {
				return false, ""
			}
			out = out[1:]
		case "exec":
			next := normalizeExecArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "env":
			if ok, reason := envArgsCanSetShellStartup(out); ok {
				return true, reason
			}
			next := normalizeEnvArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "sudo":
			if ok, reason := sudoArgsCanSetShellStartup(out); ok {
				return true, reason
			}
			next := normalizeSudoArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		default:
			return false, ""
		}
	}
}

func envArgsCanSetShellStartup(argv []string) (bool, string) {
	if len(argv) < 2 || commandName(argv[0]) != "env" {
		return false, ""
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false, ""
		}
		if envFlagTakesValue(arg) {
			i++
			continue
		}
		if envShortFlagHasInlineValue(arg) || envFlagHasInlineValue(arg) || envFlagNoValue(arg) {
			continue
		}
		name, _, ok := splitAssignmentArg(arg)
		if !ok || strings.HasPrefix(arg, "-") {
			return false, ""
		}
		if shellStartupEnvName(name) {
			return true, "shell startup environment can execute hidden script content"
		}
	}
	return false, ""
}

func sudoArgsCanSetShellStartup(argv []string) (bool, string) {
	return sudoEnvCanExpose(argv, shellStartupEnvName, "shell startup environment can execute hidden script content")
}

func shellStartupEnvName(name string) bool {
	switch name {
	case "BASH_ENV", "ENV", "ZDOTDIR":
		return true
	default:
		return false
	}
}

func gitConfigEnvArgsCanChangeCommandDispatch(argv []string) (bool, string) {
	if len(argv) < 2 || commandName(argv[0]) != "env" {
		return false, ""
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false, ""
		}
		if envFlagTakesValue(arg) {
			i++
			continue
		}
		if envShortFlagHasInlineValue(arg) || envFlagHasInlineValue(arg) || envFlagNoValue(arg) {
			continue
		}
		if !strings.Contains(arg, "=") || strings.HasPrefix(arg, "-") {
			return false, ""
		}
		name, value, _ := strings.Cut(arg, "=")
		if gitConfigEnvPairCanChangeCommandDispatch(name, value) {
			return true, "git environment can change command dispatch"
		}
	}
	return false, ""
}

func ghConfigEnvArgsCanChangeCommandDispatch(argv []string) (bool, string) {
	if len(argv) < 2 || commandName(argv[0]) != "env" {
		return false, ""
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false, ""
		}
		if envFlagTakesValue(arg) {
			i++
			continue
		}
		if envShortFlagHasInlineValue(arg) || envFlagHasInlineValue(arg) || envFlagNoValue(arg) {
			continue
		}
		if !strings.Contains(arg, "=") || strings.HasPrefix(arg, "-") {
			return false, ""
		}
		name, _, _ := strings.Cut(arg, "=")
		if assignmentNameCanChangeGhCommandDispatch(name) {
			return true, "gh configuration directory can change command dispatch"
		}
	}
	return false, ""
}

func ghConfigSudoArgsCanChangeCommandDispatch(argv []string) (bool, string) {
	return sudoEnvCanExpose(argv, assignmentNameCanChangeGhCommandDispatch, "gh configuration directory can change command dispatch")
}

func gitConfigWrapperCanChangeCommandDispatch(argv []string) (bool, string) {
	out := argv
	for {
		if len(out) == 0 {
			return false, ""
		}
		switch commandName(out[0]) {
		case "command":
			next := normalizeCommandArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "builtin":
			if len(out) == 1 {
				return false, ""
			}
			out = out[1:]
		case "exec":
			next := normalizeExecArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "env":
			if ok, reason := gitConfigEnvArgsCanChangeCommandDispatch(out); ok {
				return true, reason
			}
			next := normalizeEnvArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		default:
			return false, ""
		}
	}
}

func ghConfigWrapperCanChangeCommandDispatch(argv []string) (bool, string) {
	out := argv
	for {
		if len(out) == 0 {
			return false, ""
		}
		switch commandName(out[0]) {
		case "command":
			next := normalizeCommandArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "builtin":
			if len(out) == 1 {
				return false, ""
			}
			out = out[1:]
		case "exec":
			next := normalizeExecArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "env":
			if ok, reason := ghConfigEnvArgsCanChangeCommandDispatch(out); ok {
				return true, reason
			}
			next := normalizeEnvArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		case "sudo":
			if ok, reason := ghConfigSudoArgsCanChangeCommandDispatch(out); ok {
				return true, reason
			}
			next := normalizeSudoArgv(out)
			if len(next) == len(out) {
				return false, ""
			}
			out = next
		default:
			return false, ""
		}
	}
}

func gitConfigEnvPairCanChangeCommandDispatch(name, value string) bool {
	switch name {
	case "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_EXEC_PATH":
		return true
	}
	if strings.HasPrefix(name, "GIT_CONFIG_KEY_") {
		return gitConfigKeyCanChangeCommandDispatch(value)
	}
	return false
}

func markInvocationsDynamicForCommand(invocations []Invocation, command string, dynamic bool, reason string) []Invocation {
	if !dynamic {
		return invocations
	}
	for i := range invocations {
		if len(invocations[i].Argv) == 0 || commandName(invocations[i].Argv[0]) != command {
			continue
		}
		invocations[i].Dynamic = true
		invocations[i].DynamicCommand = true
		if invocations[i].DynamicReason == "" {
			invocations[i].DynamicReason = reason
		}
	}
	return invocations
}

func normalizeArgv(argv []string) ([]string, bool, string) {
	out := argv
	for {
		if len(out) == 0 {
			return out, false, ""
		}
		switch commandName(out[0]) {
		case "command":
			next := normalizeCommandArgv(out)
			if len(next) == len(out) {
				return normalizeFirst(next)
			}
			out = next
		case "builtin":
			if len(out) == 1 {
				return out, false, ""
			}
			out = out[1:]
		case "exec":
			next := normalizeExecArgv(out)
			if len(next) == len(out) {
				return normalizeFirst(next)
			}
			out = next
		case "env":
			next := normalizeEnvArgv(out)
			if len(next) == len(out) {
				return normalizeFirst(next)
			}
			out = next
		case "sudo":
			next := normalizeSudoArgv(out)
			if len(next) == len(out) {
				return normalizeFirst(next)
			}
			out = next
		default:
			return normalizeFirst(out)
		}
	}
}

func isCommandWrapper(cmd string) bool {
	switch commandName(cmd) {
	case "command", "builtin", "exec", "env", "sudo", "eval", "sh", "bash", "zsh", "dash", "ksh":
		return true
	default:
		return false
	}
}

func normalizeCommandArgv(argv []string) []string {
	if len(argv) < 2 || commandName(argv[0]) != "command" {
		return argv
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		switch arg {
		case "-p":
			i++
			continue
		case "-v", "-V":
			return argv
		}
		if strings.HasPrefix(arg, "-") {
			return argv
		}
		break
	}
	if i >= len(argv) {
		return argv
	}
	return argv[i:]
}

func normalizeExecArgv(argv []string) []string {
	if len(argv) < 2 || commandName(argv[0]) != "exec" {
		return argv
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if arg == "-a" {
			if i+1 >= len(argv) {
				return argv
			}
			i += 2
			continue
		}
		if arg == "-c" || arg == "-l" || isExecShortOptionGroup(arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return argv
		}
		break
	}
	if i >= len(argv) {
		return argv
	}
	return argv[i:]
}

func isExecShortOptionGroup(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 3 {
		return false
	}
	for _, ch := range arg[1:] {
		if ch != 'c' && ch != 'l' {
			return false
		}
	}
	return true
}

func normalizeFirst(argv []string) ([]string, bool, string) {
	if len(argv) == 0 {
		return argv, false, ""
	}
	out := append([]string(nil), argv...)
	name := commandName(out[0])
	if subcommand, ok := gitDashedSubcommand(name); ok {
		return append([]string{"git", subcommand}, out[1:]...), false, ""
	}
	switch name {
	case "git":
		out[0] = "git"
		return normalizeGitGlobalOptions(out)
	case "gh":
		out[0] = "gh"
		return normalizeGhGlobalOptions(out)
	}
	return out, false, ""
}

func gitDashedSubcommand(name string) (string, bool) {
	subcommand, ok := strings.CutPrefix(name, "git-")
	if !ok || subcommand == "" {
		return "", false
	}
	return subcommand, true
}

func commandName(arg string) string {
	base := filepath.Base(arg)
	if base == "." || base == string(filepath.Separator) {
		return arg
	}
	return base
}

func splitAssignmentArg(arg string) (string, string, bool) {
	name, value, ok := strings.Cut(arg, "=")
	if !ok || name == "" {
		return "", "", false
	}
	for i, ch := range name {
		if i == 0 {
			if ch != '_' && (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') {
				return "", "", false
			}
			continue
		}
		if ch != '_' && (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return "", "", false
		}
	}
	return name, value, true
}

func normalizeEnvArgv(argv []string) []string {
	if len(argv) < 2 || commandName(argv[0]) != "env" {
		return argv
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
			i++
			continue
		}
		if envFlagNoValue(arg) {
			i++
			continue
		}
		if envFlagTakesValue(arg) {
			if i+1 >= len(argv) {
				return argv
			}
			i += 2
			continue
		}
		if envShortFlagHasInlineValue(arg) || envFlagHasInlineValue(arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return argv
		}
		break
	}
	if i >= len(argv) {
		return argv
	}
	return argv[i:]
}

func evalScript(argv []string) (string, bool) {
	if len(argv) < 2 || commandName(argv[0]) != "eval" {
		return "", false
	}
	return strings.Join(argv[1:], " "), true
}

func envSplitStringScript(argv []string) (string, bool) {
	if len(argv) < 2 || commandName(argv[0]) != "env" {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return "", false
		}
		if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "-S" || arg == "--split-string" {
			if i+1 >= len(argv) {
				return "", false
			}
			return "env " + argv[i+1] + " " + quoteLiteralArgs(argv[i+2:]), true
		}
		if strings.HasPrefix(arg, "--split-string=") {
			script := strings.TrimPrefix(arg, "--split-string=")
			if i+1 < len(argv) {
				script += " " + quoteLiteralArgs(argv[i+1:])
			}
			return "env " + script, true
		}
		if script, ok := envShortSplitStringScript(arg); ok {
			if script == "" {
				if i+1 >= len(argv) {
					return "", false
				}
				return "env " + argv[i+1] + " " + quoteLiteralArgs(argv[i+2:]), true
			}
			if i+1 < len(argv) {
				script += " " + quoteLiteralArgs(argv[i+1:])
			}
			return "env " + script, true
		}
		if envFlagNoValue(arg) {
			continue
		}
		if envFlagTakesValue(arg) {
			i++
			if i >= len(argv) {
				return "", false
			}
			continue
		}
		if envShortFlagHasInlineValue(arg) || envFlagHasInlineValue(arg) {
			continue
		}
		return "", false
	}
	return "", false
}

func quoteLiteralArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func envShortSplitStringScript(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return "", false
	}
	body := arg[1:]
	for i, ch := range body {
		switch ch {
		case 'i', 'v', '0':
			continue
		case 'S':
			return body[i+1:], true
		default:
			return "", false
		}
	}
	return "", false
}

func envFlagNoValue(arg string) bool {
	return arg == "-i" || arg == "--ignore-environment" || isEnvNoValueShortOptionGroup(arg)
}

func isEnvNoValueShortOptionGroup(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	for _, ch := range arg[1:] {
		if ch != 'i' && ch != 'v' && ch != '0' {
			return false
		}
	}
	return true
}

func envFlagTakesValue(arg string) bool {
	switch arg {
	case "-u", "--unset", "-C", "--chdir", "-P", "--path":
		return true
	default:
		return false
	}
}

func envShortFlagHasInlineValue(arg string) bool {
	if len(arg) <= 2 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	for i, ch := range arg[1:] {
		switch ch {
		case 'i', 'v', '0':
			continue
		case 'u', 'C', 'P':
			return i+2 < len(arg)
		default:
			return false
		}
	}
	return false
}

func envFlagHasInlineValue(arg string) bool {
	return strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--chdir=") || strings.HasPrefix(arg, "--path=")
}

func normalizeSudoArgv(argv []string) []string {
	if len(argv) < 2 || commandName(argv[0]) != "sudo" {
		return argv
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") {
			if _, _, ok := splitAssignmentArg(arg); ok {
				i++
				continue
			}
			break
		}
		if sudoFlagTakesValue(arg) || sudoShortFlagTakesSeparateValue(arg) {
			if i+1 >= len(argv) {
				return argv
			}
			i += 2
			continue
		}
		if sudoFlagHasInlineValue(arg) || sudoShortFlagHasInlineValue(arg) || sudoFlagNoValue(arg) {
			i++
			continue
		}
		i++
	}
	for i < len(argv) {
		if _, _, ok := splitAssignmentArg(argv[i]); ok {
			i++
			continue
		}
		break
	}
	if i >= len(argv) {
		return argv
	}
	return argv[i:]
}

func sudoEnvCanExpose(argv []string, match func(string) bool, reason string) (bool, string) {
	if len(argv) < 2 || commandName(argv[0]) != "sudo" {
		return false, ""
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if sudoPreserveEnvCanExpose(arg, match) {
			return true, reason
		}
		if sudoFlagTakesValue(arg) || sudoShortFlagTakesSeparateValue(arg) {
			if i+1 >= len(argv) {
				return false, ""
			}
			i += 2
			continue
		}
		if sudoFlagHasInlineValue(arg) || sudoShortFlagHasInlineValue(arg) || sudoFlagNoValue(arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			i++
			continue
		}
		break
	}
	for i < len(argv) {
		name, _, ok := splitAssignmentArg(argv[i])
		if !ok {
			return false, ""
		}
		if match(name) {
			return true, reason
		}
		i++
	}
	return false, ""
}

func sudoPreserveEnvCanExpose(arg string, match func(string) bool) bool {
	if arg == "-E" || arg == "--preserve-env" {
		return true
	}
	if strings.HasPrefix(arg, "--preserve-env=") {
		names := strings.TrimPrefix(arg, "--preserve-env=")
		if names == "" {
			return true
		}
		for _, name := range strings.Split(names, ",") {
			if match(strings.TrimSpace(name)) {
				return true
			}
		}
		return false
	}
	return sudoShortFlagPreservesEnv(arg)
}

func sudoShortFlagPreservesEnv(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	body := arg[1:]
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch == 'E' {
			return true
		}
		if sudoShortFlagTakesValue(ch) {
			return false
		}
	}
	return false
}

func sudoFlagNoValue(arg string) bool {
	switch arg {
	case "-A", "-B", "-b", "-E", "-e", "-H", "-i", "-K", "-k", "-l", "-n", "-P", "-S", "-s", "-V", "-v",
		"--askpass", "--background", "--bell", "--edit", "--help", "--list", "--login", "--non-interactive",
		"--preserve-env", "--reset-timestamp", "--remove-timestamp", "--set-home", "--shell", "--stdin",
		"--validate", "--version":
		return true
	default:
		return false
	}
}

func sudoFlagTakesValue(arg string) bool {
	switch arg {
	case "-C", "-c", "-D", "-g", "-h", "-p", "-R", "-r", "-T", "-t", "-U", "-u",
		"--chdir", "--chroot", "--close-from", "--command-timeout", "--group", "--host",
		"--login-class", "--other-user", "--prompt", "--role", "--type", "--user":
		return true
	default:
		return false
	}
}

func sudoFlagHasInlineValue(arg string) bool {
	for _, prefix := range []string{
		"--chdir=", "--chroot=", "--close-from=", "--command-timeout=", "--group=", "--host=",
		"--login-class=", "--other-user=", "--prompt=", "--role=", "--type=", "--user=",
		"--preserve-env=",
	} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func sudoShortFlagTakesSeparateValue(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	body := arg[1:]
	for i := 0; i < len(body); i++ {
		if sudoShortFlagTakesValue(body[i]) {
			return i == len(body)-1
		}
	}
	return false
}

func sudoShortFlagHasInlineValue(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 3 {
		return false
	}
	body := arg[1:]
	for i := 0; i < len(body); i++ {
		if sudoShortFlagTakesValue(body[i]) {
			return i < len(body)-1
		}
	}
	return false
}

func sudoShortFlagTakesValue(ch byte) bool {
	switch ch {
	case 'C', 'c', 'D', 'g', 'h', 'p', 'R', 'r', 'T', 't', 'U', 'u':
		return true
	default:
		return false
	}
}

func shellScriptArgIndex(argv []string) (int, bool) {
	if len(argv) < 3 || !isShellCommand(argv[0]) {
		return 0, false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			continue
		}
		if arg == "-c" {
			return i + 1, i+1 < len(argv)
		}
		if shellOptionTakesValue(arg) {
			i++
			if i >= len(argv) {
				return 0, false
			}
			continue
		}
		if shellShortOptionHasCommand(arg) {
			return i + 1, i+1 < len(argv)
		}
		if !strings.HasPrefix(arg, "-") {
			return 0, false
		}
	}
	return 0, false
}

func shellCanExecuteScript(argv []string) bool {
	if _, ok := shellScriptArgIndex(argv); ok {
		return true
	}
	return shellInterpreterWithoutVisibleScript(argv)
}

func shellInteractiveOption(argv []string) bool {
	if len(argv) == 0 || !isShellCommand(argv[0]) {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false
		}
		if arg == "-c" {
			return false
		}
		if shellOptionTakesValue(arg) {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		if strings.Contains(arg[1:], "i") {
			return true
		}
		if shellShortOptionHasCommand(arg) {
			return false
		}
	}
	return false
}

func shellLoginOption(argv []string) bool {
	if len(argv) == 0 || !isShellCommand(argv[0]) {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false
		}
		if arg == "-c" {
			return false
		}
		if arg == "--login" {
			return true
		}
		if shellOptionTakesValue(arg) {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		if strings.Contains(arg[1:], "l") {
			return true
		}
		if shellShortOptionHasCommand(arg) {
			return false
		}
	}
	return false
}

func wrapperLoginShellStartupCanExecuteHiddenScript(argv []string) bool {
	out := argv
	for {
		if len(out) == 0 {
			return false
		}
		switch commandName(out[0]) {
		case "command":
			next := normalizeCommandArgv(out)
			if len(next) == len(out) {
				return false
			}
			out = next
		case "builtin":
			if len(out) == 1 {
				return false
			}
			out = out[1:]
		case "exec":
			if execLoginShellOption(out) {
				return true
			}
			next := normalizeExecArgv(out)
			if len(next) == len(out) {
				return false
			}
			out = next
		case "env":
			next := normalizeEnvArgv(out)
			if len(next) == len(out) {
				return false
			}
			out = next
		case "sudo":
			if sudoLoginShellOption(out) {
				return true
			}
			next := normalizeSudoArgv(out)
			if len(next) == len(out) {
				return false
			}
			out = next
		default:
			return false
		}
	}
}

func execLoginShellOption(argv []string) bool {
	if len(argv) < 2 || commandName(argv[0]) != "exec" {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false
		}
		if arg == "-l" || execShortFlagHasLogin(arg) {
			return true
		}
		if arg == "-a" {
			if i+1 >= len(argv) {
				return false
			}
			if strings.HasPrefix(argv[i+1], "-") {
				return true
			}
			i++
			continue
		}
		if arg == "-c" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return false
		}
		return false
	}
	return false
}

func execShortFlagHasLogin(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 3 {
		return false
	}
	for _, ch := range arg[1:] {
		if ch == 'l' {
			return true
		}
		if ch != 'c' {
			return false
		}
	}
	return false
}

func sudoLoginShellOption(argv []string) bool {
	if len(argv) < 2 || commandName(argv[0]) != "sudo" {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false
		}
		if arg == "-i" || arg == "--login" || sudoShortFlagHasLogin(arg) {
			return true
		}
		if sudoFlagTakesValue(arg) || sudoShortFlagTakesSeparateValue(arg) {
			i++
			continue
		}
		if sudoFlagHasInlineValue(arg) || sudoShortFlagHasInlineValue(arg) || sudoFlagNoValue(arg) {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return false
	}
	return false
}

func sudoShortFlagHasLogin(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	body := arg[1:]
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch == 'i' {
			return true
		}
		if sudoShortFlagTakesValue(ch) {
			return false
		}
	}
	return false
}

func shellStartupFileOption(argv []string) bool {
	if len(argv) == 0 || !isShellCommand(argv[0]) {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" || arg == "-c" {
			return false
		}
		if arg == "--rcfile" || arg == "--init-file" || strings.HasPrefix(arg, "--rcfile=") || strings.HasPrefix(arg, "--init-file=") {
			return true
		}
		if shellOptionTakesValue(arg) {
			i++
			continue
		}
		if shellShortOptionHasCommand(arg) {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

func shellOptionTakesValue(arg string) bool {
	switch arg {
	case "-O", "+O", "-o", "+o", "--rcfile", "--init-file":
		return true
	default:
		return false
	}
}

func shellInterpreterWithoutVisibleScript(argv []string) bool {
	if len(argv) == 0 || !isShellCommand(argv[0]) {
		return false
	}
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if arg == "-c" {
			return false
		}
		if shellOptionTakesValue(arg) {
			if i+1 >= len(argv) {
				return true
			}
			i += 2
			continue
		}
		if shellNoScriptOption(arg) {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			if shellShortOptionHasCommand(arg) {
				return false
			}
			i++
			continue
		}
		break
	}
	return true
}

func shellNoScriptOption(arg string) bool {
	switch arg {
	case "--help", "--version", "-h", "-n":
		return true
	default:
		return false
	}
}

func shellShortOptionHasCommand(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "c")
}

func isShellCommand(cmd string) bool {
	switch commandName(cmd) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	default:
		return false
	}
}

func ShellQuote(args []string) string {
	var b bytes.Buffer
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		quoted, err := syntax.Quote(arg, syntax.LangBash)
		if err != nil {
			b.WriteString("'" + strings.ReplaceAll(arg, "'", `'\''`) + "'")
			continue
		}
		b.WriteString(quoted)
	}
	return b.String()
}

func normalizeGitGlobalOptions(argv []string) ([]string, bool, string) {
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch {
		case gitGlobalFlagNoValue(arg):
			i++
		case gitGlobalDispatchFlagHasInlineValue(arg):
			return argv, true, "git execution path can change command dispatch"
		case gitGlobalConfigFlagHasInlineDispatchKey(arg):
			return argv, true, "git config can change command dispatch"
		case gitGlobalFlagHasInlineValue(arg):
			i++
		case gitGlobalDispatchFlagTakesValue(arg):
			return argv, true, "git execution path can change command dispatch"
		case gitGlobalConfigFlagTakesDispatchKeyValue(argv, i):
			return argv, true, "git config can change command dispatch"
		case gitGlobalFlagTakesValue(arg):
			if i+1 >= len(argv) {
				return argv, false, ""
			}
			i += 2
		default:
			return argv, false, ""
		}
	}
	if i <= 1 || i >= len(argv) {
		return argv, false, ""
	}
	return append([]string{argv[0]}, argv[i:]...), false, ""
}

func gitGlobalFlagNoValue(arg string) bool {
	switch arg {
	case "-p", "-P", "--paginate", "--no-pager", "--bare", "--literal-pathspecs", "--no-literal-pathspecs",
		"--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks",
		"--no-replace-objects", "--no-lazy-fetch", "--no-advice", "--version", "--help", "--html-path",
		"--man-path", "--info-path":
		return true
	default:
		return false
	}
}

func gitGlobalDispatchFlagHasInlineValue(arg string) bool {
	return strings.HasPrefix(arg, "--exec-path=")
}

func gitGlobalConfigFlagHasInlineDispatchKey(arg string) bool {
	if !strings.HasPrefix(arg, "--config-env=") {
		return false
	}
	key, _, _ := strings.Cut(strings.TrimPrefix(arg, "--config-env="), "=")
	return gitConfigKeyCanChangeCommandDispatch(key)
}

func gitGlobalFlagHasInlineValue(arg string) bool {
	for _, prefix := range []string{
		"--git-dir=", "--work-tree=", "--namespace=", "--super-prefix=", "--config-env=", "--exec-path=",
	} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func gitGlobalDispatchFlagTakesValue(arg string) bool {
	return arg == "--exec-path"
}

func gitGlobalConfigFlagTakesDispatchKeyValue(argv []string, i int) bool {
	if i+1 >= len(argv) {
		return false
	}
	switch argv[i] {
	case "-c", "--config-env":
		key, _, _ := strings.Cut(argv[i+1], "=")
		return gitConfigKeyCanChangeCommandDispatch(key)
	default:
		return false
	}
}

func gitConfigKeyCanChangeCommandDispatch(key string) bool {
	key = strings.ToLower(key)
	return strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.")
}

func gitGlobalFlagTakesValue(arg string) bool {
	switch arg {
	case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env":
		return true
	default:
		return false
	}
}

func normalizeGhGlobalOptions(argv []string) ([]string, bool, string) {
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch {
		case ghGlobalFlagNoValue(arg):
			i++
		case ghGlobalDispatchFlagHasInlineValue(arg):
			return argv, true, "gh configuration directory can change command dispatch"
		case ghGlobalFlagHasInlineValue(arg):
			i++
		case ghGlobalShortFlagHasInlineValue(arg):
			i++
		case ghGlobalDispatchFlagTakesValue(arg):
			return argv, true, "gh configuration directory can change command dispatch"
		case ghGlobalFlagTakesValue(arg):
			if i+1 >= len(argv) {
				return argv, false, ""
			}
			i += 2
		default:
			return argv, false, ""
		}
	}
	if i <= 1 || i >= len(argv) {
		return argv, false, ""
	}
	return append([]string{argv[0]}, argv[i:]...), false, ""
}

func ghGlobalFlagNoValue(arg string) bool {
	switch arg {
	case "--paginate", "--help", "-h", "--version":
		return true
	default:
		return false
	}
}

func ghGlobalDispatchFlagHasInlineValue(arg string) bool {
	return strings.HasPrefix(arg, "--config-dir=")
}

func ghGlobalFlagHasInlineValue(arg string) bool {
	for _, prefix := range []string{"--repo=", "--hostname=", "--config-dir=", "--git-protocol=", "--editor=", "--browser="} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func ghGlobalShortFlagHasInlineValue(arg string) bool {
	return strings.HasPrefix(arg, "-R") && len(arg) > len("-R")
}

func ghGlobalDispatchFlagTakesValue(arg string) bool {
	return arg == "--config-dir"
}

func ghGlobalFlagTakesValue(arg string) bool {
	switch arg {
	case "-R", "--repo", "--hostname", "--config-dir", "--git-protocol", "--editor", "--browser":
		return true
	default:
		return false
	}
}
