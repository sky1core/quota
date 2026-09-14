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
	optionAttachedValue
	optionLastArgDefault
	optionBooleanValue
)

type optionGroup struct {
	value optionValue
	names string
}

type commandFlag struct {
	name  string
	token string
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
}

var ghCheckoutOptions = optionValues(
	optionGroup{optionRequiredValue, `-b --branch -R --repo`},
	optionGroup{optionBooleanValue, `--detach -f --force --recurse-submodules -h --help`},
)

var ghRepoEditOptions = optionValues(
	optionGroup{optionRequiredValue, `--add-topic --default-branch -d --description -h --homepage --remove-topic
		--squash-merge-commit-message --visibility`},
	optionGroup{optionBooleanValue, `--accept-visibility-change-consequences --allow-forking --allow-update-branch
		--delete-branch-on-merge --enable-advanced-security --enable-auto-merge --enable-discussions --enable-issues
		--enable-merge-commit --enable-projects --enable-rebase-merge --enable-secret-scanning
		--enable-secret-scanning-push-protection --enable-squash-merge --enable-wiki --template --help`},
)

func commandFlags(argv []string) ([]commandFlag, bool, error) {
	if len(argv) < 2 {
		return nil, false, nil
	}
	name := commandName(argv[0])
	var options map[string]optionValue
	start := 2
	switch name {
	case "git":
		options = gitCommandOptions[argv[1]]
	case "gh":
		switch {
		case argv[1] == "co":
			options = ghCheckoutOptions
		case len(argv) >= 3 && argv[1] == "pr" && (argv[2] == "checkout" || argv[2] == "co"):
			options, start = ghCheckoutOptions, 3
		case len(argv) >= 3 && argv[1] == "repo" && argv[2] == "edit":
			options, start = ghRepoEditOptions, 3
		}
	}
	if options == nil {
		return nil, false, nil
	}
	command := strings.Join(append([]string{name}, argv[1:start]...), " ")
	flags, err := parseCommandFlags(command, argv[start:], options, name == "git")
	return flags, true, err
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
			if attached && value == optionNoValue {
				return nil, fmt.Errorf("%s option %s does not accept a value", command, name)
			}
			if attached && value == optionBooleanValue {
				if _, err := strconv.ParseBool(text); err != nil {
					return nil, fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
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
			flags = append(flags, commandFlag{name: name, token: arg})
			if !attached && (value == optionRequiredValue || value == optionLastArgDefault && i+1 < len(args)) {
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
			flags = append(flags, commandFlag{name: name, token: arg})
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
			if value == optionBooleanValue && j+1 < len(arg) && arg[j+1] == '=' {
				if _, err := strconv.ParseBool(arg[j+2:]); err != nil {
					return nil, fmt.Errorf("%s option %s requires a boolean value", command, name)
				}
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
