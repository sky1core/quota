package agenthooks

import "strings"

var commitOptions = map[string]optionValue{
	"-F": optionRequiredValue, "--file": optionRequiredValue,
	"-m": optionRequiredValue, "--message": optionRequiredValue,
	"-c": optionRequiredValue, "--reedit-message": optionRequiredValue,
	"-C": optionRequiredValue, "--reuse-message": optionRequiredValue,
	"-t": optionRequiredValue, "--template": optionRequiredValue,
	"-U": optionRequiredValue, "--unified": optionRequiredValue,
	"--author": optionRequiredValue, "--date": optionRequiredValue,
	"--fixup": optionRequiredValue, "--squash": optionRequiredValue,
	"--trailer": optionRequiredValue, "--cleanup": optionRequiredValue,
	"--inter-hunk-context": optionRequiredValue, "--pathspec-from-file": optionRequiredValue,
	"-S": optionAttachedValue, "--gpg-sign": optionAttachedValue,
	"-u": optionAttachedValue, "--untracked-files": optionAttachedValue,
	"-q": optionNoValue, "--quiet": optionNoValue,
	"-v": optionNoValue, "--verbose": optionNoValue,
	"-s": optionNoValue, "--signoff": optionNoValue,
	"-e": optionNoValue, "--edit": optionNoValue,
	"-a": optionNoValue, "--all": optionNoValue,
	"-i": optionNoValue, "--include": optionNoValue,
	"-p": optionNoValue, "--patch": optionNoValue,
	"-o": optionNoValue, "--only": optionNoValue,
	"-n": optionNoValue, "--no-verify": optionNoValue, "--verify": optionNoValue,
	"-z": optionNoValue, "--null": optionNoValue,
	"-h": optionNoValue, "--help": optionNoValue,
	"--reset-author": optionNoValue, "--status": optionNoValue,
	"--interactive": optionNoValue, "--dry-run": optionNoValue,
	"--short": optionNoValue, "--branch": optionNoValue, "--ahead-behind": optionNoValue,
	"--porcelain": optionNoValue, "--long": optionNoValue,
	"--amend": optionNoValue, "--no-post-rewrite": optionNoValue, "--post-rewrite": optionNoValue,
	"--pathspec-file-nul": optionNoValue, "--allow-empty": optionNoValue, "--allow-empty-message": optionNoValue,
}

func commitOptionsWithNegations() map[string]optionValue {
	options := make(map[string]optionValue, len(commitOptions)*2)
	for name, value := range commitOptions {
		options[name] = value
	}
	for name := range commitOptions {
		if strings.HasPrefix(name, "--") && name != "--trailer" {
			negative := "--no-" + strings.TrimPrefix(name, "--")
			if _, exists := options[negative]; !exists {
				options[negative] = optionNoValue
			}
		}
	}
	return options
}
