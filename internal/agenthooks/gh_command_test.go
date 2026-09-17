package agenthooks

import (
	"reflect"
	"testing"
)

func TestGhOptionPositionsReachProtectionRules(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		rule    string
	}{
		{`gh pr -R example/repository checkout 123 --force`, "deny-gh-pr-checkout-force"},
		{`gh pr -R example/repository merge 123`, "deny-gh-pr-merge"},
		{`gh pr -d list merge`, "deny-gh-pr-merge"},
		{`gh pr --delete-branch close merge`, "deny-gh-pr-merge"},
		{`gh release --draft edit create --notes example`, "deny-gh-release-create"},
		{`gh repo -h view edit --visibility public`, "deny-gh-repo-visibility"},
		{`gh repo --allow-forking view edit --visibility public`, "deny-gh-repo-visibility"},
		{`gh release --draft view create`, "deny-gh-release-create"},
		{`gh --force=true pr checkout 123`, "deny-gh-pr-checkout-force"},
		{`gh pr --force=true checkout 123`, "deny-gh-pr-checkout-force"},
		{`gh --branch merge pr checkout 123 --force`, "deny-gh-pr-checkout-force"},
		{`gh pr -fbmerge checkout 123`, "deny-gh-pr-checkout-force-short"},
		{`gh pr -R=example/repository -f=true co 123`, "deny-gh-pr-co-force-short"},
		{`gh -f=true co 123`, "deny-gh-co-force-short"},
		{`gh --body checkout pr merge 123`, "deny-gh-pr-merge"},
		{`gh pr --body -- merge 123`, "deny-gh-pr-merge"},
		{`gh pr --rebase=true update-branch 123`, "deny-gh-pr-update-branch"},
		{`gh issue -R owner/repo develop 12 --name feature`, "deny-gh-issue-develop"},
		{`gh pr --title revert revert 12`, "deny-gh-pr-revert"},
		{`gh --visibility public repo edit`, "deny-gh-repo-visibility"},
		{`gh repo -hmerge edit --visibility public`, "deny-gh-repo-visibility"},
		{`gh repo create owner/new --template owner/template`, "deny-gh-repo-create"},
		{`gh repo --source checkout sync example/repository`, "deny-gh-repo-sync"},
		{`gh repo fork owner/repo --clone=false`, "deny-gh-repo-fork"},
		{`gh --notes delete release create v1`, "deny-gh-release-create"},
		{`gh release --notes merge create v1`, "deny-gh-release-create"},
		{`gh release --cleanup-tag=true delete v1 --yes`, "deny-gh-release-delete"},
		{`gh --cleanup-tag=true release delete v1`, "deny-gh-release-delete"},
		{`gh alias --shell=true set example 'pr merge'`, "deny-gh-alias-set"},
		{`gh --clobber=true alias import aliases.yml`, "deny-gh-alias-import"},
		{`gh alias --all=true delete`, "deny-gh-alias-delete"},
		{`gh extension --help=false exec example --unknown merge`, "deny-gh-extension-exec"},
		{`gh workflow run --json`, "deny-gh-workflow-run"},
		{`gh agent-task create -R owner/repo -F task.md`, "deny-gh-agent-task-create"},
		{`gh codespace ssh -R owner/repo -c example -- ./publish.sh`, "deny-gh-codespace-ssh"},
		{`gh -X DELETE api repos/example/repository`, "deny-gh-api"},
		{`gh -fmerge=checkout api repos/example/repository`, "deny-gh-api"},
		{`gh --hostname example.invalid --method DELETE api repos/example/repository`, "deny-gh-api"},
		{`command env -u EXAMPLE nice -n 5 timeout -s TERM 5 gh pr -R example/repository merge 123`, "deny-gh-pr-merge"},
		{`sh -c 'gh pr --repo=example/repository checkout 123 --force'`, "deny-gh-pr-checkout-force"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || decision.Allowed || decision.RuleID != tc.rule {
				t.Fatalf("decision=%+v err=%v; want rule %s", decision, err, tc.rule)
			}
		})
	}
}

func TestGhOptionPositionsAllowNormalOperations(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`gh --json number pr view 123`,
		`gh --comments 123 pr view`,
		`gh issue list -h`,
		`gh pr view -h`,
		`gh pr --json number view 123`,
		`gh pr --web list view`,
		`gh pr -c list view`,
		`gh search code 'merge checkout'`,
		`gh pr -R example/repository view 123`,
		`gh pr --repo=example/repository view 123 --json number`,
		`gh --json merge pr view 123`,
		`gh pr --branch merge checkout 123`,
		`gh --branch checkout pr checkout 123`,
		`gh pr --branch --force checkout 123`,
		`gh pr -b--force checkout 123`,
		`gh pr -b=checkout checkout 123`,
		`gh pr --force=false checkout 123`,
		`gh --force=true pr -f=false checkout 123`,
		`gh pr --branch '' checkout 123 --force=false`,
		`gh pr checkout -- --force`,
		`gh pr view -- merge --force`,
		`gh repo --description --visibility edit`,
		`gh repo --description merge edit`,
		`gh repo -hcheckout edit`,
		`gh --enable-secret-scanning=false repo edit example/repository`,
		`gh --title merge pr create --body checkout`,
		`gh issue --title checkout create --body merge`,
		`gh --body-file merge pr edit 123 --title checkout`,
		`gh --json tagName release view v1`,
		`gh --json name repo view example/repository`,
		`gh alias list`,
		`gh extension list`,
		`gh extension --json name search example`,
		`gh pr ls --json number`,
		`gh repo autolink --json id view 123`,
		`gh run --json databaseId list`,
		`gh --json name workflow list`,
		`gh repo clone example/repository -- --depth 1`,
		`gh browse --commit --no-browser`,
		`gh stack link 123 456`,
		`env -u EXAMPLE nice -n 5 gh --json number pr view 123`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, command)
			if err != nil || !decision.Allowed {
				t.Fatalf("decision=%+v err=%v; want allow", decision, err)
			}
		})
	}
}

func TestGhParsingErrorsDenyBeforePolicyMatching(t *testing.T) {
	for _, protected := range []string{"git", "gh", "*"} {
		policy := Policy{Version: PolicyVersion, ID: "custom", Enabled: true,
			Rules: []Rule{{ID: "deny", Effect: EffectDeny, Match: Match{Argv: []ArgPattern{{Glob: protected}, {Exact: "push"}}}}},
		}
		for _, command := range []string{`gh my-query`, `gh pr view 123 --future-option`, `gh --future-option=value pr view 123`} {
			t.Run(protected+"/"+command, func(t *testing.T) {
				decision, err := EvaluateCommand([]Policy{policy}, command)
				if err != nil || decision.Allowed {
					t.Fatalf("decision=%+v err=%v", decision, err)
				}
			})
		}
	}
}

func TestGhUnknownOptionsAndUnresolvedValuesFailClosed(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`gh --not-real=value pr view 123`,
		`gh pr --not-real value view 123`,
		`gh pr view 123 --not-real`,
		`gh --not-real pr merge 123`,
		`gh pr --not-real checkout 123 --force`,
		`gh --json`, `gh pr --repo`, `gh --branch pr checkout`,
		`gh pr --force=invalid checkout 123`,
		`gh pr -xf checkout 123`,
		`gh repo --not-real edit --visibility public`,
		`gh release --not-real view v1`,
		`gh alias --not-real list`,
		`gh extension --not-real list`,
		`gh --config-dir=example pr view 123`,
		`gh pr --config-dir example view 123`,
		`gh -- pr merge 123`, `gh pr -- checkout 123 --force`,
		`gh pr -R "$repository" view 123`,
		`gh --json "$fields" pr view 123`,
		`gh pr "$operation" 123`,
		`nice -n 5 gh pr --force="$enabled" checkout 123`,
		`gh stack --repo example/repository link 123 456`,
		`gh --future-option=value done`,
		`gh --web pr view`,
		`gh pr --force checkout 123`,
		`gh 'pr view' 123`,
		`gh 'pr ' view 123`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, command)
			if err != nil || decision.Allowed || decision.Reason == "" {
				t.Fatalf("decision=%+v err=%v; want rejection", decision, err)
			}
		})
	}
}

func TestGhNormalizationPreservesOptionTokensAndValues(t *testing.T) {
	for _, tc := range []struct {
		command string
		argv    []string
	}{
		{`gh --repo '' pr create --body ''`, []string{"gh", "pr", "create", "--body", ""}},
		{`gh -Rexample/repository pr merge 123`, []string{"gh", "pr", "merge", "123"}},
		{`gh pr -R example/repository merge 123`, []string{"gh", "pr", "merge", "-R", "example/repository", "123"}},
		{`gh pr -d list merge`, []string{"gh", "pr", "merge", "-d", "list"}},
		{`gh pr --web list view`, []string{"gh", "pr", "view", "--web", "list"}},
		{`gh --json merge pr view checkout`, []string{"gh", "pr", "view", "--json", "merge", "checkout"}},
		{`gh --force=false pr -f=true checkout 123 --force`, []string{"gh", "pr", "checkout", "--force=false", "-f=true", "123", "--force"}},
		{`gh repo -d '' edit --enable-secret-scanning=false`, []string{"gh", "repo", "edit", "-d", "", "--enable-secret-scanning=false"}},
		{`gh pr --branch -- checkout 123 --force`, []string{"gh", "pr", "checkout", "--branch", "--", "123", "--force"}},
		{`gh pr view -- --json merge`, []string{"gh", "pr", "view", "--", "--json", "merge"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			invocations, err := ParseShellInvocations(tc.command)
			if err != nil || len(invocations) != 1 || invocations[0].Dynamic || !reflect.DeepEqual(invocations[0].Argv, tc.argv) {
				t.Fatalf("invocations=%+v err=%v; want %q", invocations, err, tc.argv)
			}
			again, err := parseGhCommand(tc.argv)
			if err != nil || !reflect.DeepEqual(again.argv, tc.argv) {
				t.Fatalf("normalization changed on second parse: %q err=%v", again.argv, err)
			}
		})
	}
}

func TestGhReorderedFlagsMatchCustomRules(t *testing.T) {
	for _, tc := range []struct {
		flag    string
		command string
	}{
		{"--enable-secret-scanning", `gh --enable-secret-scanning=false repo edit example/repository`},
		{"--enable-secret-scanning=false", `gh repo --enable-secret-scanning=false edit --enable-secret-scanning=true`},
		{"--force", `gh --force=true pr -f=true checkout 123`},
		{"-f", `gh -f=true pr --force=true checkout 123`},
		{"--force=false", `gh --force=false pr checkout 123 -f`},
		{"--json", `gh --json number pr view 123`},
		{"--repo", `gh pr --repo example/repository view 123`},
		{"--method", `gh --method GET api repos/example/repository`},
		{"--force", `gh extension exec example --force`},
	} {
		t.Run(tc.command, func(t *testing.T) {
			policy := Policy{Version: PolicyVersion, ID: "custom", Enabled: true,
				Rules: []Rule{{ID: "flag", Effect: EffectDeny, Match: Match{HasFlag: []string{tc.flag}}}},
			}
			decision, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || decision.Allowed || decision.RuleID != "flag" {
				t.Fatalf("decision=%+v err=%v; want custom flag rule", decision, err)
			}
		})
	}
}
