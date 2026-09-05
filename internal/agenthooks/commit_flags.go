package agenthooks

import (
	"fmt"
	"strings"
)

type commitOptionValue uint8

const (
	commitNoValue commitOptionValue = iota
	commitRequiredValue
	commitAttachedValue
)

var commitOptions = map[string]commitOptionValue{
	"-F": commitRequiredValue, "--file": commitRequiredValue,
	"-m": commitRequiredValue, "--message": commitRequiredValue,
	"-c": commitRequiredValue, "--reedit-message": commitRequiredValue,
	"-C": commitRequiredValue, "--reuse-message": commitRequiredValue,
	"-t": commitRequiredValue, "--template": commitRequiredValue,
	"-U": commitRequiredValue, "--unified": commitRequiredValue,
	"--author": commitRequiredValue, "--date": commitRequiredValue,
	"--fixup": commitRequiredValue, "--squash": commitRequiredValue,
	"--trailer": commitRequiredValue, "--cleanup": commitRequiredValue,
	"--inter-hunk-context": commitRequiredValue, "--pathspec-from-file": commitRequiredValue,
	"-S": commitAttachedValue, "--gpg-sign": commitAttachedValue,
	"-u": commitAttachedValue, "--untracked-files": commitAttachedValue,
	"-q": commitNoValue, "--quiet": commitNoValue,
	"-v": commitNoValue, "--verbose": commitNoValue,
	"-s": commitNoValue, "--signoff": commitNoValue,
	"-e": commitNoValue, "--edit": commitNoValue,
	"-a": commitNoValue, "--all": commitNoValue,
	"-i": commitNoValue, "--include": commitNoValue,
	"-p": commitNoValue, "--patch": commitNoValue,
	"-o": commitNoValue, "--only": commitNoValue,
	"-n": commitNoValue, "--no-verify": commitNoValue, "--verify": commitNoValue,
	"-z": commitNoValue, "--null": commitNoValue,
	"-h": commitNoValue, "--help": commitNoValue,
	"--reset-author": commitNoValue, "--status": commitNoValue,
	"--interactive": commitNoValue, "--dry-run": commitNoValue,
	"--short": commitNoValue, "--branch": commitNoValue, "--ahead-behind": commitNoValue,
	"--porcelain": commitNoValue, "--long": commitNoValue,
	"--amend": commitNoValue, "--no-post-rewrite": commitNoValue, "--post-rewrite": commitNoValue,
	"--pathspec-file-nul": commitNoValue, "--allow-empty": commitNoValue, "--allow-empty-message": commitNoValue,
}

func isGitCommit(argv []string) bool {
	return len(argv) >= 2 && commandName(argv[0]) == "git" && argv[1] == "commit"
}

func gitCommitFlags(args []string) ([]string, error) {
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			name, value, err := resolveCommitLongOption(name)
			if err != nil {
				return nil, err
			}
			if attached && value == commitNoValue {
				return nil, fmt.Errorf("git commit option %s does not accept a value", name)
			}
			if name == "--amend" || name == "--no-amend" {
				active := flags[:0]
				for _, flag := range flags {
					if flag != "--amend" && flag != "--no-amend" {
						active = append(active, flag)
					}
				}
				flags = active
			}
			flags = append(flags, name)
			if value == commitRequiredValue && !attached {
				i++
				if i == len(args) {
					return nil, fmt.Errorf("git commit option %s requires a value", name)
				}
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			name := "-" + string(arg[j])
			value, ok := commitOptions[name]
			if !ok {
				return nil, fmt.Errorf("unsupported git commit option %s", name)
			}
			flags = append(flags, name)
			if value == commitRequiredValue {
				if j == len(arg)-1 {
					i++
					if i == len(args) {
						return nil, fmt.Errorf("git commit option %s requires a value", name)
					}
				}
				break
			}
			if value == commitAttachedValue {
				break
			}
		}
	}
	return flags, nil
}

func resolveCommitLongOption(name string) (string, commitOptionValue, error) {
	if value, ok := commitOptions[name]; ok {
		return name, value, nil
	}
	if positive, negative := strings.CutPrefix(name, "--no-"); negative {
		if _, ok := commitOptions["--"+positive]; ok {
			return name, commitNoValue, nil
		}
	}
	matches := map[string]commitOptionValue{}
	for option, value := range commitOptions {
		if !strings.HasPrefix(option, "--") {
			continue
		}
		if strings.HasPrefix(option, name) {
			matches[option] = value
		}
		negative := "--no-" + strings.TrimPrefix(option, "--")
		if strings.HasPrefix(negative, name) {
			matches[negative] = commitNoValue
		}
	}
	if len(matches) == 1 {
		for option, value := range matches {
			return option, value, nil
		}
	}
	return "", commitNoValue, fmt.Errorf("unsupported or ambiguous git commit option %s", name)
}
