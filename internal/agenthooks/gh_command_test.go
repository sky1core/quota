package agenthooks

import (
	"fmt"
	"reflect"
	"testing"
)

func TestGhOptionPositionsAllowNormalOperations(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`gh --json number pr view 123`,
		`gh --future-option=value pr view 123`,
		`gh pr --not-real value view 123`,
		`gh pr -xf checkout 123`,
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
		`gh --title merge pr create --head feature --body checkout`,
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
		for _, command := range []string{`gh my-query`} {
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

		`gh --not-real pr merge 123`,
		`gh pr --not-real checkout 123 --force`,
		`gh --json`, `gh pr --repo`, `gh --branch pr checkout`,
		`gh pr --force=invalid checkout 123`,

		`gh repo --not-real edit --visibility public`,
		`gh release --not-real view v1`,
		`gh alias --not-real list`,
		`gh extension --not-real list`,

		`gh -- pr merge 123`, `gh pr -- checkout 123 --force`,

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

func TestGhRemoteMutationContract(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		remote, unknown bool
	}{
		{[]string{"pr", "checkout", "1", "--force"}, false, false},
		{[]string{"pr", "merge", "1"}, true, false},
		{[]string{"pr", "-R", "example/repo", "merge", "1"}, true, false},
		{[]string{"--body", "checkout", "pr", "merge", "1"}, true, false},
		{[]string{"pr", "--body", "--", "merge", "1"}, true, false},
		{[]string{"--cleanup-tag=true", "release", "delete", "v1"}, true, false},
		{[]string{"pr", "--disable-auto=true", "merge", "1"}, false, false},
		{[]string{"pr", "merge", "1", "--auto=false"}, true, false},
		{[]string{"pr", "merge", "1", "--disable-auto"}, false, false},
		{[]string{"pr", "merge", "1", "--disable-auto=false"}, true, false},
		{[]string{"pr", "merge", "1", "--disable-auto", "--auto=true"}, true, false},
		{[]string{"pr", "merge", "1", "--disable-auto", "--auto=false"}, false, false},
		{[]string{"pr", "close", "1"}, false, false},
		{[]string{"pr", "close", "1", "-d"}, true, false},
		{[]string{"pr", "close", "1", "-d=false"}, false, false},
		{[]string{"pr", "close", "1", "--delete-branch", "-d=false"}, false, false},
		{[]string{"pr", "create", "--head", "feature", "--body", "git push"}, false, false},
		{[]string{"pr", "new", "--head=feature"}, false, false},
		{[]string{"pr", "create", "--dry-run"}, true, false},
		{[]string{"pr", "create", "--head", ""}, false, true},
		{[]string{"pr", "update-branch", "1"}, true, false},
		{[]string{"pr", "revert", "1"}, true, false},
		{[]string{"issue", "develop", "1", "--list"}, false, false},
		{[]string{"issue", "develop", "1", "-l=false"}, true, false},
		{[]string{"issue", "develop", "1"}, true, false},
		{[]string{"release", "create", "v1", "--verify-tag"}, false, false},
		{[]string{"release", "create", "v1", "--verify-tag=false"}, true, false},
		{[]string{"release", "delete", "v1"}, false, false},
		{[]string{"release", "delete", "v1", "--cleanup-tag"}, true, false},
		{[]string{"release", "delete", "v1", "--cleanup-tag=false"}, false, false},
		{[]string{"release", "edit", "v1", "--notes", "git push"}, false, false},
		{[]string{"release", "edit", "v1", "--tag", "v2"}, true, false},
		{[]string{"release", "edit", "v1", "--tag", "v2", "--verify-tag"}, false, false},
		{[]string{"repo", "edit", "--visibility", "public"}, true, false},
		{[]string{"repo", "fork", "example/repo"}, true, false},
		{[]string{"repo", "sync", "example/repo"}, true, false},
		{[]string{"repo", "delete", "example/repo"}, true, false},
		{[]string{"repo", "create", "example/repo", "--private"}, true, false},
		{[]string{"repo", "create", "example/repo", "--private", "--push"}, true, false},
		{[]string{"repo", "create", "example/repo", "--template", "example/template"}, true, false},
		{[]string{"repo", "create"}, true, false},
		{[]string{"repo", "deploy-key", "add", "key.pub"}, true, false},
		{[]string{"repo", "deploy-key", "delete", "key.pub"}, true, false},
		{[]string{"secret", "set", "TOKEN", "--body", "value"}, true, false},
		{[]string{"secret", "set", "TOKEN", "--body", "value", "--no-store"}, false, false},
		{[]string{"secret", "set", "TOKEN", "--body", "value", "--no-store=false"}, true, false},
		{[]string{"variable", "set", "MODE", "--body", "dev"}, true, false},
		{[]string{"workflow", "disable", "ci.yml"}, true, false},
		{[]string{"alias", "set", "publish", "!git push"}, false, false},
		{[]string{"alias", "import", "aliases.yml"}, false, false},
		{[]string{"workflow", "run", "ci.yml"}, false, true},
		{[]string{"workflow", "view", "ci.yml"}, false, false},
		{[]string{"run", "rerun", "1"}, false, true},
		{[]string{"extension", "exec", "example", "--help"}, false, true},
		{[]string{"extension", "exec", "--help"}, false, false},
		{[]string{"codespace", "ssh", "--", ""}, false, true},
		{[]string{"codespace", "ssh", "--config"}, false, false},
		{[]string{"pr", "merge", "--help"}, false, false},
		{[]string{"--help", "pr", "merge"}, false, false},
		{[]string{"pr", "merge", "--help=false"}, true, false},
		{[]string{"pr", "merge", "--", "--help"}, true, false},
		{[]string{"pr", "view", ""}, false, false},
		{[]string{"", "pr", "view"}, false, true},
		{[]string{"pr", "", "view"}, false, true},
		{[]string{"api", ""}, false, true},
		{[]string{"pr", "view", "--future-option"}, false, false},
	} {
		t.Run(fmt.Sprint(tc.args), func(t *testing.T) {
			parsed, err := parseGhCommand(append([]string{"gh"}, tc.args...))
			if (err != nil || parsed.undecidable != "") != tc.unknown || (parsed.risk == "remote-code-ref-mutation") != tc.remote {
				t.Fatalf("parsed=%+v err=%v; remote=%v unknown=%v", parsed, err, tc.remote, tc.unknown)
			}
		})
	}
}

func TestGhShellContract(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allow   bool
		rule    string
	}{
		{`gh pr view "$NUM"`, true, ""},
		{`gh pr create --head topic --body "$BODY"`, true, ""},
		{`gh pr comment 1 --body 'git push'`, true, ""},
		{`gh --help pr merge`, true, ""},
		{`gh --help pr merge 1 --help=false`, false, ""},
		{`gh codespace ssh -- echo hello`, true, ""},
		{`gh codespace ssh -- git push`, false, ""},
		{`gh codespace ssh -- "$CMD"`, false, ""},
		{`gh auth token`, false, "deny-gh-auth-token"},
		{`gh auth status --show-token`, false, "deny-gh-auth-status-token-long"},
		{`gh auth status -t`, false, "deny-gh-auth-status-token-short"},
		{`gh auth --show-token status`, false, "deny-gh-auth-status-token-long"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || decision.Allowed != tc.allow || tc.rule != "" && decision.RuleID != tc.rule {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}
