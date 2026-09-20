package agenthooks

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

var ghLeadingOptions = optionValues(
	optionGroup{optionBooleanValue, `--paginate --help -h --version`},
	optionGroup{optionRequiredValue, `-R --repo --hostname --git-protocol --editor --browser`},
)

func parseGhCommand(argv []string) (parsedCommand, error) {
	return parseGhCommandInput(commandInput{argv: argv})
}

func parseGhCommandInput(input commandInput) (parsedCommand, error) {
	argv := input.argv
	args := append([]string(nil), argv[1:]...)
	indices := make([]int, len(args))
	for i := range indices {
		indices[i] = i + 1
	}
	var command []string
	firstCommand := len(argv)
	for {
		path := strings.Join(command, " ")
		index := ghCommandIndex(args, ghOptionsWithImplicitHelp(ghCommandOptions[path]))
		if index < 0 {
			break
		}
		child := args[index]
		if path != "" {
			child = path + " " + child
		}
		if _, ok := ghCommandOptions[child]; !ok || args[index] == "" || strings.ContainsAny(args[index], " \t\r\n") {
			if len(command) == 0 || ghCommandHasChildren(command) {
				return parsedCommand{}, fmt.Errorf("unsupported gh command %q", child)
			}
			break
		}
		if input.dynamicAt(indices[index]) {
			return parsedCommand{}, fmt.Errorf("gh command cannot be determined")
		}
		if len(command) == 0 {
			firstCommand = indices[index]
		}
		command = append(command, args[index])
		args = append(args[:index], args[index+1:]...)
		indices = append(indices[:index], indices[index+1:]...)
	}
	parseInput := commandInput{argv: append([]string(nil), args...), dynamicArgs: make([]bool, len(args)), splitArgs: make([]bool, len(args)), literalPrefix: make([]bool, len(args))}
	for i, index := range indices {
		parseInput.dynamicArgs[i] = input.dynamicAt(index)
		parseInput.splitArgs[i] = input.maySplitAt(index)
		parseInput.literalPrefix[i] = input.literalPrefixAt(index)
	}
	leadingEnd := 1
	for leadingEnd < firstCommand {
		end, ok := ghOptionEnd(argv, leadingEnd, ghLeadingOptions)
		if !ok || end > firstCommand {
			break
		}
		leadingEnd = end
	}
	if len(command) > 0 {
		kept := args[:0]
		keptIndices := indices[:0]
		for i, arg := range args {
			if indices[i] >= leadingEnd {
				kept = append(kept, arg)
				keptIndices = append(keptIndices, indices[i])
			}
		}
		args = kept
		indices = keptIndices
	}
	path := strings.Join(command, " ")
	out := append([]string{"gh"}, command...)
	out = append(out, args...)
	parsed := parsedCommand{argv: out}
	if path == "extension exec" {
		parsed.flags = literalCommandFlags(args)
		parsed.undecidable = "gh extension execution content is not statically available"
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			parsed.undecidable = ""
		}
		return parsed, nil
	}
	options := maps.Clone(ghLeadingOptions)
	maps.Copy(options, ghCommandOptions[path])
	if _, hasShorthand := options["-h"]; !hasShorthand {
		options["-h"] = optionBooleanValue
	}
	parsedArgs, err := parseCommandArgumentInput("gh "+path, parseInput, options, false, false)
	if err != nil {
		if ghUnconditionalDataCommand(path) && strings.Contains(err.Error(), "unsupported") {
			parsed.allowDynamicArgs = true
			parsed.flagError = err.Error()
			return parsed, nil
		}
		return parsedCommand{}, err
	}
	if parsedArgs.dynamicOptionSyntax {
		parsed.flags = parsedArgs.flags
		parsed.flagsUncertain = true
		parsed.undecidable = "gh options cannot be determined"
		return parsed, nil
	}
	if parsedArgs.dynamicOptions && !ghUnconditionalDataCommand(path) {
		parsed.flags = parsedArgs.flags
		parsed.flagsUncertain = true
		parsed.undecidable = "gh arguments cannot be determined"
		return parsed, nil
	}
	if len(command) == 0 || ghCommandHasChildren(command) {
		for i := 0; i < len(args); {
			if args[i] == "--" && i+1 == len(args) {
				break
			}
			end, ok := ghOptionEnd(args, i, options)
			if !ok {
				return parsedCommand{}, fmt.Errorf("unsupported gh command arguments")
			}
			i = end
		}
	}
	parsed.flags = parsedArgs.flags
	parsed.flagsUncertain = parsedArgs.dynamicOptions || parsedArgs.dynamicOptionSyntax
	classifyGhCommand(&parsed, path, parsedArgs.operands, options)
	return parsed, nil
}

func ghOptionsWithImplicitHelp(options map[string]optionValue) map[string]optionValue {
	if _, ok := options["-h"]; ok {
		return options
	}
	withHelp := maps.Clone(options)
	withHelp["-h"] = optionBooleanValue
	return withHelp
}

func ghCommandIndex(args []string, options map[string]optionValue) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && (strings.HasPrefix(arg, "--") || len(arg) == 2) {
				value, known := options[arg]
				if !known || value == optionRequiredValue || value == optionRequiredNonEmptyValue {
					i++
				}
			}
		} else {
			return i
		}
	}
	return -1
}

func ghCommandHasChildren(command []string) bool {
	prefix := strings.Join(command, " ") + " "
	for path := range ghCommandOptions {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func ghOptionEnd(argv []string, start int, options map[string]optionValue) (int, bool) {
	arg := argv[start]
	end := start + 1
	if arg == "-" || !strings.HasPrefix(arg, "-") {
		return end, false
	}
	if strings.HasPrefix(arg, "--") {
		name, _, attached := strings.Cut(arg, "=")
		value, ok := options[name]
		if !ok {
			return end, false
		}
		if (value == optionRequiredValue || value == optionRequiredNonEmptyValue) && !attached {
			end++
		}
	} else {
		for j := 1; j < len(arg); j++ {
			value, ok := options["-"+string(arg[j])]
			if !ok {
				return end, false
			}
			if value == optionRequiredValue || value == optionRequiredNonEmptyValue {
				if j == len(arg)-1 {
					end++
				}
				break
			}
			if value == optionAttachedValue || j+1 < len(arg) && arg[j+1] == '=' {
				break
			}
		}
	}
	if end > len(argv) {
		return end, false
	}
	_, err := parseCommandFlags("gh", argv[start:end], options, false)
	return end, err == nil
}

func ghFlagValue(flags []commandFlag, names ...string) (string, bool) {
	value, present, _ := ghFlagInfo(flags, names...)
	return value, present
}

func ghFlagInfo(flags []commandFlag, names ...string) (string, bool, bool) {
	for i := len(flags) - 1; i >= 0; i-- {
		if slices.Contains(names, flags[i].name) {
			return flags[i].value, true, flags[i].valueDynamic
		}
	}
	return "", false, false
}

func ghFlagEnabled(flags []commandFlag, names ...string) bool {
	value, present, dynamic := ghFlagInfo(flags, names...)
	return present && !dynamic && value == "true"
}

func ghFlagDynamic(flags []commandFlag, names ...string) bool {
	for i := len(flags) - 1; i >= 0; i-- {
		if slices.Contains(names, flags[i].name) && flags[i].valueDynamic {
			return true
		}
	}
	return false
}

func classifyGhCommand(parsed *parsedCommand, path string, positionals commandInput, options map[string]optionValue) {
	enabled := func(names ...string) bool { return ghFlagEnabled(parsed.flags, names...) }
	dynamic := func(names ...string) bool { return ghFlagDynamic(parsed.flags, names...) }
	value := func(names ...string) string { v, _ := ghFlagValue(parsed.flags, names...); return v }
	helpNames := []string{"--help"}
	if options["-h"] == optionBooleanValue {
		helpNames = append(helpNames, "-h")
	}
	if enabled(helpNames...) || path == "help" || path == "" || ghCommandHasChildren(strings.Fields(path)) {
		parsed.allowDynamicArgs = true
		return
	}
	remote := false
	switch path {
	case "api":
		classifyGhAPI(parsed, positionals)
		return
	case "pr merge":
		remote = dynamic("--disable-auto", "--auto") || !enabled("--disable-auto") || enabled("--auto")
	case "pr close":
		remote = dynamic("-d", "--delete-branch") || enabled("-d", "--delete-branch")
	case "pr create", "pr new":
		remote = value("-H", "--head") == ""
	case "issue develop":
		remote = dynamic("-l", "--list") || !enabled("-l", "--list")
	case "release create":
		remote = dynamic("--verify-tag") || !enabled("--verify-tag")
	case "release delete":
		remote = dynamic("--cleanup-tag") || enabled("--cleanup-tag")
	case "release edit":
		_, tag := ghFlagValue(parsed.flags, "--tag")
		_, target := ghFlagValue(parsed.flags, "--target")
		draft, draftSet := ghFlagValue(parsed.flags, "--draft")
		remote = dynamic("--verify-tag", "--tag", "--target", "--draft") || !enabled("--verify-tag") && (tag || target || draftSet && draft == "false")
	case "repo create", "repo new", "repo edit", "repo rename", "repo archive", "repo unarchive",
		"repo deploy-key add", "repo deploy-key delete", "repo autolink create", "repo autolink delete",
		"secret delete", "variable set", "variable delete", "workflow enable", "workflow disable":
		remote = true
	case "secret set":
		remote = dynamic("--no-store") || !enabled("--no-store")
	case "pr update-branch", "pr revert", "repo fork", "repo sync", "repo delete", "gist create", "gist edit", "gist delete", "gist rename":
		remote = true
	case "workflow run", "run rerun", "agent-task create", "codespace create", "codespace rebuild", "copilot":
		parsed.undecidable = "gh indirect execution content is not statically available"
		return
	case "codespace ssh":
		if !enabled("--config") {
			if len(positionals.argv) == 0 || positionals.containsEmpty() || strings.HasPrefix(positionals.argv[0], "-") {
				parsed.undecidable = "codespace SSH execution content is not statically available"
			} else {
				parsed.nestedScripts = []string{strings.Join(positionals.argv, " ")}
			}
			return
		}
	case "skill publish":
		remote = dynamic("--dry-run") || !enabled("--dry-run")
	case "stack":
		if positionals.anyDynamic() {
			parsed.undecidable = "gh stack arguments cannot be determined"
			return
		}
	}
	if remote {
		parsed.risk = "remote-code-ref-mutation"
		return
	}
	parsed.allowDynamicArgs = true
}

func ghUnconditionalDataCommand(path string) bool {
	switch path {
	case "pr view", "pr list", "pr ls", "pr diff", "pr checks", "pr status",
		"pr checkout", "pr co", "co", "pr comment", "pr edit", "pr review", "pr ready", "pr reopen", "pr lock", "pr unlock",
		"issue list", "issue ls", "issue view", "issue status", "issue create", "issue edit", "issue comment",
		"issue close", "issue reopen", "issue lock", "issue unlock", "issue delete", "issue transfer", "issue pin", "issue unpin",
		"repo view", "repo list", "repo ls", "repo clone",
		"alias list", "alias ls", "alias set", "alias import", "alias delete",
		"release view", "release list", "release ls", "release download", "release upload", "release delete-asset":
		return true
	}
	return false
}
