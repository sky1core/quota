package agenthooks

import (
	"fmt"
	"strconv"
	"strings"
)

type optionValue uint8

const (
	optionNoValue optionValue = iota
	optionRequiredValue
	optionRequiredSeparateValue
	optionRequiredNonEmptyValue
	optionAttachedValue
	optionLastArgDefault
	optionBooleanValue
	optionBooleanForce
	optionToggle
)

type optionGroup struct {
	value optionValue
	names string
}

type commandFlag struct {
	name         string
	token        string
	value        string
	valueDynamic bool
	disabled     bool
	literal      bool
}

func optionValues(groups ...optionGroup) map[string]optionValue {
	values := make(map[string]optionValue)
	for _, group := range groups {
		for _, name := range strings.Fields(group.names) {
			values[name] = group.value
		}
	}
	return values
}

var gitCommandOptions = map[string]map[string]optionValue{
	"push": optionValues(
		optionGroup{optionNoValue, `-h --help -n --dry-run -f --force --all --mirror --delete -d --tags --follow-tags --prune --atomic --porcelain --verify --no-verify --set-upstream -u --quiet -q --verbose -v --progress --force-if-includes`},
		optionGroup{optionRequiredValue, `--repo --receive-pack --exec --push-option -o`},
		optionGroup{optionAttachedValue, `--force-with-lease --signed --recurse-submodules`},
	),
	"send-pack": optionValues(
		optionGroup{optionNoValue, `-h --help -n --dry-run -f --force --all --mirror --atomic --verbose -v --quiet -q --stdin --stateless-rpc --thin --progress`},
		optionGroup{optionRequiredValue, `--receive-pack --exec --push-option`},
		optionGroup{optionAttachedValue, `--signed`},
	),
	"rebase": optionValues(
		optionGroup{optionNoValue, `-h --help --abort --continue --skip --quit -i --interactive --autosquash --autostash --no-autostash --no-autosquash --root --update-refs --no-update-refs --keep-empty --no-verify --verify --edit-todo --keep-base --no-keep-base -q --quiet --no-quiet -v --verbose --no-verbose -n --no-stat --stat --signoff --no-signoff --committer-date-is-author-date --reset-author-date --ignore-whitespace -f --force-rebase --no-force-rebase --no-ff --ff --show-current-patch --apply -m --merge --rerere-autoupdate --no-rerere-autoupdate --no-gpg-sign --no-rebase-merges --fork-point --no-fork-point --no-root --reschedule-failed-exec --no-reschedule-failed-exec --reapply-cherry-picks --no-reapply-cherry-picks --no-exec`},
		optionGroup{optionRequiredValue, `-x --exec --onto --strategy -s --strategy-option -X -C --whitespace --empty`},
		optionGroup{optionAttachedValue, `-r --rebase-merges --gpg-sign -S`},
	),
	"filter-branch": optionValues(
		optionGroup{optionNoValue, `-h --help -f --force --prune-empty --remap-to-ancestor`},
		optionGroup{optionRequiredValue, `--setup --env-filter --tree-filter --index-filter --parent-filter --msg-filter --commit-filter --tag-name-filter --state-branch -d`},
	),
	"commit": commitOptionsWithNegations(),
	"tag": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -l --list -d --delete -v --verify
			-a --annotate --no-annotate -e --edit --no-edit -s --sign --no-sign -f --force --no-force
			--create-reflog --no-create-reflog --omit-empty --no-omit-empty -i --ignore-case --no-ignore-case
			--no-file --no-cleanup --no-local-user --no-column --no-sort --no-points-at --no-format --no-color`},
		optionGroup{optionRequiredValue, `-m --message -F --file --trailer --cleanup -u --local-user --sort --format`},
		optionGroup{optionAttachedValue, `-n --column --color`},
		optionGroup{optionLastArgDefault, `--contains --no-contains --with --without --merged --no-merged --points-at`},
	),
	"branch": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -v --verbose --no-verbose -q --quiet --no-quiet
			--set-upstream --no-set-upstream --unset-upstream --no-unset-upstream -r --remotes -a --all
			-d --delete --no-delete -D -m --move --no-move -M -c --copy --no-copy -C -l --list --no-list
			--omit-empty --no-omit-empty --show-current --no-show-current --create-reflog --no-create-reflog
			--edit-description --no-edit-description -f --force --no-force -i --ignore-case --no-ignore-case
			--recurse-submodules --no-recurse-submodules --no-track --no-set-upstream-to --no-color
			--no-abbrev --no-column --no-sort --no-points-at --no-format`},
		optionGroup{optionRequiredValue, `-u --set-upstream-to --sort --points-at --format`},
		optionGroup{optionAttachedValue, `-t --track --color --abbrev --column`},
		optionGroup{optionLastArgDefault, `--contains --no-contains --with --without --merged --no-merged`},
	),
	"switch": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --guess --no-guess --discard-changes --no-discard-changes
			-q --quiet --no-quiet --progress --no-progress -m --merge --no-merge -d --detach --no-detach
			-f --force --no-force --overwrite-ignore --no-overwrite-ignore --ignore-other-worktrees --no-ignore-other-worktrees
			--no-create --no-force-create --no-recurse-submodules --no-conflict --no-track --no-orphan`},
		optionGroup{optionRequiredValue, `-c --create -C --force-create --conflict --orphan`},
		optionGroup{optionAttachedValue, `--recurse-submodules -t --track`},
	),
	"checkout": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -l --guess --no-guess --overlay --no-overlay
			-q --quiet --no-quiet --progress --no-progress -m --merge --no-merge -d --detach --no-detach
			-f --force --no-force --overwrite-ignore --no-overwrite-ignore --ignore-other-worktrees --no-ignore-other-worktrees
			-2 --ours -3 --theirs -p --patch --no-patch --ignore-skip-worktree-bits --no-ignore-skip-worktree-bits
			--pathspec-file-nul --no-pathspec-file-nul --no-pathspec-from-file
			--no-recurse-submodules --no-conflict --no-track --no-orphan`},
		optionGroup{optionRequiredValue, `-b -B --conflict --orphan -U --unified --inter-hunk-context --pathspec-from-file`},
		optionGroup{optionAttachedValue, `--recurse-submodules -t --track`},
	),
	"reset": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -q --quiet --no-quiet --no-refresh --refresh
			--mixed --soft --hard --merge --keep -p --patch --no-patch -N --intent-to-add --no-intent-to-add
			--pathspec-file-nul --no-pathspec-file-nul --no-pathspec-from-file --no-recurse-submodules`},
		optionGroup{optionRequiredValue, `-U --unified --inter-hunk-context --pathspec-from-file`},
		optionGroup{optionAttachedValue, `--recurse-submodules`},
	),
	"pull": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -q --quiet --no-quiet -v --verbose --no-verbose
			--ff --no-ff --ff-only --commit --no-commit --edit --no-edit --cleanup --no-cleanup
			--rebase --no-rebase --autostash --no-autostash --stat --no-stat --log --no-log
			--signoff --no-signoff --verify --no-verify --progress --no-progress --tags --no-tags
			--allow-unrelated-histories --no-recurse-submodules`},
		optionGroup{optionRequiredValue, `-s --strategy -X --strategy-option -S --gpg-sign --depth --deepen --shallow-since
			--shallow-exclude --server-option --upload-pack`},
		optionGroup{optionAttachedValue, `--recurse-submodules --jobs`},
	),
	"for-each-repo": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all`},
		optionGroup{optionRequiredValue, `--config`},
	),
	"submodule": optionValues(
		optionGroup{optionNoValue, `-h --help -q --quiet --cached --recursive --init --remote -N --no-fetch -f --force --checkout --merge --rebase --recommend-shallow --no-recommend-shallow --single-branch --no-single-branch --all --default --files --progress`},
		optionGroup{optionRequiredValue, `-b --branch --name --reference --filter --summary-limit --depth -j --jobs`},
	),
	"http-push": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --dry-run --all --force --verbose -d -D`},
	),
	"update-ref": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -d --delete --stdin --no-deref --create-reflog`},
		optionGroup{optionRequiredValue, `-m`},
	),
	"replace": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -f --force -d --delete --graft --edit --convert-graft-file`},
		optionGroup{optionRequiredValue, `--format`},
	),
	"reflog": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --all --single-worktree --updateref --rewrite --stale-fix
			--dry-run --verbose`},
		optionGroup{optionAttachedValue, `--expire --expire-unreachable`},
	),
	"diff": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --cached --staged --check --summary --patch --no-patch
			--name-only --name-status --numstat --shortstat --dirstat --raw --exit-code --quiet
			--no-color --no-color-moved --minimal --histogram --patience --full-index --binary --text
			--find-copies-harder --pickaxe-all`},
		optionGroup{optionRequiredValue, `--inter-hunk-context --diff-filter -S -G -O --output
			--src-prefix --dst-prefix --word-diff-regex`},
		optionGroup{optionAttachedValue, `-U --unified -M -C -B -l --stat --dirstat --diff-algorithm --word-diff --color-words
			--color --color-moved --color-moved-ws --relative --ignore-submodules --submodule
			--find-renames --find-copies --break-rewrites --abbrev`},
	),
	"log": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --oneline --shortstat --name-only --name-status
			--graph --all --no-decorate --patch --no-patch --reverse
			--date-order --author-date-order --topo-order --walk-reflogs --do-walk --no-color`},
		optionGroup{optionRequiredValue, `-n --max-count --skip --since --after --until --before --author
			--committer --grep --grep-reflog --format --date --decorate-refs --decorate-refs-exclude`},
		optionGroup{optionAttachedValue, `--stat --pretty --branches --tags --remotes --no-walk --decorate --color`},
	),
	"show": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --shortstat --summary --patch --no-patch
			--name-only --name-status --raw --quiet --no-color --no-decorate`},
		optionGroup{optionRequiredValue, `--format --date`},
		optionGroup{optionAttachedValue, `--stat --pretty --decorate --color`},
	),
	"rev-parse": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all --show-toplevel --show-prefix --show-cdup --git-dir
			--absolute-git-dir --git-common-dir --is-inside-git-dir --is-inside-work-tree --is-bare-repository
			--is-shallow-repository --show-superproject-working-tree --show-ref-format
			--verify --quiet --symbolic --symbolic-full-name --revs-only --no-revs
			--flags --no-flags --parseopt --sq --not`},
		optionGroup{optionRequiredValue, `--path-format --prefix --since --after --until --before`},
		optionGroup{optionRequiredSeparateValue, `--default`},
		optionGroup{optionAttachedValue, `--short --abbrev-ref --show-object-format --branches --tags --remotes --glob --exclude`},
	),
	"status": optionValues(
		optionGroup{optionNoValue, `-h --help --help-all -s --short -b --branch --show-stash
			--ahead-behind --no-ahead-behind --long -z --renames --no-renames --no-column`},
		optionGroup{optionAttachedValue, `--porcelain -u --untracked-files --ignored --ignore-submodules --column --find-renames`},
	),
}

var gitDefaultSubcommandOptions = optionValues(optionGroup{optionNoValue, `-h --help --help-all`})

var gitSubmoduleForeachOptions = optionValues(optionGroup{optionNoValue, `-h --help -q --quiet --recursive`})

var ghCheckoutOptions = optionValues(
	optionGroup{optionRequiredValue, `-b --branch -R --repo`},
	optionGroup{optionBooleanValue, `--detach --recurse-submodules -h --help`},
	optionGroup{optionBooleanForce, `-f --force`},
)

var ghRepoEditOptions = optionValues(
	optionGroup{optionRequiredValue, `--add-topic --default-branch -d --description -h --homepage --remove-topic
		--squash-merge-commit-message --visibility`},
	optionGroup{optionBooleanValue, `--accept-visibility-change-consequences --allow-forking --allow-update-branch
		--delete-branch-on-merge --enable-advanced-security --enable-auto-merge --enable-discussions --enable-issues
		--enable-merge-commit --enable-projects --enable-rebase-merge --enable-secret-scanning
		--enable-secret-scanning-push-protection --enable-squash-merge --enable-wiki --template --help`},
)

type parsedCommand struct {
	argv             []string
	flags            []commandFlag
	undecidable      string
	risk             string
	flagError        string
	flagsUncertain   bool
	nestedScripts    []string
	nestedArgv       []commandInput
	allowDynamicArgs bool
	dynamic          bool
}

type commandInput struct {
	argv        []string
	dynamicArgs []bool
	splitArgs   []bool
}

func (input commandInput) dynamicAt(i int) bool {
	return i >= 0 && i < len(input.dynamicArgs) && input.dynamicArgs[i]
}

func (input commandInput) maySplitAt(i int) bool {
	return i >= 0 && i < len(input.splitArgs) && input.splitArgs[i]
}

func (input commandInput) anyDynamic() bool {
	for i := range input.argv {
		if input.dynamicAt(i) {
			return true
		}
	}
	return false
}

func (input commandInput) containsEmpty() bool {
	for _, arg := range input.argv {
		if arg == "" {
			return true
		}
	}
	return false
}

func (input commandInput) from(i int) commandInput {
	out := commandInput{argv: input.argv[i:]}
	if i < len(input.dynamicArgs) {
		out.dynamicArgs = input.dynamicArgs[i:]
	}
	if i < len(input.splitArgs) {
		out.splitArgs = input.splitArgs[i:]
	}
	return out
}

func (input commandInput) withArgvPrefixAndTail(prefix []string, tail int) commandInput {
	out := commandInput{argv: append(append([]string(nil), prefix...), input.argv[tail:]...)}
	for i := range prefix {
		out.dynamicArgs = append(out.dynamicArgs, input.dynamicAt(i))
		out.splitArgs = append(out.splitArgs, input.maySplitAt(i))
	}
	if tail < len(input.dynamicArgs) {
		out.dynamicArgs = append(out.dynamicArgs, input.dynamicArgs[tail:]...)
	}
	if tail < len(input.splitArgs) {
		out.splitArgs = append(out.splitArgs, input.splitArgs[tail:]...)
	}
	return out
}

func (input commandInput) shellQuote() string {
	parts := make([]string, len(input.argv))
	for i, arg := range input.argv {
		if input.dynamicAt(i) {
			parts[i] = `"${__quota_dynamic_argument}"`
		} else {
			parts[i] = ShellQuote([]string{arg})
		}
	}
	return strings.Join(parts, " ")
}

func parseCommand(argv []string) parsedCommand {
	return parseCommandInput(commandInput{argv: argv})
}

func parseCommandInput(input commandInput) parsedCommand {
	argv := input.argv
	parsed := parsedCommand{argv: append([]string(nil), argv...)}
	if len(argv) == 0 {
		return parsed
	}
	name := commandName(argv[0])
	if subcommand, ok := gitDashedSubcommand(name); ok {
		parsed.argv = append([]string{"git", subcommand}, argv[1:]...)
		input.argv = parsed.argv
		if len(input.dynamicArgs) > 0 {
			input.dynamicArgs = append([]bool{input.dynamicAt(0), false}, input.dynamicArgs[1:]...)
		}
		if len(input.splitArgs) > 0 {
			input.splitArgs = append([]bool{input.maySplitAt(0), false}, input.splitArgs[1:]...)
		}
		name = "git"
	}
	switch name {
	case "git":
		input.argv = append([]string(nil), input.argv...)
		input.argv[0] = name
		var unknown bool
		input, unknown, parsed.undecidable = normalizeGitGlobalOptions(input)
		parsed.argv = append([]string(nil), input.argv...)
		parsed.dynamic = unknown
		if unknown || len(parsed.argv) < 2 {
			return parsed
		}
		if input.dynamicAt(1) {
			parsed.undecidable = "git subcommand cannot be determined"
			return parsed
		}
		if subcommand := parsed.argv[1]; subcommand != "" && !strings.HasPrefix(subcommand, "-") && !knownGitSubcommand(subcommand) {
			parsed.undecidable = "unknown git subcommand can resolve to a git alias or external helper"
			return parsed
		}
		options := gitCommandOptions[parsed.argv[1]]
		if options == nil {
			options = gitDefaultSubcommandOptions
		}
		input = input.from(2)
		stopAtOperand := parsed.argv[1] == "for-each-repo" || parsed.argv[1] == "bisect" || parsed.argv[1] == "submodule"
		args, err := parseCommandArgumentInput("git "+parsed.argv[1], input, options, true, stopAtOperand)
		parsed.flags = args.flags
		parsed.flagsUncertain = args.dynamicOptions || args.dynamicOptionSyntax
		if err != nil {
			parsed.flagError = err.Error()
		}
		classifyGitCommand(&parsed, args)

	case "gh":
		parsed.argv[0] = name
		normalized, err := parseGhCommandInput(input)
		if err != nil {
			parsed.dynamic = true
			parsed.undecidable = err.Error()
		} else {
			parsed = normalized
		}
	case "source", ".":
		parsed.undecidable = "sourced script content is not visible to policy evaluator"
	case "xargs":
		nested, err := parseProcessWrapperInput(input)
		if err != nil {
			parsed.undecidable = err.Error()
		} else if len(nested.argv) > 0 && commandName(nested.argv[0]) != "xargs" {
			if nested.argv[0] == "" || nested.dynamicAt(0) {
				parsed.undecidable = "xargs command cannot be determined"
			} else {
				parsed.nestedArgv = []commandInput{nested}
			}
		}
	case "trap":
		if len(argv) > 1 && argv[1] != "-p" && argv[1] != "-l" && argv[1] != "" && argv[1] != "-" {
			if input.dynamicAt(1) {
				parsed.undecidable = "trap script cannot be determined"
				return parsed
			}
			parsed.nestedScripts = []string{argv[1]}
		}
	default:
		parsed.flags = literalCommandFlags(parsed.argv[1:])
	}
	return parsed
}

func argvHasOption(args []string) bool {
	for _, arg := range args {
		if arg == "--" || arg == "--end-of-options" {
			return false
		}
		if arg != "-" && strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

func classifyGitCommand(parsed *parsedCommand, args parsedArguments) {
	argv := parsed.argv
	operands := args.operands.argv
	if len(argv) < 2 || argv[1] == "" {
		parsed.undecidable = "git subcommand cannot be determined"
		return
	}
	parsed.allowDynamicArgs = true
	if commandHasFlag(parsed.flags, "--help", "-h") {
		return
	}
	switch argv[1] {
	case "push", "send-pack", "http-push":
		parsed.allowDynamicArgs = false
		if commandHasFlag(parsed.flags, "--dry-run", "-n") && parsed.flagError == "" {
			if gitPushHasCustomReceiveProgram(parsed.flags) {
				parsed.undecidable = "git push receiver execution content is not visible to policy evaluator"
				return
			}
			return
		}
		parsed.risk = PolicyGroupRemoteCodeRefMutation
	case "hook":
		parsed.undecidable = "git hook execution content is not visible to policy evaluator"
	case "for-each-repo":
		if parsed.flagError != "" || len(operands) == 0 {
			parsed.undecidable = "git for-each-repo command cannot be determined"
			return
		}
		parsed.nestedArgv = []commandInput{{
			argv:        append([]string{"git"}, operands...),
			dynamicArgs: append([]bool{false}, args.operands.dynamicArgs...),
			splitArgs:   append([]bool{false}, args.operands.splitArgs...),
		}}
	case "bisect":
		if parsed.flagError != "" {
			parsed.undecidable = parsed.flagError
		} else if len(operands) > 0 && (operands[0] == "" || args.operands.dynamicAt(0)) {
			parsed.undecidable = "git bisect operation cannot be determined"
		} else if len(operands) > 0 && operands[0] == "run" {
			if len(operands) < 2 || operands[1] == "" {
				parsed.undecidable = "git bisect run command cannot be determined"
				return
			}
			parsed.flagError = ""
			parsed.nestedArgv = []commandInput{args.operands.from(1)}
		}
	case "submodule":
		classifyGitSubmoduleCommand(parsed, args)
	case "rebase", "filter-branch":
		if args.dynamicOptions {
			parsed.undecidable = "git execution options cannot be determined"
			return
		}
		for _, flag := range parsed.flags {
			if flag.name == "--exec" || flag.name == "-x" || strings.HasSuffix(flag.name, "-filter") || flag.name == "--setup" {
				if flag.value == "" || flag.valueDynamic {
					parsed.undecidable = "git execution option script cannot be determined"
					return
				}
				parsed.nestedScripts = append(parsed.nestedScripts, flag.value)
			}
		}
		if parsed.flagError != "" {
			parsed.undecidable = parsed.flagError
		}
	}
}

func gitPushHasCustomReceiveProgram(flags []commandFlag) bool {
	for _, flag := range flags {
		switch flag.name {
		case "--receive-pack", "--exec":
			return true
		}
	}
	return false
}

func classifyGitSubmoduleCommand(parsed *parsedCommand, args parsedArguments) {
	operands := args.operands.argv
	if len(operands) > 0 && !args.operands.dynamicAt(0) {
		switch operands[0] {
		case "add", "status", "init", "deinit", "update", "set-branch", "set-url", "summary", "sync", "absorbgitdirs":
			return
		}
	}
	if parsed.flagError != "" {
		parsed.undecidable = parsed.flagError
		return
	}
	if len(operands) == 0 {
		return
	}
	if operands[0] == "" || args.operands.dynamicAt(0) {
		parsed.undecidable = "git submodule operation cannot be determined"
		return
	}
	if operands[0] != "foreach" {
		parsed.undecidable = "unsupported git submodule operation"
		return
	}
	input := args.operands.from(1)
	foreach, err := parseCommandArgumentInput("git submodule foreach", input, gitSubmoduleForeachOptions, true, true)
	if err != nil {
		parsed.undecidable = err.Error()
		return
	}
	if commandHasFlag(foreach.flags, "--help", "-h") {
		return
	}
	if foreach.dynamicOptions {
		parsed.undecidable = "git submodule foreach options cannot be determined"
		return
	}
	scriptInput := foreach.operands
	scripts := scriptInput.argv
	if len(scripts) == 0 || scripts[0] == "" || scriptInput.anyDynamic() {
		parsed.undecidable = "git submodule foreach script cannot be determined"
		return
	}
	parsed.flagError = ""
	if len(scripts) == 1 {
		parsed.nestedScripts = []string{scripts[0]}
	} else {
		parsed.nestedArgv = []commandInput{scriptInput}
	}
}

func commandHasFlag(flags []commandFlag, names ...string) bool {
	for _, flag := range flags {
		for _, name := range names {
			if flag.name == name && !flag.disabled {
				return true
			}
		}
	}
	return false
}

func literalCommandFlags(args []string) []commandFlag {
	var flags []commandFlag
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flags = append(flags, commandFlag{name: arg, token: arg, literal: true})
		if !strings.HasPrefix(arg, "--") {
			for _, letter := range arg[1:] {
				flags = append(flags, commandFlag{name: "-" + string(letter), token: arg, literal: true})
			}
		}
	}
	return flags
}

func parseCommandFlags(command string, args []string, options map[string]optionValue, gitOptions bool) ([]commandFlag, error) {
	flags, _, err := parseCommandArguments(command, args, options, gitOptions)
	return flags, err
}

func parseCommandArguments(command string, args []string, options map[string]optionValue, gitOptions bool) ([]commandFlag, []string, error) {
	parsed, err := parseCommandArgumentInput(command, commandInput{argv: args}, options, gitOptions, false)
	if err != nil {
		return nil, nil, err
	}
	return parsed.flags, parsed.operands.argv, err
}

type parsedArguments struct {
	flags               []commandFlag
	operands            commandInput
	dynamicOptions      bool
	dynamicOptionSyntax bool
}

func parseCommandArgumentInput(command string, input commandInput, options map[string]optionValue, gitOptions, stopAtOperand bool) (parsedArguments, error) {
	args := input.argv
	var flags []commandFlag
	var operands []string
	var operandDynamic []bool
	var operandSplit []bool
	dynamicOptions := false
	dynamicOptionSyntax := false
	result := func() parsedArguments {
		return parsedArguments{flags: flags, operands: commandInput{argv: operands, dynamicArgs: operandDynamic, splitArgs: operandSplit}, dynamicOptions: dynamicOptions, dynamicOptionSyntax: dynamicOptionSyntax}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || gitOptions && arg == "--end-of-options" {
			if input.dynamicAt(i) {
				dynamicOptionSyntax = true
				return result(), fmt.Errorf("%s option boundary cannot be determined", command)
			}
			operands = append(operands, args[i+1:]...)
			for j := i + 1; j < len(args); j++ {
				operandDynamic = append(operandDynamic, input.dynamicAt(j))
				operandSplit = append(operandSplit, input.maySplitAt(j))
			}
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			if stopAtOperand {
				operands = append(operands, args[i:]...)
				for j := i; j < len(args); j++ {
					operandDynamic = append(operandDynamic, input.dynamicAt(j))
					operandSplit = append(operandSplit, input.maySplitAt(j))
				}
				break
			}
			dynamicOptions = dynamicOptions || input.maySplitAt(i) || arg == "" && input.dynamicAt(i)
			operands = append(operands, arg)
			operandDynamic = append(operandDynamic, input.dynamicAt(i))
			operandSplit = append(operandSplit, input.maySplitAt(i))
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, text, attached := strings.Cut(arg, "=")
			name, value, err := resolveLongOption(name, options, gitOptions)
			if err != nil {
				return result(), fmt.Errorf("%s: %w", command, err)
			}
			if input.dynamicAt(i) && !attached {
				dynamicOptionSyntax = true
				dynamicOptions = true
			}
			if input.maySplitAt(i) {
				dynamicOptions = true
			}
			if attached && (value == optionNoValue || value == optionRequiredSeparateValue || value == optionToggle) {
				return result(), fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			disabled := false
			if !attached && (value == optionBooleanValue || value == optionBooleanForce) {
				text = "true"
			}
			if attached && (value == optionBooleanValue || value == optionBooleanForce) {
				enabled, err := strconv.ParseBool(text)
				if err != nil {
					return result(), fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
				disabled = !enabled && value == optionBooleanForce
				text = strconv.FormatBool(enabled)
			}
			flags = appendCommandFlag(flags, commandFlag{name: name, token: arg, value: text, valueDynamic: input.dynamicAt(i), disabled: disabled}, value)
			if !attached && (value == optionRequiredValue || value == optionRequiredSeparateValue || value == optionRequiredNonEmptyValue || value == optionLastArgDefault && i+1 < len(args)) {
				i++
				if i == len(args) {
					return result(), fmt.Errorf("%s option %s requires a value", command, name)
				}
				flags[len(flags)-1].value = args[i]
				flags[len(flags)-1].valueDynamic = input.dynamicAt(i)
				dynamicOptions = dynamicOptions || input.maySplitAt(i)
				if value == optionRequiredNonEmptyValue && args[i] == "" && !input.dynamicAt(i) {
					return result(), fmt.Errorf("%s option %s requires a non-empty value", command, name)
				}
			}
			if attached && value == optionRequiredNonEmptyValue && text == "" && !input.dynamicAt(i) {
				return result(), fmt.Errorf("%s option %s requires a non-empty value", command, name)
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			name := "-" + string(arg[j])
			value, ok := options[name]
			if !ok {
				return result(), fmt.Errorf("unsupported %s option %s", command, name)
			}
			tokenDynamic := input.dynamicAt(i)
			if input.maySplitAt(i) {
				dynamicOptions = true
			}
			if tokenDynamic && value != optionRequiredValue && value != optionRequiredNonEmptyValue && value != optionAttachedValue {
				dynamicOptions = true
				dynamicOptionSyntax = true
			}
			disabled := false
			text := ""
			if value == optionBooleanValue || value == optionBooleanForce {
				text = "true"
			}
			booleanAttached := (value == optionBooleanValue || value == optionBooleanForce) && j+1 < len(arg) && arg[j+1] == '='
			if booleanAttached {
				enabled, err := strconv.ParseBool(arg[j+2:])
				if err != nil {
					return result(), fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
				disabled = !enabled && value == optionBooleanForce
				text = strconv.FormatBool(enabled)
			}
			flags = appendCommandFlag(flags, commandFlag{name: name, token: arg, value: text, valueDynamic: input.dynamicAt(i), disabled: disabled}, value)
			if value == optionRequiredValue || value == optionRequiredNonEmptyValue {
				var text string
				if j == len(arg)-1 {
					if tokenDynamic {
						text = ""
					} else {
						i++
					}
					if i == len(args) {
						return result(), fmt.Errorf("%s option %s requires a value", command, name)
					}
					if !tokenDynamic {
						text = args[i]
					}
				} else {
					text = arg[j+1:]
					text = strings.TrimPrefix(text, "=")
				}
				flags[len(flags)-1].value = text
				flags[len(flags)-1].valueDynamic = input.dynamicAt(i) || tokenDynamic
				dynamicOptions = dynamicOptions || input.maySplitAt(i)
				if value == optionRequiredNonEmptyValue && text == "" && !flags[len(flags)-1].valueDynamic {
					return result(), fmt.Errorf("%s option %s requires a non-empty value", command, name)
				}
				break
			}
			if value == optionAttachedValue {
				break
			}
			if booleanAttached {
				break
			}
		}
	}
	return result(), nil
}

func resolveLongOption(name string, options map[string]optionValue, abbreviate bool) (string, optionValue, error) {
	if value, ok := options[name]; ok {
		return name, value, nil
	}
	if abbreviate {
		match := ""
		for option := range options {
			if strings.HasPrefix(option, name) {
				if match != "" {
					return "", optionNoValue, fmt.Errorf("ambiguous option %s", name)
				}
				match = option
			}
		}
		if match != "" {
			return match, options[match], nil
		}
	}
	return "", optionNoValue, fmt.Errorf("unsupported option %s", name)
}

func appendCommandFlag(flags []commandFlag, flag commandFlag, value optionValue) []commandFlag {
	if value == optionBooleanForce {
		for i := range flags {
			if sameOverridingFlag(flags[i].name, flag.name) {
				flags[i].disabled = flag.disabled
			}
		}
	}
	if value == optionToggle {
		for i := range flags {
			if sameOverridingFlag(flags[i].name, flag.name) {
				flags[i].disabled = true
			}
		}
	}
	return append(flags, flag)
}

func sameOverridingFlag(a, b string) bool {
	if a == b {
		return true
	}
	return overridingFlagAlias(a) != "" && overridingFlagAlias(a) == overridingFlagAlias(b)
}

func overridingFlagAlias(name string) string {
	switch name {
	case "-f", "--force":
		return "force"
	case "-d", "--delete-branch":
		return "delete-branch"
	case "--amend", "--no-amend":
		return "amend"
	default:
		return ""
	}
}
