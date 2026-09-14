package agenthooks

import (
	"fmt"
	"maps"
	"strings"
)

var ghLeadingOptions = optionValues(
	optionGroup{optionBooleanValue, `--paginate --help -h --version`},
	optionGroup{optionRequiredValue, `-R --repo --hostname --git-protocol --editor --browser`},
)

func parseGhCommand(argv []string) ([]string, []commandFlag, error) {
	args := append([]string(nil), argv[1:]...)
	indices := make([]int, len(args))
	for i := range indices {
		indices[i] = i + 1
	}
	var command []string
	firstCommand := len(argv)
	for {
		path := strings.Join(command, " ")
		index := ghCommandIndex(args, ghCommandOptions[path])
		if index < 0 {
			break
		}
		child := args[index]
		if path != "" {
			child = path + " " + child
		}
		if _, ok := ghCommandOptions[child]; !ok || strings.ContainsAny(args[index], " \t\r\n") {
			if len(command) == 0 || ghCommandHasChildren(command) {
				return nil, nil, fmt.Errorf("unsupported gh command %q", child)
			}
			break
		}
		if len(command) == 0 {
			firstCommand = indices[index]
		}
		command = append(command, args[index])
		args = append(args[:index], args[index+1:]...)
		indices = append(indices[:index], indices[index+1:]...)
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
		for i, arg := range args {
			if indices[i] >= leadingEnd {
				kept = append(kept, arg)
			}
		}
		args = kept
	}
	path := strings.Join(command, " ")
	out := append([]string{"gh"}, command...)
	out = append(out, args...)
	if path == "extension exec" {
		return out, nil, nil
	}
	options := maps.Clone(ghCommandOptions[path])
	if _, hasShorthand := options["-h"]; !hasShorthand {
		options["-h"] = optionBooleanValue
	}
	flags, err := parseCommandFlags("gh "+path, args, options, false)
	if err != nil {
		return nil, nil, err
	}
	if len(command) == 0 || ghCommandHasChildren(command) {
		for i := 0; i < len(args); {
			if args[i] == "--" && i+1 == len(args) {
				break
			}
			end, ok := ghOptionEnd(args, i, options)
			if !ok {
				return nil, nil, fmt.Errorf("unsupported gh command arguments")
			}
			i = end
		}
	}
	return out, flags, nil
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
				if !known || value == optionRequiredValue {
					i++
				}
			}
		} else if arg != "" {
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
		if value == optionRequiredValue && !attached {
			end++
		}
	} else {
		for j := 1; j < len(arg); j++ {
			value, ok := options["-"+string(arg[j])]
			if !ok {
				return end, false
			}
			if value == optionRequiredValue {
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
