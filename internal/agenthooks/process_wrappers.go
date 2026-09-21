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

func parseProcessWrapperInput(input commandInput) (commandInput, error) {
	argv := input.argv
	command := commandName(argv[0])
	options := processWrapperOptions[command]
	replacement := ""
	xargsMaxArgs := 0
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
				if input.dynamicAt(i) || input.maySplitAt(i) {
					return input, fmt.Errorf("%s execution options cannot be determined", command)
				}
				i++
				continue
			}
		}
		if input.maySplitAt(i) || input.dynamicAt(i) && !input.literalPrefixAt(i) {
			return input, fmt.Errorf("%s execution options cannot be determined", command)
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			name, value, err := resolveLongOption(name, options, true)
			if err != nil {
				return input, fmt.Errorf("%s: %w", command, err)
			}
			if input.dynamicAt(i) && !attached {
				return input, fmt.Errorf("%s execution options cannot be determined", command)
			}
			if attached && value == optionNoValue {
				return input, fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			if command == "xargs" && attached && value == optionRequiredValue && input.dynamicAt(i) {
				return input, fmt.Errorf("%s execution options cannot be determined", command)
			}
			if name == "--help" || name == "--version" {
				return input, nil
			}
			if !attached && value == optionRequiredValue {
				i++
				if i == len(argv) {
					return input, fmt.Errorf("%s option %s requires a value", command, name)
				}
				if input.maySplitAt(i) {
					return input, fmt.Errorf("%s option %s value can change command position", command, name)
				}
				if command == "xargs" && input.dynamicAt(i) {
					return input, fmt.Errorf("%s execution options cannot be determined", command)
				}
			}
			if command == "xargs" && name == "--replace" {
				if attached {
					_, replacement, _ = strings.Cut(arg, "=")
				} else {
					replacement = argv[i]
				}
			}
			if command == "xargs" && name == "--max-args" {
				valueText := ""
				if attached {
					_, valueText, _ = strings.Cut(arg, "=")
				} else {
					valueText = argv[i]
				}
				n, err := strconv.Atoi(valueText)
				if err != nil || n < 1 {
					return input, fmt.Errorf("%s option %s requires a positive integer value", command, name)
				}
				if replacement != "" && n != 1 {
					return input, fmt.Errorf("%s option %s conflicts with replacement mode", command, name)
				}
				xargsMaxArgs = n
			}
			if command == "xargs" && name == "--max-lines" {
				replacement = ""
				xargsMaxArgs = 0
			}
		} else {
			for j := 1; j < len(arg); j++ {
				name := "-" + string(arg[j])
				value, ok := options[name]
				if !ok {
					return input, fmt.Errorf("unsupported %s option %s", command, name)
				}
				if input.dynamicAt(i) && value != optionRequiredValue {
					return input, fmt.Errorf("%s execution options cannot be determined", command)
				}
				if value == optionRequiredValue {
					valueText := ""
					if j == len(arg)-1 {
						if input.dynamicAt(i) {
							return input, fmt.Errorf("%s execution options cannot be determined", command)
						}
						i++
						if i == len(argv) {
							return input, fmt.Errorf("%s option %s requires a value", command, name)
						}
						if input.maySplitAt(i) {
							return input, fmt.Errorf("%s option %s value can change command position", command, name)
						}
						if command == "xargs" && input.dynamicAt(i) {
							return input, fmt.Errorf("%s execution options cannot be determined", command)
						}
						valueText = argv[i]
					} else {
						valueText = arg[j+1:]
						if input.dynamicAt(i) {
							return input, fmt.Errorf("%s execution options cannot be determined", command)
						}
					}
					if command == "xargs" && name == "-I" {
						replacement = valueText
					}
					if command == "xargs" && name == "-n" {
						n, err := strconv.Atoi(valueText)
						if err != nil || n < 1 {
							return input, fmt.Errorf("%s option %s requires a positive integer value", command, name)
						}
						if replacement != "" && n != 1 {
							return input, fmt.Errorf("%s option %s conflicts with replacement mode", command, name)
						}
						xargsMaxArgs = n
					}
					if command == "xargs" && name == "-L" {
						replacement = ""
						xargsMaxArgs = 0
					}
					break
				}
			}
		}
		i++
	}
	if command == "timeout" {
		if i < len(argv) && input.maySplitAt(i) {
			return input, fmt.Errorf("%s duration can change command position", command)
		}
		i++
	}
	if i >= len(argv) {
		return input, nil
	}
	rest := commandInput{argv: append([]string(nil), argv[i:]...), dynamicArgs: make([]bool, len(argv)-i)}
	for j := range rest.argv {
		rest.dynamicArgs[j] = input.dynamicAt(i + j)
	}
	if i < len(input.splitArgs) {
		rest.splitArgs = append([]bool(nil), input.splitArgs[i:]...)
	}
	if i < len(input.literalPrefix) {
		rest.literalPrefix = append([]bool(nil), input.literalPrefix[i:]...)
	}
	if replacement != "" {
		for i, arg := range rest.argv {
			index := strings.Index(arg, replacement)
			if index >= 0 {
				rest.argv[i] = arg[:index]
				rest.dynamicArgs[i] = true
				if i < len(rest.splitArgs) {
					rest.splitArgs[i] = false
				}
				if i < len(rest.literalPrefix) {
					rest.literalPrefix[i] = index > 0
				}
			}
		}
	} else if command == "xargs" {
		rest.argv = append(rest.argv, "")
		rest.dynamicArgs = append(rest.dynamicArgs, true)
		rest.splitArgs = append(rest.splitArgs, xargsMaxArgs != 1)
		rest.literalPrefix = append(rest.literalPrefix, false)
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
