package agenthooks

import (
	"encoding/json"
	"testing"
)

func TestCommandOptionValuesThroughHookEvent(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		command string
		ruleID  string
	}{
		{`git tag -a audit-tag -mfeature`, ""},
		{`git tag -a audit-tag -mdocs`, ""},
		{`git tag -a audit-tag -m --delete`, ""},
		{`git tag -am--force audit-tag`, ""},
		{`git tag -a audit-tag --message --delete`, ""},
		{`git tag -a audit-tag --mess=--force`, ""},
		{`git tag -a audit-tag --mess --delete`, ""},
		{`git tag -a audit-tag -F--force`, ""},
		{`git tag -a audit-tag --file --delete`, ""},
		{`git tag -a audit-tag --local-user --force -m message`, ""},
		{`git tag -a audit-tag --trailer --delete -m message`, ""},
		{`git tag -l --format --delete`, ""},
		{`git tag -l --format=--force`, ""},
		{`git tag -l --points-at`, ""},
		{`git tag -l --contains`, ""},
		{`git tag -l --no-contains`, ""},
		{`git tag -l --merged`, ""},
		{`git tag -l --no-merged`, ""},
		{`git tag -l --no-column --format ''`, ""},
		{`git tag -l -- --delete`, ""},
		{`git tag -a audit-tag -m '' --force`, "deny-git-tag-force-long"},
		{`git tag -a audit-tag --message= --delete`, "deny-git-tag-delete-long"},
		{`git tag -a audit-tag -m -- -f`, "deny-git-tag-force"},
		{`git tag -a audit-tag --message -- --delete`, "deny-git-tag-delete-long"},
		{`git tag -am--force audit-tag -f`, "deny-git-tag-force"},
		{`git tag -famessage audit-tag`, "deny-git-tag-force"},
		{`git tag -damessage audit-tag`, "deny-git-tag-delete"},
		{`git tag --del audit-tag`, "deny-git-tag-delete-long"},
		{`git tag --forc audit-tag`, "deny-git-tag-force-long"},
		{`git tag --column --delete audit-tag`, "deny-git-tag-delete-long"},
		{`git tag -n -d audit-tag`, "deny-git-tag-delete"},
		{`git tag --no-file -f audit-tag`, "deny-git-tag-force"},
		{`git branch -u--force feature`, ""},
		{`git branch --set-upstream-to --delete feature`, ""},
		{`git branch --format --move`, ""},
		{`git branch --forma=--force`, ""},
		{`git branch --format=--force`, ""},
		{`git branch --list --contains`, ""},
		{`git branch --list --no-merged`, ""},
		{`git branch --contains --force`, ""},
		{`git branch --contains -- --force feature`, "deny-git-branch-force-long"},
		{`git branch --list -- --delete`, ""},
		{`git branch --list --end-of-options`, ""},
		{`git branch --list --end-of-options --delete`, ""},
		{`git branch --delete --end-of-options feature`, "deny-git-branch-delete-long"},
		{`git branch --format --end-of-options --delete feature`, "deny-git-branch-delete-long"},
		{`git tag -l --end-of-options --delete`, ""},
		{`git tag -m --end-of-options --delete audit-tag`, "deny-git-tag-delete-long"},
		{`git commit --end-of-options --amend`, ""},
		{`git commit -m --end-of-options --amend`, "deny-git-commit-amend"},
		{`git switch --end-of-options -Cfeature`, ""},
		{`git checkout --end-of-options -Bfeature`, ""},
		{`git reset --end-of-options --hard`, ""},
		{`git branch -m old new`, "deny-git-branch-move"},
		{`git branch -qm old new`, "deny-git-branch-move"},
		{`git branch -M old new`, "deny-git-branch-move-force"},
		{`git branch -c old new`, "deny-git-branch-copy"},
		{`git branch -C old new`, "deny-git-branch-copy-force"},
		{`git branch --mov old new`, "deny-git-branch-move-long"},
		{`git branch --cop old new`, "deny-git-branch-copy-long"},
		{`git branch --format '' -df feature`, "deny-git-branch-force"},
		{`git branch --color -D feature`, "deny-git-branch-delete-force"},
		{`git branch -t -m old new`, "deny-git-branch-move"},
		{`git branch --track --copy old new`, "deny-git-branch-copy-long"},
		{`git switch -cfeatureC`, ""},
		{`git switch --create --force-create`, ""},
		{`git switch --cre=--force-create`, ""},
		{`git switch --orphan -C`, ""},
		{`git switch -- --force-create`, ""},
		{`git switch -Cfeature`, "deny-git-switch-force-create"},
		{`git switch -qCfeature`, "deny-git-switch-force-create"},
		{`git switch --force-c=feature`, "deny-git-switch-force-create-long"},
		{`git switch --create '' -Cfeature`, "deny-git-switch-force-create"},
		{`git switch --recurse-submodules -Cfeature`, "deny-git-switch-force-create"},
		{`git checkout -bfeatureB`, ""},
		{`git checkout --orphan -B`, ""},
		{`git checkout --pathspec-from-file -B`, ""},
		{`git checkout --pathspec-from-file=-B`, ""},
		{`git checkout -- -B`, ""},
		{`git checkout -Bfeature`, "deny-git-checkout-force-create"},
		{`git checkout -qBfeature`, "deny-git-checkout-force-create"},
		{`git checkout -b '' -Bfeature`, "deny-git-checkout-force-create"},
		{`git checkout --track -Bfeature`, "deny-git-checkout-force-create"},
		{`git reset --pathspec-from-file --hard`, ""},
		{`git reset --pathspec-from-file=--hard`, ""},
		{`git reset --pathspec-from --hard`, ""},
		{`git reset -- --hard`, ""},
		{`git reset --pathspec-from-file '' --hard`, "deny-git-reset-hard"},
		{`git reset --pathspec-from-file -- --har`, "deny-git-reset-hard"},
		{`git reset --recurse-submodules --hard`, "deny-git-reset-hard"},
		{`gh pr checkout 12 -bfeature`, ""},
		{`gh pr checkout 12 -b--force`, ""},
		{`gh pr checkout 12 --branch --force`, ""},
		{`gh pr checkout 12 --branch=--force`, ""},
		{`gh pr checkout 12 -b=--force`, ""},
		{`gh pr checkout 12 --repo --force`, ""},
		{`gh pr checkout 12 -R--force`, ""},
		{`gh pr co 12 -bfeature`, ""},
		{`gh co 12 -bfeature`, ""},
		{`gh pr checkout -- -f`, ""},
		{`gh pr checkout 12 -fbfeature`, "deny-gh-pr-checkout-force-short"},
		{`gh pr checkout 12 -bfeature -f`, "deny-gh-pr-checkout-force-short"},
		{`gh pr checkout 12 --branch= --force`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 12 -b= --force`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 12 --branch -- --force`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 12 --detach --force`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 12 --force=false`, ""},
		{`gh pr checkout 123 --force=true --force=false`, ""},
		{`gh pr checkout 123 -f --force=0`, ""},
		{`gh pr checkout 123 --force -f=FALSE`, ""},
		{`gh pr checkout 123 -ff=false`, ""},
		{`gh pr checkout 123 --force=false --force`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force=false -f`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 -f=false --force=TRUE`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force=1`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force false`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force --branch --force=false`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force -- --force=false`, "deny-gh-pr-checkout-force"},
		{`gh pr checkout 123 --force=false --branch --force`, ""},
		{`gh pr checkout 123 --force=false --detach=true`, ""},
		{`gh pr co 123 --force=false`, ""},
		{`gh co 123 -f=false`, ""},
		{`gh pr close 12 --delete-branch=false`, ""},
		{`gh pr close 12 --delete-branch=true`, "deny-gh-pr-close-delete-branch"},
		{`gh pr close 12 -d=false`, ""},
		{`gh pr close 12 -d --delete-branch=false`, ""},
		{`gh pr close 12 --delete-branch=false -d`, "deny-gh-pr-close-delete-branch"},
		{`git commit --no-t --dry-run --allow-empty -m example`, ""},
		{`git commit --no-tem --dry-run --allow-empty -m example`, ""},
		{`git commit --no-t --amend`, "deny-git-commit-amend"},
		{`git commit --trailer --amend -m example`, ""},

		{`gh pr checkout 12 -f=false`, ""},
		{`gh pr co 12 -b '' -f`, "deny-gh-pr-co-force-short"},
		{`gh co 12 -b '' -f`, "deny-gh-co-force-short"},
		{`gh repo edit --description --visibility`, ""},
		{`gh repo edit -d--visibility`, ""},
		{`gh repo edit --description=--visibility`, ""},
		{`gh repo edit --homepage --visibility`, ""},
		{`gh repo edit -h--visibility`, ""},
		{`gh repo edit -h= --visibility private`, "deny-gh-repo-visibility"},
		{`gh repo edit --enable-issues=false`, ""},
		{`gh repo edit -- --visibility`, ""},
		{`gh repo edit --description '' --visibility=private`, "deny-gh-repo-visibility"},
		{`gh repo edit --description -- --visibility private`, "deny-gh-repo-visibility"},
		{`gh repo edit --enable-issues --visibility private`, "deny-gh-repo-visibility"},
		{`command git -C . tag -amfeature audit-tag`, ""},
		{`sh -c 'git tag -m -- --delete audit-tag'`, "deny-git-tag-delete-long"},
		{`git tag -amfeature audit-tag && git push`, "deny-git-push"},
		{`git diff -M`, ""},
		{`git diff -C`, ""},
		{`git diff -B`, ""},
		{`git diff -U`, ""},
		{`git diff --unified`, ""},
		{`git diff --relative`, ""},
		{`git diff --relative=subdir`, ""},
		{`git diff --color=never --color-words`, ""},
		{`git diff --color-words='[[:alnum:]]+'`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": tt.command}})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := EvaluateHookEvent([]Policy{policy}, input)
			if err != nil || decision.Allowed != (tt.ruleID == "") || decision.RuleID != tt.ruleID {
				t.Fatalf("decision = %+v, err = %v, want rule=%q", decision, err, tt.ruleID)
			}
		})
	}
}

func TestCommandOptionParsingFailsClosed(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`git commit --no-trailer --dry-run`, `git commit --no-t=value`,
		`git tag -m`, `git tag --message`, `git tag --message=message --unknown`,
		`git tag --m message`, `git tag --for message`, `git tag --force=true`,
		`git tag --no-message --delete`, `git tag --no-trailer`,
		`git branch --format`, `git branch --for --force`, `git branch --for=--force`, `git branch --points-at`,
		`git branch --unknown -- --force`, `git branch -qmmessage old new`,
		`git switch --create`, `git switch --conflict`, `git switch --for feature`,
		`git switch --unknown -- -Cfeature`, `git checkout -b`,
		`git checkout --conflict`, `git checkout --unknown -- -Bfeature`,
		`git reset --pathspec-from-file`, `git reset --hard=true`, `git reset --unknown -- --hard`,
		`git log --future-option`,
		`git status --future-option`,
		`git clean -fd`,
		`gh pr checkout --branch`, `gh pr checkout --for`, `gh pr checkout --force=`,
		`gh pr checkout -f=invalid`, `gh pr checkout --unknown -- --force`,
		`gh pr co --branch`, `gh co --unknown`, `gh pr checkout --end-of-options`, `gh repo edit --description`,
		`gh repo edit --vis=private`, `gh repo edit --unknown -- --visibility`,
	} {
		t.Run(command, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"cmd": command}})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := EvaluateHookEvent([]Policy{policy}, input)
			if err != nil || decision.Allowed || decision.Reason == "" || decision.RuleID != "" {
				t.Fatalf("decision = %+v, err = %v, want parse rejection", decision, err)
			}
		})
	}
}

func TestCustomPolicyLiteralFlags(t *testing.T) {
	for _, tt := range []struct {
		flag    string
		command string
		matched bool
	}{
		{"--visibility=public", `gh repo edit --visibility=public`, true},
		{"--visibility=public", `gh repo edit --visibility=private`, false},
		{"--visibility=public", `gh repo edit --description --visibility=public`, false},
		{"--visibility=public", `gh repo edit -- --visibility=public`, false},
		{"-mfeature", `git tag -a audit-tag -mfeature`, true},
		{"-amfeature", `git tag -amfeature audit-tag`, true},
		{"-mfeature", `git tag -m -mfeature audit-tag`, false},
		{"--message=--force", `git tag --message=--force audit-tag`, true},
		{"--message=--force", `git tag --message --message=--force audit-tag`, false},
		{"--force", `git tag --message=--force audit-tag`, false},
		{"-f", `git tag -mfeature audit-tag`, false},
		{"-f=false", `gh pr checkout 12 -f=false`, true},
		{"--force=false", `gh pr checkout 12 --force=false`, true},
		{"--force=false", `gh pr checkout 12 --force=false -f`, true},
		{"-f=false", `gh pr checkout 12 -f=false --force`, true},
		{"--force=true", `gh pr checkout 12 --force=true -f=false`, true},
		{"--force", `gh pr checkout 12 --force=false`, false},
		{"--force", `gh pr checkout 12 --force -f`, true},
		{"-f", `gh pr checkout 12 -f --force`, true},
		{"--force", `gh pr checkout 12 --force -f=false`, false},
		{"-f", `gh pr checkout 12 -f --force=false`, false},
		{"-h", `gh repo edit -h https://example.invalid --help=true`, true},
		{"--enable-secret-scanning", `gh repo edit example/repository --enable-secret-scanning=false`, true},
		{"--enable-secret-scanning", `gh repo edit example/repository --enable-secret-scanning=true --enable-secret-scanning=false`, true},
		{"--enable-secret-scanning=false", `gh repo edit example/repository --enable-secret-scanning=false --enable-secret-scanning=true`, true},
		{"--enable-secret-scanning-push-protection", `gh repo edit example/repository --enable-secret-scanning-push-protection=false`, true},
		{"--amend", `git commit --ame --no-amend -m message`, false},
		{"--amend", `git commit --no-amend --ame -m message`, true},
	} {
		t.Run(tt.flag+"/"+tt.command, func(t *testing.T) {
			match := Match{HasFlag: []string{tt.flag}}
			for _, exception := range []bool{false, true} {
				rule := Rule{ID: "literal", Effect: EffectDeny, Match: match}
				if exception {
					rule.Match = Match{Argv: []ArgPattern{{Type: "nonempty"}}}
					rule.Except = []Match{match}
				}
				policy := Policy{Version: PolicyVersion, ID: "custom-options", Enabled: true, Rules: []Rule{rule}}
				if err := ValidatePolicy(policy); err != nil {
					t.Fatal(err)
				}
				decision, err := EvaluateCommand([]Policy{policy}, tt.command)
				wantAllow := tt.matched == exception
				if err != nil || decision.Allowed != wantAllow {
					t.Fatalf("exception=%v: decision = %+v, err = %v, want allowed=%v", exception, decision, err, wantAllow)
				}
			}
		})
	}
}

func TestCustomPolicyOptionParsingErrors(t *testing.T) {
	for _, effect := range []string{EffectAllow, EffectDeny} {
		for _, exception := range []bool{false, true} {
			rule := Rule{ID: "global-flag", Effect: effect, Match: Match{HasFlag: []string{"--delete"}}}
			if exception {
				rule.Match = Match{Contains: []ArgPattern{{Exact: "branch"}}}
				rule.Except = []Match{{HasFlag: []string{"--list"}}}
			}
			policy := Policy{Version: PolicyVersion, ID: "custom-options", Enabled: true, Rules: []Rule{rule}}
			if err := ValidatePolicy(policy); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{
				`git branch --delete --unknown feature`,
				`git branch --unknown --list`,
				`git branch --format`,
			} {
				decision, err := EvaluateCommand([]Policy{policy}, command)
				if err != nil || decision.Allowed || decision.Reason == "" || decision.RuleID != "" {
					t.Fatalf("effect=%s exception=%v command=%s: decision = %+v, err = %v, want parse rejection", effect, exception, command, decision, err)
				}
			}
		}
	}
}

func TestCustomPolicyFlagValuesAndExceptions(t *testing.T) {
	var policy Policy
	if err := json.Unmarshal([]byte(`{
		"version":1,"id":"custom-options","enabled":true,"rules":[
			{"id":"tag-list-only","effect":"deny","match":{"argv":[{"exact":"git"},{"exact":"tag"}]},
			 "except":[{"hasFlag":["--list"]}]},
			{"id":"custom-tool-force","effect":"deny","match":{"argv":[{"exact":"custom-tool"}],"hasFlag":["-f"]}}
		]}`), &policy); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicy(policy); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		command string
		allowed bool
	}{
		{`git tag --list`, true},
		{`git tag --message --list`, false},
		{`git tag -- --list`, false},
		{`git tag --unknown --list`, false},
		{`custom-tool -vf`, false},
		{`custom-tool -v`, true},
	} {
		t.Run(tt.command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, tt.command)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
	policy.Enabled = false
	decision, err := EvaluateCommand([]Policy{policy}, `git tag --unknown`)
	if err != nil || decision.Allowed || decision.RuleID != "" {
		t.Fatalf("unsupported syntax must be denied independently of disabled policy: decision = %+v, err = %v", decision, err)
	}
}
