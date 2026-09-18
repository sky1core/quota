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
	command        parsedCommand
	literalArgv    []string
	source         string
	sourceID       int
	sourceStart    int
	sourceEnd      int
}

func ParseShellInvocations(command string) ([]Invocation, error) {
	state := shellParseState{}
	return state.parseShellInvocations(command, 0, "")
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

type shellParseState struct {
	nextSourceID int
}

func (state *shellParseState) parseShellInvocations(command string, depth int, inheritedShellStartup string) ([]Invocation, error) {
	if depth > 8 {
		return nil, fmt.Errorf("nested shell command depth exceeded")
	}
	sourceID := state.nextSourceID
	state.nextSourceID++
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, err
	}
	var invocations []Invocation
	syntax.Walk(file, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok {
			if decl, ok := stmt.Cmd.(*syntax.DeclClause); ok {
				if shellStartupDeclCanExecuteHiddenScript(decl) {
					invocations = append(invocations, undecidableInvocation([]string{decl.Variant.Value}, shellStartupEnvReason))
				} else {
					invocations = append(invocations, sourceInvocation(command, sourceID, stmt))
				}
				return true
			}
			if stmt.Cmd == nil || sourceOnlyStatementCommand(stmt.Cmd) {
				invocations = append(invocations, sourceInvocation(command, sourceID, stmt))
			}
			return true
		}
		inv := callInvocation(call)
		if len(inv.Argv) == 0 && !inv.Dynamic {
			if len(call.Assigns) > 0 {
				setInvocationSource(&inv, command, sourceID, stmt)
				invocations = append(invocations, inv)
			}
			return true
		}
		wrappers := parseWrapperChain(inv.Argv)
		if wrappers.undecidable != "" {
			invocations = append(invocations, undecidableInvocation(wrappers.argv, wrappers.undecidable))
			return true
		}
		gitConfigDispatch, gitConfigReason := gitConfigAssignmentCanChangeCommandDispatch(call.Assigns)
		if wrappers.gitEnvironment {
			gitConfigDispatch, gitConfigReason = true, "git environment can change command dispatch"
		}
		ghConfigDispatch, ghConfigReason := ghConfigAssignmentCanChangeCommandDispatch(call.Assigns)
		if wrappers.ghEnvironment {
			ghConfigDispatch, ghConfigReason = true, "gh configuration directory can change command dispatch"
		}
		if inv.Dynamic && len(inv.Argv) > 0 && isCommandWrapper(inv.Argv[0]) {
			inv.DynamicCommand = true
		}
		shellStartupDispatch, shellStartupReason := shellStartupAssignmentCanExecuteHiddenScript(call.Assigns)
		if wrappers.shellEnvironment {
			shellStartupDispatch, shellStartupReason = true, "shell startup environment can execute hidden script content"
		}
		if inheritedShellStartup != "" {
			shellStartupDispatch, shellStartupReason = true, inheritedShellStartup
		}
		parsed := parseCommand(wrappers.argv)
		norm := parsed.argv
		dynamicCommand, dynamicReason := parsed.dynamic, parsed.undecidable
		inv.command = parsed
		inv.literalArgv = literalCommandArgv(call, wrappers)
		setInvocationSource(&inv, command, sourceID, stmt)
		if shellSetCanExposeFutureStartupEnv(norm, inv.Dynamic) {
			invocations = append(invocations, undecidableInvocation(norm, shellStartupEnvReason))
			return true
		}
		if wrappers.sameShell && shellStartupBuiltinCallCanExecuteHiddenScript(norm, inv.Dynamic) {
			invocations = append(invocations, undecidableInvocation(norm, shellStartupEnvReason))
			return true
		}
		if gitConfigDispatch && len(norm) > 0 && commandName(norm[0]) == "git" {
			dynamicCommand, dynamicReason = true, gitConfigReason
		}
		if ghConfigDispatch && len(norm) > 0 && commandName(norm[0]) == "gh" {
			dynamicCommand, dynamicReason = true, ghConfigReason
		}
		script, hasScript := wrappers.script, wrappers.hasScript
		if len(norm) > 0 && isShellCommand(norm[0]) {
			shell := parseShellInterpreter(norm)
			canExecute := shell.canExecute()
			hidden := ""
			switch {
			case shell.undecidable != "":
				hidden = shell.undecidable
			case !canExecute:
			case shellStartupDispatch:
				hidden = shellStartupReason
			case shell.interactive:
				hidden = "interactive shell startup files can execute hidden script content"
			case shell.login:
				hidden = "login shell startup files can execute hidden script content"
			case wrappers.loginShell:
				hidden = "wrapper login shell startup files can execute hidden script content"
			case shell.startupFile:
				hidden = "shell startup file can execute hidden script content"
			case shell.hiddenScript():
				hidden = "shell interpreter script is not visible to policy evaluator"
			}
			if hidden != "" {
				invocations = append(invocations, undecidableInvocation(norm, hidden))
				return true
			}
			if canExecute {
				script, hasScript = shell.command, shell.hasCommand
			} else {
				script, hasScript = "", false
			}
		}
		if hasScript && !inv.Dynamic {
			nested, err := state.parseShellInvocations(script, depth+1, shellStartupReason)
			if err != nil {
				invocations = append(invocations, undecidableInvocation(norm, "nested command: "+err.Error()))
				return true
			}
			nested = markInvocationsDynamicForCommand(nested, "git", gitConfigDispatch, gitConfigReason)
			nested = markInvocationsDynamicForCommand(nested, "gh", ghConfigDispatch, ghConfigReason)
			invocations = append(invocations, nested...)
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

func sourceInvocation(source string, sourceID int, node syntax.Node) Invocation {
	inv := Invocation{}
	setInvocationSource(&inv, source, sourceID, node)
	return inv
}

func setInvocationSource(inv *Invocation, source string, sourceID int, node syntax.Node) {
	text, start, end := nodeSource(source, node)
	inv.source = text
	inv.sourceID = sourceID
	inv.sourceStart = start
	inv.sourceEnd = end
}

func sourceOnlyStatementCommand(cmd syntax.Command) bool {
	switch cmd.(type) {
	case *syntax.ArithmCmd, *syntax.CaseClause, *syntax.ForClause, *syntax.TestClause, *syntax.LetClause:
		return true
	default:
		return false
	}
}

func nodeSource(source string, node syntax.Node) (string, int, int) {
	start, end := int(node.Pos().Offset()), int(node.End().Offset())
	if start < 0 || end < start || end > len(source) {
		return "", 0, 0
	}
	return source[start:end], start, end
}

func undecidableInvocation(argv []string, reason string) Invocation {
	return Invocation{Argv: argv, Dynamic: true, DynamicCommand: true, DynamicReason: reason}
}

func literalCommandArgv(call *syntax.CallExpr, wrappers wrapperChain) []string {
	if wrappers.undecidable != "" || wrappers.hasScript || len(wrappers.argv) == 0 {
		return nil
	}
	start := len(call.Args) - len(wrappers.argv)
	var argv []string
	for i, word := range call.Args {
		value, literal := staticWord(word)
		if i >= start {
			if !literal {
				return nil
			}
			argv = append(argv, value)
		} else if !literal && !quotedScalarWord(word) {
			return nil
		}
	}
	return argv
}

func quotedScalarWord(word *syntax.Word) bool {
	if len(word.Parts) != 1 {
		return false
	}
	quoted, ok := word.Parts[0].(*syntax.DblQuoted)
	if !ok {
		return false
	}
	for _, part := range quoted.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
		case *syntax.ParamExp:
			if p.Param == nil || p.Param.Value == "@" || p.Index != nil || p.Excl || p.Names != 0 || p.Slice != nil || p.Repl != nil || p.Exp != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
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

const shellStartupEnvReason = "shell startup environment can execute hidden script content"

func shellStartupAssignmentCanExecuteHiddenScript(assigns []*syntax.Assign) (bool, string) {
	for _, assign := range assigns {
		if assign.Name == nil {
			continue
		}
		if shellStartupEnvName(assign.Name.Value) {
			return true, shellStartupEnvReason
		}
	}
	return false, ""
}

func shellStartupDeclCanExecuteHiddenScript(decl *syntax.DeclClause) bool {
	if decl == nil || decl.Variant == nil {
		return false
	}
	args := make([]shellStartupDeclArg, 0, len(decl.Args))
	for _, arg := range decl.Args {
		token, ok := declArgToken(arg)
		view := shellStartupDeclArg{token: token, ok: ok}
		if arg != nil && arg.Name != nil {
			view.name, view.hasName = arg.Name.Value, true
		}
		args = append(args, view)
	}
	return shellStartupDeclArgsCanExecuteHiddenScript(decl.Variant.Value, args)
}

func shellStartupBuiltinCallCanExecuteHiddenScript(argv []string, dynamic bool) bool {
	if len(argv) == 0 {
		return false
	}
	variant := commandName(argv[0])
	if !declVariantCanExport(variant) {
		return false
	}
	if dynamic {
		return true
	}
	args := make([]shellStartupDeclArg, 0, len(argv)-1)
	for _, arg := range argv[1:] {
		view := shellStartupDeclArg{token: arg, ok: true}
		if name := shellStartupDeclArgName(arg); name != "" {
			view.name, view.hasName = name, true
		}
		args = append(args, view)
	}
	return shellStartupDeclArgsCanExecuteHiddenScript(variant, args)
}

type shellStartupDeclArg struct {
	token   string
	ok      bool
	name    string
	hasName bool
}

func shellStartupDeclArgsCanExecuteHiddenScript(variant string, args []shellStartupDeclArg) bool {
	exports := variant == "export"
	functionMode := false
	nameRefMode := false
	dynamicOpt := false
	parsingOptions := true
	for _, arg := range args {
		if !arg.ok {
			if declVariantCanExport(variant) || nameRefMode {
				return true
			}
			continue
		}
		if parsingOptions && arg.token == "--" {
			parsingOptions = false
			continue
		}
		if parsingOptions && declOptionToken(arg.token) {
			if applyDeclOption(variant, arg.token, &exports, &functionMode, &nameRefMode) {
				dynamicOpt = true
			}
			continue
		}
		parsingOptions = false
		if functionMode {
			continue
		}
		if nameRefMode {
			return true
		}
		if !arg.hasName {
			if exports || dynamicOpt {
				return true
			}
			continue
		}
		if shellStartupEnvName(arg.name) && (exports || dynamicOpt) {
			return true
		}
	}
	return false
}

func declVariantCanExport(variant string) bool {
	switch variant {
	case "export", "declare", "typeset", "readonly", "local":
		return true
	default:
		return false
	}
}

func declArgToken(arg *syntax.Assign) (string, bool) {
	if arg == nil {
		return "", false
	}
	if arg.Name != nil {
		return arg.Name.Value, true
	}
	if arg.Value == nil {
		return "", false
	}
	return staticWord(arg.Value)
}

func shellStartupDeclArgName(token string) string {
	if name, _, ok := strings.Cut(token, "="); ok {
		return strings.TrimSuffix(name, "+")
	}
	return token
}

func declOptionToken(token string) bool {
	return len(token) >= 2 && (token[0] == '-' || token[0] == '+')
}

func applyDeclOption(variant, opt string, exports, functionMode, nameRefMode *bool) bool {
	dynamic := false
	if opt == "--" {
		return false
	}
	if len(opt) < 2 || (opt[0] != '-' && opt[0] != '+') {
		return false
	}
	for _, ch := range opt[1:] {
		switch ch {
		case 'x':
			*exports = opt[0] == '-'
		case 'n':
			if variant == "export" && opt[0] == '-' {
				*exports = false
			} else if opt[0] == '-' {
				*nameRefMode = true
			}
		case 'f':
			if variant == "export" && opt[0] == '-' {
				*functionMode = true
			}
		default:
			if opt[0] == '-' || opt[0] == '+' {
				dynamic = true
			}
		}
	}
	return dynamic
}

func shellSetCanExposeFutureStartupEnv(argv []string, dynamic bool) bool {
	if len(argv) == 0 || commandName(argv[0]) != "set" {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if dynamic && arg == "" {
			return true
		}
		switch {
		case arg == "--":
			return false
		case arg == "-o":
			if i+1 >= len(argv) {
				return false
			}
			if dynamic && argv[i+1] == "" {
				return true
			}
			if argv[i+1] == "allexport" {
				return true
			}
			i++
		case arg == "+o":
			if i+1 >= len(argv) {
				return false
			}
			if dynamic && argv[i+1] == "" {
				return true
			}
			if strings.HasPrefix(argv[i+1], "-") || strings.HasPrefix(argv[i+1], "+") {
				continue
			}
			i++
		case strings.HasPrefix(arg, "-") && arg != "-":
			body := arg[1:]
			for j := 0; j < len(body); j++ {
				switch body[j] {
				case 'a':
					return true
				case 'o':
					if j+1 < len(body) {
						return true
					}
					if i+1 >= len(argv) {
						return false
					}
					if dynamic && argv[i+1] == "" {
						return true
					}
					if argv[i+1] == "allexport" {
						return true
					}
					i++
				}
			}
		case strings.HasPrefix(arg, "+") && arg != "+":
			body := arg[1:]
			for j := 0; j < len(body); j++ {
				if body[j] == 'o' {
					if j+1 < len(body) {
						break
					}
					if i+1 >= len(argv) {
						return false
					}
					if dynamic && argv[i+1] == "" {
						return true
					}
					if strings.HasPrefix(argv[i+1], "-") || strings.HasPrefix(argv[i+1], "+") {
						break
					}
					i++
					break
				}
			}
		default:
			return false
		}
	}
	return false
}

// wrapperParse is the single interpretation of one wrapper layer. Option
// grammar for each family lives only in its parse function; protection checks
// read these fields and never re-walk the arguments.
type wrapperParse struct {
	rest        []string
	undecidable string
	loginShell  bool // the wrapped program starts as a login shell
	assigns     []assignment
	preserveAll bool     // sudo keeps the whole caller environment
	preserve    []string // sudo keeps these named variables
	script      string   // eval / env -S script text
	hasScript   bool
}

type assignment struct{ name, value string }

func undecidableWrapper(family, arg string) wrapperParse {
	return wrapperParse{undecidable: "unsupported or incomplete " + family + " option " + arg}
}

func wrapperCommand(p wrapperParse, argv []string, i int) wrapperParse {
	if i < len(argv) {
		p.rest = argv[i:]
	}
	return p
}

func parseWrapper(argv []string) (wrapperParse, bool) {
	if len(argv) == 0 {
		return wrapperParse{}, false
	}
	switch commandName(argv[0]) {
	case "command":
		return parseCommandWrapper(argv), true
	case "builtin":
		return parseBuiltinWrapper(argv), true
	case "exec":
		return parseExecWrapper(argv), true
	case "env":
		return parseEnvWrapper(argv), true
	case "sudo":
		return parseSudoWrapper(argv), true
	case "nohup", "nice", "timeout":
		return parseProcessWrapper(argv), true
	case "eval":
		return parseEvalWrapper(argv), true
	}
	return wrapperParse{}, false
}

type wrapperChain struct {
	argv             []string
	undecidable      string
	loginShell       bool
	gitEnvironment   bool
	ghEnvironment    bool
	shellEnvironment bool
	sameShell        bool
	script           string
	hasScript        bool
}

func parseWrapperChain(argv []string) wrapperChain {
	chain := wrapperChain{argv: argv, sameShell: true}
	for {
		wrapperName := ""
		if len(chain.argv) > 0 {
			wrapperName = commandName(chain.argv[0])
		}
		p, ok := parseWrapper(chain.argv)
		if !ok {
			return chain
		}
		switch wrapperName {
		case "command", "builtin":
		case "eval":
		default:
			chain.sameShell = false
		}
		if p.undecidable != "" {
			chain.undecidable = p.undecidable
			if p.rest != nil {
				chain.argv = p.rest
			}
			return chain
		}
		chain.loginShell = chain.loginShell || p.loginShell
		chain.gitEnvironment = chain.gitEnvironment || p.exposesEnv(gitConfigEnvNameCanChangeCommandDispatch, gitConfigEnvPairCanChangeCommandDispatch)
		chain.ghEnvironment = chain.ghEnvironment || p.exposesEnv(assignmentNameCanChangeGhCommandDispatch, func(name, _ string) bool { return assignmentNameCanChangeGhCommandDispatch(name) })
		chain.shellEnvironment = chain.shellEnvironment || p.exposesEnv(shellStartupEnvName, func(name, _ string) bool { return shellStartupEnvName(name) })
		if p.hasScript {
			chain.script, chain.hasScript = p.script, true
			return chain
		}
		if p.rest == nil {
			return chain
		}
		chain.argv = p.rest
	}
}

func (p wrapperParse) exposesEnv(name func(string) bool, pair func(string, string) bool) bool {
	if p.preserveAll {
		return true
	}
	for _, n := range p.preserve {
		if name(n) {
			return true
		}
	}
	for _, a := range p.assigns {
		if pair(a.name, a.value) {
			return true
		}
	}
	return false
}

func shellStartupEnvName(name string) bool {
	switch name {
	case "BASH_ENV", "ENV", "ZDOTDIR":
		return true
	default:
		return false
	}
}

func gitConfigEnvNameCanChangeCommandDispatch(name string) bool {
	return gitConfigEnvPairCanChangeCommandDispatch(name, "") || strings.HasPrefix(name, "GIT_CONFIG_KEY_")
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

func isCommandWrapper(cmd string) bool {
	switch commandName(cmd) {
	case "command", "builtin", "exec", "env", "sudo", "nohup", "nice", "timeout", "eval", "sh", "bash", "zsh", "dash", "ksh":
		return true
	default:
		return false
	}
}

// command [-p] [--] name ...; -v/-V describe instead of running.
func parseCommandWrapper(argv []string) wrapperParse {
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
			return wrapperParse{}
		}
		if strings.HasPrefix(arg, "-") {
			return undecidableWrapper("command", arg)
		}
		break
	}
	return wrapperCommand(wrapperParse{}, argv, i)
}

// builtin [--] name ...; it takes no options besides the end marker.
func parseBuiltinWrapper(argv []string) wrapperParse {
	i := 1
	if len(argv) > i && argv[i] == "--" {
		i++
	}
	if len(argv) > i && strings.HasPrefix(argv[i], "-") {
		return undecidableWrapper("builtin", argv[i])
	}
	return wrapperCommand(wrapperParse{}, argv, i)
}

// exec [-cl] [-a name] [--] command ...; short options combine and -a takes
// the rest of its group or the next argument. A name starting with "-" makes
// the program start as a login shell.
func parseExecWrapper(argv []string) wrapperParse {
	var p wrapperParse
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
		if len(arg) < 2 || strings.HasPrefix(arg, "--") {
			return undecidableWrapper("exec", arg)
		}
		body := arg[1:]
		for j := 0; j < len(body); j++ {
			switch body[j] {
			case 'c':
			case 'l':
				p.loginShell = true
			case 'a':
				name := body[j+1:]
				if name == "" {
					i++
					if i >= len(argv) {
						return undecidableWrapper("exec", arg)
					}
					name = argv[i]
				}
				if strings.HasPrefix(name, "-") {
					p.loginShell = true
				}
				j = len(body)
			default:
				return undecidableWrapper("exec", arg)
			}
		}
		i++
	}
	return wrapperCommand(p, argv, i)
}

// env [-iv0] [-u NAME] [-C DIR] [-P PATH] [-S STRING] [--] [NAME=VALUE ...]
// [command ...]. Option parsing stops at the first NAME=VALUE; every later
// argument containing "=" is an assignment.
func parseEnvWrapper(argv []string) wrapperParse {
	var p wrapperParse
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			if i < len(argv) && argv[i] == "-" {
				i++
			}
			break
		}
		if arg == "--help" || arg == "--version" {
			return wrapperParse{}
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if arg == "-" || arg == "--ignore-environment" {
			i++
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--split-string="); ok {
			return envSplitString(value, argv[i+1:])
		}
		switch arg {
		case "--unset", "--chdir", "--path":
			if i+1 >= len(argv) {
				return undecidableWrapper("env", arg)
			}
			i += 2
			continue
		case "--split-string":
			if i+1 >= len(argv) {
				return undecidableWrapper("env", arg)
			}
			return envSplitString(argv[i+1], argv[i+2:])
		}
		if strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--chdir=") || strings.HasPrefix(arg, "--path=") {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			return undecidableWrapper("env", arg)
		}
		body := arg[1:]
		consumed := false
		for j := 0; j < len(body) && !consumed; j++ {
			switch body[j] {
			case 'i', 'v', '0':
			case 'u', 'C', 'P':
				if j+1 < len(body) {
					consumed = true
					break
				}
				if i+1 >= len(argv) {
					return undecidableWrapper("env", arg)
				}
				i++
				consumed = true
			case 'S':
				if j+1 < len(body) {
					return envSplitString(body[j+1:], argv[i+1:])
				}
				if i+1 >= len(argv) {
					return undecidableWrapper("env", arg)
				}
				return envSplitString(argv[i+1], argv[i+2:])
			default:
				return undecidableWrapper("env", arg)
			}
		}
		i++
	}
	for i < len(argv) {
		name, value, ok := strings.Cut(argv[i], "=")
		if !ok {
			break
		}
		p.assigns = append(p.assigns, assignment{name, value})
		i++
	}
	return wrapperCommand(p, argv, i)
}

func envSplitString(value string, rest []string) wrapperParse {
	script := "env " + value
	if len(rest) > 0 {
		script += " " + quoteLiteralArgs(rest)
	}
	return wrapperParse{script: script, hasScript: true}
}

func quoteLiteralArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

const (
	sudoShortNoValue = "ABbEeHiKklnPSsVv"
	sudoShortValue   = "CcDghpRrTtUu"
)

var sudoLongNoValue = map[string]bool{
	"--askpass": true, "--background": true, "--bell": true, "--edit": true, "--help": true, "--list": true,
	"--login": true, "--non-interactive": true, "--preserve-env": true, "--reset-timestamp": true,
	"--remove-timestamp": true, "--set-home": true, "--shell": true, "--stdin": true, "--validate": true,
	"--version": true,
}

var sudoLongValue = map[string]bool{
	"--chdir": true, "--chroot": true, "--close-from": true, "--command-timeout": true, "--group": true,
	"--host": true, "--login-class": true, "--other-user": true, "--prompt": true, "--role": true,
	"--type": true, "--user": true,
}

// sudo [options] [NAME=VALUE ...] [--] [command ...]; short options combine
// and a value option takes the rest of its group or the next argument.
func parseSudoWrapper(argv []string) wrapperParse {
	var p wrapperParse
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") {
			name, value, ok := splitAssignmentArg(arg)
			if !ok {
				break
			}
			p.assigns = append(p.assigns, assignment{name, value})
			i++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg, "=")
			switch {
			case name == "--preserve-env" && attached:
				if value == "" {
					p.preserveAll = true
				}
				for _, n := range strings.Split(value, ",") {
					p.preserve = append(p.preserve, strings.TrimSpace(n))
				}
			case name == "--preserve-env":
				p.preserveAll = true
			case (name == "--login" || name == "--shell") && !attached:
				p.undecidable = "sudo shell mode can execute hidden script content"
			case sudoLongNoValue[name] && !attached:
			case sudoLongValue[name] && attached:
			case sudoLongValue[name]:
				if i+1 >= len(argv) {
					return undecidableWrapper("sudo", arg)
				}
				i++
			default:
				return undecidableWrapper("sudo", arg)
			}
			i++
			continue
		}
		body := arg[1:]
		if body == "" {
			return undecidableWrapper("sudo", arg)
		}
		for j := 0; j < len(body); j++ {
			ch := body[j]
			switch {
			case ch == 'E':
				p.preserveAll = true
			case ch == 'i' || ch == 's':
				p.undecidable = "sudo shell mode can execute hidden script content"
			case ch == 'h' && j+1 == len(body) && (i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-")):
				return wrapperParse{}
			case strings.IndexByte(sudoShortNoValue, ch) >= 0:
			case strings.IndexByte(sudoShortValue, ch) >= 0:
				if j+1 == len(body) {
					if i+1 >= len(argv) {
						return undecidableWrapper("sudo", arg)
					}
					i++
				}
				j = len(body)
			default:
				return undecidableWrapper("sudo", arg)
			}
		}
		i++
	}
	for i < len(argv) {
		name, value, ok := splitAssignmentArg(argv[i])
		if !ok {
			break
		}
		p.assigns = append(p.assigns, assignment{name, value})
		i++
	}
	return wrapperCommand(p, argv, i)
}

func parseProcessWrapper(argv []string) wrapperParse {
	next, err := normalizeProcessWrapperArgv(argv)
	if err != nil {
		return wrapperParse{undecidable: err.Error()}
	}
	if len(next) == len(argv) {
		return wrapperParse{}
	}
	return wrapperParse{rest: next}
}

func parseEvalWrapper(argv []string) wrapperParse {
	i := 1
	if i < len(argv) && argv[i] == "--" {
		i++
	} else if i < len(argv) && strings.HasPrefix(argv[i], "-") {
		return undecidableWrapper("eval", argv[i])
	}
	if i == len(argv) {
		return wrapperParse{}
	}
	return wrapperParse{script: strings.Join(argv[i:], " "), hasScript: true}
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

// shellParse is the single interpretation of a shell interpreter invocation.
type shellParse struct {
	undecidable string
	hasCommand  bool   // -c seen
	command     string // the -c script text
	interactive bool
	login       bool
	startupFile bool // --rcfile / --init-file
	noScript    bool // --help or --version
}

// canExecute reports whether the shell may run script content at all.
func (s shellParse) canExecute() bool { return s.hasCommand || !s.noScript }

// hiddenScript reports whether the shell runs content the evaluator cannot see.
func (s shellParse) hiddenScript() bool { return !s.hasCommand && !s.noScript }

const shellShortLetters = "abefhkmnprstuvxBCDEHIPT"

var shellLongNoValue = map[string]bool{
	"--debugger": true, "--dump-po-strings": true, "--dump-strings": true, "--noediting": true,
	"--noprofile": true, "--norc": true, "--posix": true, "--pretty-print": true, "--restricted": true,
	"--verbose": true,
}

// sh|bash|zsh|dash|ksh [options] [-c string | file] ...; short options combine
// with either "-" or "+", -o/+o/-O/+O take the next argument.
func parseShellInterpreter(argv []string) shellParse {
	var s shellParse
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" || arg == "-" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "+") {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			switch {
			case (name == "--rcfile" || name == "--init-file") && attached:
				s.startupFile = true
			case name == "--rcfile" || name == "--init-file":
				if i+1 >= len(argv) {
					s.undecidable = "unsupported or incomplete shell option " + arg
					return s
				}
				s.startupFile = true
				i++
			case name == "--login" && !attached:
				s.login = true
			case (name == "--help" || name == "--version") && !attached:
				s.noScript = true
			case shellLongNoValue[name] && !attached:
			default:
				s.undecidable = "unsupported or incomplete shell option " + arg
				return s
			}
			i++
			continue
		}
		body := arg[1:]
		if body == "" {
			s.undecidable = "unsupported or incomplete shell option " + arg
			return s
		}
		for j := 0; j < len(body); j++ {
			ch := body[j]
			switch {
			case ch == 'c':
				s.hasCommand = true
			case ch == 'i':
				s.interactive = arg[0] == '-'
			case ch == 'l':
				s.login = true
			case ch == 'o' || ch == 'O':
				if j+1 != len(body) || i+1 >= len(argv) {
					s.undecidable = "unsupported or incomplete shell option " + arg
					return s
				}
				i++
			case strings.IndexByte(shellShortLetters, ch) < 0:
				s.undecidable = "unsupported or incomplete shell option " + arg
				return s
			}
		}
		i++
	}
	if s.hasCommand {
		if i >= len(argv) {
			s.undecidable = "shell command option requires script text"
			return s
		}
		s.command = argv[i]
	}
	return s
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
				return argv, true, "git global option requires a value"
			}
			i += 2
		default:
			return argv, true, "unsupported git global option"
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
		"--no-replace-objects", "--no-lazy-fetch", "--no-advice", "-v", "--version", "-h", "--help", "--html-path",
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
		"--git-dir=", "--work-tree=", "--namespace=", "--super-prefix=", "--config-env=", "--exec-path=", "--attr-source=",
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
	case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env", "--attr-source":
		return true
	default:
		return false
	}
}
