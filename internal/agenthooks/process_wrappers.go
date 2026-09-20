package agenthooks

import (
	"fmt"
	"strconv"
	"strings"
)

var processWrapperOptions = map[string]map[string]optionValue{
	"xargs": optionValues(
		optionGroup{optionNoValue, `-0 --null -r --no-run-if-empty -t --verbose -p --interactive -x --exit -o --open-tty --help --version`},
		optionGroup{optionRequiredValue, `-I --replace -n --max-args -L --max-lines -P --max-procs -s --max-chars -a --arg-file -d --delimiter -E --eof`},
	),
	"nohup": optionValues(optionGroup{optionNoValue, `--help --version`}),
	"nice": optionValues(
		optionGroup{optionNoValue, `--help --version`},
		optionGroup{optionRequiredValue, `-n --adjustment`},
	),
	"timeout": optionValues(
		optionGroup{optionNoValue, `--help --version -v --verbose -f --foreground -p --preserve-status`},
		optionGroup{optionRequiredValue, `-s --signal -k --kill-after`},
	),
}

func normalizeProcessWrapperArgv(argv []string) ([]string, error) {
	input, err := parseProcessWrapperInput(commandInput{argv: argv})
	return input.argv, err
}

func parseProcessWrapperInput(input commandInput) (commandInput, error) {
	argv := input.argv
	command := commandName(argv[0])
	options := processWrapperOptions[command]
	replacement := ""
	i := 1
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			break
		}
		if command == "nice" {
			if _, err := strconv.Atoi(arg[1:]); err == nil {
				i++
				continue
			}
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			name, value, err := resolveLongOption(name, options, true)
			if err != nil {
				return input, fmt.Errorf("%s: %w", command, err)
			}
			if attached && value == optionNoValue {
				return input, fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			if name == "--help" || name == "--version" {
				return input, nil
			}
			if !attached && value == optionRequiredValue {
				i++
				if i == len(argv) {
					return input, fmt.Errorf("%s option %s requires a value", command, name)
				}
			}
			if command == "xargs" && name == "--replace" {
				if attached {
					_, replacement, _ = strings.Cut(arg, "=")
				} else {
					replacement = argv[i]
				}
			}
		} else {
			for j := 1; j < len(arg); j++ {
				name := "-" + string(arg[j])
				value, ok := options[name]
				if !ok {
					return input, fmt.Errorf("unsupported %s option %s", command, name)
				}
				if value == optionRequiredValue {
					if j == len(arg)-1 {
						i++
						if i == len(argv) {
							return input, fmt.Errorf("%s option %s requires a value", command, name)
						}
					}
					if command == "xargs" && name == "-I" {
						if j == len(arg)-1 {
							replacement = argv[i]
						} else {
							replacement = arg[j+1:]
						}
					}
					break
				}
			}
		}
		i++
	}
	if command == "timeout" {
		i++
	}
	if i >= len(argv) {
		return input, nil
	}
	for j := 1; j < i; j++ {
		if input.dynamicAt(j) {
			return input, fmt.Errorf("%s execution options cannot be determined", command)
		}
	}
	rest := commandInput{argv: append([]string(nil), argv[i:]...), dynamicArgs: make([]bool, len(argv)-i)}
	for j := range rest.argv {
		rest.dynamicArgs[j] = input.dynamicAt(i + j)
	}
	if replacement != "" {
		for i, arg := range rest.argv {
			if strings.Contains(arg, replacement) {
				rest.argv[i] = ""
				rest.dynamicArgs[i] = true
			}
		}
	} else if command == "xargs" {
		rest.argv = append(rest.argv, "")
		rest.dynamicArgs = append(rest.dynamicArgs, true)
	}
	return rest, nil
}

func knownIndirectProgram(arg string) bool {
	name := commandName(arg)
	if isShellCommand(name) || isCommandWrapper(name) {
		return true
	}
	switch name {
	case "git", "gh", "echo", "printf", "true", "false", ":", "rm", "rmdir", "unlink", "dd", "kill", "killall", "pkill", "chmod", "chown", "chgrp":
		return true
	}
	return false
}
