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
	optionAttachedValue
	optionLastArgDefault
	optionBooleanValue
	optionBooleanForce
)

type optionGroup struct {
	value optionValue
	names string
}

type commandFlag struct {
	name     string
	token    string
	disabled bool
	literal  bool
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
	argv        []string
	flags       []commandFlag
	undecidable string
	dynamic     bool
}

func parseCommand(argv []string) parsedCommand {
	parsed := parsedCommand{argv: append([]string(nil), argv...)}
	if len(argv) == 0 {
		return parsed
	}
	name := commandName(argv[0])
	if subcommand, ok := gitDashedSubcommand(name); ok {
		parsed.argv = append([]string{"git", subcommand}, argv[1:]...)
		name = "git"
	}
	switch name {
	case "git":
		parsed.argv[0] = name
		var unknown bool
		parsed.argv, unknown, parsed.undecidable = normalizeGitGlobalOptions(parsed.argv)
		parsed.dynamic = unknown
		if unknown || len(parsed.argv) < 2 {
			return parsed
		}
		if subcommand := parsed.argv[1]; subcommand != "" && !strings.HasPrefix(subcommand, "-") && !knownGitSubcommand(subcommand) {
			parsed.undecidable = "unknown git subcommand can resolve to a git alias or external helper"
			return parsed
		}
		options, ok := gitCommandOptions[parsed.argv[1]]
		if !ok {
			options = gitDefaultSubcommandOptions
			if !argvHasOption(parsed.argv[2:]) {
				return parsed
			}
			flags, err := parseCommandFlags("git "+parsed.argv[1], parsed.argv[2:], options, true)
			parsed.flags = flags
			if err != nil {
				parsed.undecidable = err.Error()
			}
			return parsed
		}
		flags, err := parseCommandFlags("git "+parsed.argv[1], parsed.argv[2:], options, true)
		parsed.flags = flags
		if err != nil {
			parsed.undecidable = err.Error()
		}
	case "gh":
		parsed.argv[0] = name
		normalized, err := parseGhCommand(parsed.argv)
		if err != nil {
			parsed.dynamic = true
			parsed.undecidable = err.Error()
		} else {
			parsed = normalized
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
	var flags []commandFlag
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || gitOptions && arg == "--end-of-options" {
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, text, attached := strings.Cut(arg, "=")
			name, value, err := resolveLongOption(name, options, gitOptions)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", command, err)
			}
			if attached && (value == optionNoValue || value == optionRequiredSeparateValue) {
				return nil, fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			disabled := false
			if attached && (value == optionBooleanValue || value == optionBooleanForce) {
				enabled, err := strconv.ParseBool(text)
				if err != nil {
					return nil, fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
				disabled = !enabled && value == optionBooleanForce
			}
			if command == "git commit" && (name == "--amend" || name == "--no-amend") {
				active := flags[:0]
				for _, flag := range flags {
					if flag.name != "--amend" && flag.name != "--no-amend" {
						active = append(active, flag)
					}
				}
				flags = active
			}
			flags = appendCommandFlag(flags, commandFlag{name: name, token: arg, disabled: disabled}, value)
			if !attached && (value == optionRequiredValue || value == optionRequiredSeparateValue || value == optionLastArgDefault && i+1 < len(args)) {
				i++
				if i == len(args) {
					return nil, fmt.Errorf("%s option %s requires a value", command, name)
				}
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			name := "-" + string(arg[j])
			value, ok := options[name]
			if !ok {
				return nil, fmt.Errorf("unsupported %s option %s", command, name)
			}
			disabled := false
			booleanAttached := (value == optionBooleanValue || value == optionBooleanForce) && j+1 < len(arg) && arg[j+1] == '='
			if booleanAttached {
				enabled, err := strconv.ParseBool(arg[j+2:])
				if err != nil {
					return nil, fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
				disabled = !enabled && value == optionBooleanForce
			}
			flags = appendCommandFlag(flags, commandFlag{name: name, token: arg, disabled: disabled}, value)
			if value == optionRequiredValue {
				if j == len(arg)-1 {
					i++
					if i == len(args) {
						return nil, fmt.Errorf("%s option %s requires a value", command, name)
					}
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
	return flags, nil
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
			if sameBooleanForceFlag(flags[i].name, flag.name) {
				flags[i].disabled = flag.disabled
			}
		}
	}
	return append(flags, flag)
}

func sameBooleanForceFlag(a, b string) bool {
	if a == b {
		return true
	}
	return booleanForceFlagAlias(a) != "" && booleanForceFlagAlias(a) == booleanForceFlagAlias(b)
}

func booleanForceFlagAlias(name string) string {
	switch name {
	case "-f", "--force":
		return "force"
	case "-d", "--delete-branch":
		return "delete-branch"
	default:
		return ""
	}
}
