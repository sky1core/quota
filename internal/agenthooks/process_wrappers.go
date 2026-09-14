package agenthooks

import (
	"fmt"
	"strconv"
	"strings"
)

var processWrapperOptions = map[string]map[string]optionValue{
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
	command := commandName(argv[0])
	options := processWrapperOptions[command]
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
				return argv, fmt.Errorf("%s: %w", command, err)
			}
			if attached && value == optionNoValue {
				return argv, fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			if name == "--help" || name == "--version" {
				return argv, nil
			}
			if !attached && value == optionRequiredValue {
				i++
				if i == len(argv) {
					return argv, fmt.Errorf("%s option %s requires a value", command, name)
				}
			}
		} else {
			for j := 1; j < len(arg); j++ {
				name := "-" + string(arg[j])
				value, ok := options[name]
				if !ok {
					return argv, fmt.Errorf("unsupported %s option %s", command, name)
				}
				if value == optionRequiredValue {
					if j == len(arg)-1 {
						i++
						if i == len(argv) {
							return argv, fmt.Errorf("%s option %s requires a value", command, name)
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
		return argv, nil
	}
	return argv[i:], nil
}
