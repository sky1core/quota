package agenthooks

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubHistoryGuardPresetTestsPass(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	results := RunPolicyTests([]Policy{policy})
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s: got decision=%s rule=%s source=%s want decision=%s rule=%s source=%s error=%s", result.Name, result.Got, result.RuleID, result.Source, result.Want, findPresetTestRule(policy, result.Name), findPresetTestSource(policy, result.Name), result.Error)
		}
	}
}

func TestGitHubHistoryGuardPresetGroups(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for _, group := range policy.Groups {
		groups[group.ID] = true
	}
	for _, group := range []string{PolicyGroupRemoteCodeRefMutation, PolicyGroupLocalSystemSecretSafety, PolicyGroupGitHubCollaborationMetadata} {
		if !groups[group] {
			t.Fatalf("missing group %s", group)
		}
	}
	for _, rule := range policy.Rules {
		switch rule.ID {
		case "deny-rm", "deny-rmdir", "deny-unlink", "deny-mkfs-family", "deny-newfs-family", "deny-fdisk",
			"deny-dd-input", "deny-dd-output", "deny-sudo", "deny-doas", "deny-su", "deny-shutdown",
			"deny-reboot", "deny-poweroff", "deny-halt", "deny-init-stop", "deny-init-reboot",
			"deny-killall", "deny-pkill", "deny-kill-multiple-pids", "deny-kill-multiple-pids-with-signal",
			"deny-kill-zero-pid", "deny-kill-negative-pid", "deny-kill-negative-pid-after-end", "deny-chmod", "deny-chown",
			"deny-chgrp", "deny-gh-auth-token", "deny-gh-auth-status-token-long", "deny-gh-auth-status-token-short":
			if rule.Group != PolicyGroupLocalSystemSecretSafety {
				t.Fatalf("rule %s group = %q, want %q", rule.ID, rule.Group, PolicyGroupLocalSystemSecretSafety)
			}
		default:
			if rule.Group != PolicyGroupRemoteCodeRefMutation {
				t.Fatalf("rule %s group = %q, want %q", rule.ID, rule.Group, PolicyGroupRemoteCodeRefMutation)
			}
		}
	}
	wantTests := map[string]string{
		"deny git push":                        PolicyGroupRemoteCodeRefMutation,
		"deny git http push":                   PolicyGroupRemoteCodeRefMutation,
		"deny git bisect run":                  PolicyGroupRemoteCodeRefMutation,
		"deny git submodule foreach":           PolicyGroupRemoteCodeRefMutation,
		"deny rm":                              PolicyGroupLocalSystemSecretSafety,
		"deny mkfs variant":                    PolicyGroupLocalSystemSecretSafety,
		"deny sudo":                            PolicyGroupLocalSystemSecretSafety,
		"deny command sudo":                    PolicyGroupLocalSystemSecretSafety,
		"deny killall":                         PolicyGroupLocalSystemSecretSafety,
		"allow kill single pid":                PolicyGroupLocalSystemSecretSafety,
		"deny kill multiple pids":              PolicyGroupLocalSystemSecretSafety,
		"deny gh auth token":                   PolicyGroupLocalSystemSecretSafety,
		"deny gh auth status show token":       PolicyGroupLocalSystemSecretSafety,
		"deny gh pr merge":                     PolicyGroupRemoteCodeRefMutation,
		"deny gh pr update branch":             PolicyGroupRemoteCodeRefMutation,
		"deny gh issue develop":                PolicyGroupRemoteCodeRefMutation,
		"deny gh pr revert":                    PolicyGroupRemoteCodeRefMutation,
		"deny pr create without explicit head": PolicyGroupRemoteCodeRefMutation,
		"deny gh pr close delete branch":       PolicyGroupRemoteCodeRefMutation,
		"deny gh repo create":                  PolicyGroupRemoteCodeRefMutation,
		"deny gh repo fork":                    PolicyGroupRemoteCodeRefMutation,
		"deny gh repo delete":                  PolicyGroupRemoteCodeRefMutation,

		"deny gh workflow run":                 PolicyGroupRemoteCodeRefMutation,
		"deny gh run rerun":                    PolicyGroupRemoteCodeRefMutation,
		"deny gh agent task create":            PolicyGroupRemoteCodeRefMutation,
		"deny gh codespace ssh":                PolicyGroupRemoteCodeRefMutation,
		"deny gh stack other":                  PolicyGroupRemoteCodeRefMutation,
		"allow pr create with explicit head":   PolicyGroupGitHubCollaborationMetadata,
		"allow pr close without branch delete": PolicyGroupGitHubCollaborationMetadata,
		"allow pr comment":                     PolicyGroupGitHubCollaborationMetadata,
		"allow issue comment":                  PolicyGroupGitHubCollaborationMetadata,
		"allow issue edit":                     PolicyGroupGitHubCollaborationMetadata,
		"allow issue view":                     PolicyGroupGitHubCollaborationMetadata,
		"allow gh stack link ints":             PolicyGroupGitHubCollaborationMetadata,
	}
	for _, test := range policy.Tests {
		if want, ok := wantTests[test.Name]; ok {
			if test.Group != want {
				t.Fatalf("test %s group = %q, want %q", test.Name, test.Group, want)
			}
			delete(wantTests, test.Name)
		}
	}
	if len(wantTests) > 0 {
		t.Fatalf("missing grouped tests: %+v", wantTests)
	}
}

func TestRunPolicyTestsSkipsDisabledPolicies(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	if results := RunPolicyTests([]Policy{policy}); len(results) != 0 {
		t.Fatalf("results = %+v, want none", results)
	}
}

func TestRunPolicyTestsChecksDecisionSource(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "source-check",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push")},
		}},
		Tests: []TestCase{
			{Name: "direct rule", Command: `git push origin main`, Want: DecisionDeny, RuleID: "deny-push", Source: string(decisionSourceRule)},
			{Name: "literal data", Command: `echo 'git push origin main'`, Want: DecisionAllow, Source: string(decisionSourceAllow)},
			{Name: "undecidable", Command: `$cmd push origin main`, Want: DecisionDeny, Source: string(decisionSourceUndecidable)},
			{Name: "wrong source", Command: `git push origin main`, Want: DecisionDeny, RuleID: "deny-push", Source: string(decisionSourceUndecidable)},
		},
	}
	results := RunPolicyTests([]Policy{policy})
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	for _, i := range []int{0, 1, 2} {
		if !results[i].Passed {
			t.Fatalf("%s: result = %+v, want pass", results[i].Name, results[i])
		}
	}
	if results[3].Passed {
		t.Fatalf("%s: result = %+v, want source mismatch failure", results[3].Name, results[3])
	}
}

func TestRiskMatchRespectsRuleOrderAndExcept(t *testing.T) {
	tests := []struct {
		name   string
		rules  []Rule
		want   string
		ruleID string
	}{
		{
			name: "allow before kill risk deny",
			rules: []Rule{
				{ID: "allow-test-command", Effect: EffectAllow, Match: Match{Argv: []ArgPattern{{Exact: "kill"}, {Exact: "12345"}, {Exact: "23456"}}, Exact: true}},
				{ID: "deny-kill-multiple-pids", Effect: EffectDeny, Match: Match{Argv: exactArgs("kill"), Risk: riskKillMultiplePIDs}},
			},
			want:   DecisionAllow,
			ruleID: "allow-test-command",
		},
		{
			name: "except on kill risk deny",
			rules: []Rule{
				{
					ID:     "deny-kill-multiple-pids",
					Effect: EffectDeny,
					Match:  Match{Argv: exactArgs("kill"), Risk: riskKillMultiplePIDs},
					Except: []Match{{Argv: []ArgPattern{{Exact: "kill"}, {Exact: "12345"}, {Exact: "23456"}}, Exact: true}},
				},
			},
			want: DecisionAllow,
		},
	}
	for _, tt := range tests {
		policy := Policy{Version: PolicyVersion, ID: "risk-order", Enabled: true, Rules: tt.rules}
		decision, err := EvaluateCommand([]Policy{policy}, `kill 12345 23456`)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if decision.Decision != tt.want || decision.RuleID != tt.ruleID {
			t.Fatalf("%s: decision = %+v, want decision=%s rule=%s", tt.name, decision, tt.want, tt.ruleID)
		}
	}
}

func TestDynamicLocalRiskDependsOnActiveRules(t *testing.T) {
	remoteOnly := Policy{
		Version: PolicyVersion,
		ID:      "remote-only",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-remote-gh",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("gh"), Risk: PolicyGroupRemoteCodeRefMutation},
		}},
	}
	for _, command := range []string{`kill "$PID"`, `dd if="$INPUT" of="$OUTPUT"`} {
		decision, err := EvaluateCommand([]Policy{remoteOnly}, command)
		if err != nil || !decision.Allowed {
			t.Fatalf("%s: decision=%+v err=%v, want allow without local safety rules", command, decision, err)
		}
	}

	killPolicy := Policy{
		Version: PolicyVersion,
		ID:      "kill-risk",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-kill-multiple",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("kill"), Risk: riskKillMultiplePIDs},
		}},
	}
	decision, err := EvaluateCommand([]Policy{killPolicy}, `kill "$PID"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("kill dynamic risk: decision=%+v err=%v, want undecidable deny", decision, err)
	}
}

func TestDynamicRiskAndContainsBothChecked(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "risk-and-contains",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-remote-push-main",
			Effect: EffectDeny,
			Match: Match{
				Argv:     exactArgs("git", "push"),
				Risk:     PolicyGroupRemoteCodeRefMutation,
				Contains: []ArgPattern{{Exact: "main"}},
			},
		}},
	}

	// Risk matches statically, but the Contains token is a dynamic argument that
	// could resolve to "main". The rule's firing cannot be confirmed, so it must
	// be undecidable rather than silently allowed.
	decision, err := EvaluateCommand([]Policy{policy}, `git push origin "$REF"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("dynamic contains: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	// Risk matches, but the Contains token is definitely absent (no static match,
	// no dynamic argument). The AND-term cannot hold, so the rule does not fire.
	decision, err = EvaluateCommand([]Policy{policy}, `git push origin feature`)
	if err != nil || !decision.Allowed {
		t.Fatalf("contains absent: decision=%+v err=%v, want allow", decision, err)
	}
}

func TestDynamicArgvLaterStaticMismatchEliminatesMatch(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "docker-stop-prod",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-docker-stop-prod",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("docker", "stop", "prod"), Exact: true},
		}},
	}

	// A later static argument ("staging") definitively mismatches the rule, so the
	// quoted (non-splitting) dynamic argument cannot make the rule fire: allow.
	decision, err := EvaluateCommand([]Policy{policy}, `docker "$ACTION" staging`)
	if err != nil || !decision.Allowed {
		t.Fatalf("later static mismatch: decision=%+v err=%v, want allow", decision, err)
	}

	// Every static argument is consistent and the dynamic argument may resolve to
	// "stop", so the rule may fire: undecidable.
	decision, err = EvaluateCommand([]Policy{policy}, `docker "$ACTION" prod`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("dynamic command word: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	// An unquoted expansion can split to zero words, so a later static token cannot
	// be trusted to hold its position: the rule may still fire, so undecidable.
	decision, err = EvaluateCommand([]Policy{policy}, `docker $E stop prod`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("split before static: decision=%+v err=%v, want undecidable deny", decision, err)
	}
}

func TestGhLeadingOptionReachesFlags(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "gh-repo-flag",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-gh-pr-view-repo",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("gh", "pr", "view"), HasFlag: []string{"--repo"}},
		}},
	}

	decision, err := EvaluateCommand([]Policy{policy}, `gh --repo o/r pr view 1`)
	if err != nil || decision.Allowed {
		t.Fatalf("leading --repo: decision=%+v err=%v, want deny", decision, err)
	}

	decision, err = EvaluateCommand([]Policy{policy}, `gh pr view 1`)
	if err != nil || !decision.Allowed {
		t.Fatalf("no --repo: decision=%+v err=%v, want allow", decision, err)
	}
}

func TestDynamicExceptDoesNotApplyWhenRuleMatchMisses(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "push-only",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push")},
			Except: []Match{{Contains: []ArgPattern{{Exact: "--dry-run"}}}},
		}},
	}
	decision, err := EvaluateCommand([]Policy{policy}, `git commit -m "$message"`)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v, want allow for unrelated rule", decision, err)
	}

	policy.Rules[0].Match = Match{Argv: exactArgs("git", "commit"), HasFlag: []string{"--amend"}}
	decision, err = EvaluateCommand([]Policy{policy}, `git commit -m "$message"`)
	if err != nil || !decision.Allowed {
		t.Fatalf("flag miss: decision=%+v err=%v, want allow for unrelated except", decision, err)
	}
}

func TestDynamicMetadataSurvivesNormalization(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "push-main",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-main-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push"), Contains: []ArgPattern{{Exact: "main"}}},
		}},
	}
	decision, err := EvaluateCommand([]Policy{policy}, `command git -C . push origin "$REF"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("decision=%+v err=%v, want undecidable deny for normalized dynamic ref", decision, err)
	}
}

func TestDynamicExceptCannotUseTentativeExactMatch(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "push-except",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push")},
			Except: []Match{{Contains: []ArgPattern{{Exact: "refs/heads/safe"}}}},
		}},
	}
	decision, err := EvaluateCommand([]Policy{policy}, `git push origin "refs/heads/safe${SUFFIX}"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("decision=%+v err=%v, want undecidable deny for dynamic except token", decision, err)
	}

	policy.Rules[0].Except = []Match{{Argv: exactArgs("git", "push", "origin", "refs/heads/safe"), Exact: true}}
	decision, err = EvaluateCommand([]Policy{policy}, `git push origin "refs/heads/safe${SUFFIX}"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("argv except: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	policy.Rules[0].Except = []Match{{Contains: []ArgPattern{{Type: "int"}}}}
	decision, err = EvaluateCommand([]Policy{policy}, `git push origin "123${SUFFIX}"`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("int except: decision=%+v err=%v, want undecidable deny", decision, err)
	}
}

func TestDynamicMatchWithStaticExceptExcluded(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "force-push-except",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-force-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push"), Contains: []ArgPattern{{Exact: "--force"}}},
			Except: []Match{{Contains: []ArgPattern{{Exact: "--dry-run"}}}},
		}},
	}

	// The match is dynamically uncertain ($X could be "--force"), but the except
	// token "--dry-run" is statically present and definitely excludes the rule, so
	// it cannot fire: allow rather than undecidable over-block.
	decision, err := EvaluateCommand([]Policy{policy}, `git push "$X" --dry-run`)
	if err != nil || !decision.Allowed {
		t.Fatalf("static except with uncertain match: decision=%+v err=%v, want allow", decision, err)
	}
}

func TestDynamicRiskWithDynamicContainsOrFlagUndecidable(t *testing.T) {
	// A rule that restricts a risk together with a Contains token or HasFlag must
	// not be eliminated just because the token is not statically present: risk
	// applicability is judged from the command's own scope, and a push-family
	// command with a dynamic argument keeps the risk uncertain.
	contains := Policy{
		Version: PolicyVersion, ID: "risk-contains", Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push-main",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push"), Risk: PolicyGroupRemoteCodeRefMutation, Contains: []ArgPattern{{Exact: "main"}}},
		}},
	}
	// git push --dry-run reads as non-mutating, but an unquoted expansion could add
	// "--no-dry-run" and "main" and make it a real push to a protected ref.
	decision, err := EvaluateCommand([]Policy{contains}, `git push --dry-run $ARGS`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("risk+contains: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	hasFlag := Policy{
		Version: PolicyVersion, ID: "risk-hasflag", Enabled: true,
		Rules: []Rule{{
			ID:     "deny-force-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push"), Risk: PolicyGroupRemoteCodeRefMutation, HasFlag: []string{"--force"}},
		}},
	}
	decision, err = EvaluateCommand([]Policy{hasFlag}, `git push --dry-run $ARGS`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("risk+hasflag: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	// A dynamic argument in a later Argv position must not be compared as a
	// placeholder and eliminate the rule: risk applicability is judged from the
	// command scope, so the risk stays uncertain.
	argvPos := Policy{
		Version: PolicyVersion, ID: "risk-argv", Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push-origin",
			Effect: EffectDeny,
			Match:  Match{Argv: []ArgPattern{{Exact: "git"}, {Exact: "push"}, {Glob: "*"}, {Exact: "origin"}}, Risk: PolicyGroupRemoteCodeRefMutation},
		}},
	}
	decision, err = EvaluateCommand([]Policy{argvPos}, `git push --dry-run $ARGS`)
	if err != nil || decision.Allowed || decision.source != decisionSourceUndecidable {
		t.Fatalf("risk+dynamic argv: decision=%+v err=%v, want undecidable deny", decision, err)
	}

	// Risk applicability comes from the command's own scope, so a rule with no Argv
	// that restricts the remote risk must not block commands that cannot carry it.
	// A dynamic argument on a non-push command is allowed, not over-blocked.
	openScope := Policy{
		Version: PolicyVersion, ID: "risk-open", Enabled: true,
		Rules: []Rule{{
			ID:     "deny-remote-main",
			Effect: EffectDeny,
			Match:  Match{Risk: PolicyGroupRemoteCodeRefMutation, Contains: []ArgPattern{{Exact: "main"}}},
		}},
	}
	for _, command := range []string{`echo "$X"`, `git log "$BRANCH"`} {
		decision, err = EvaluateCommand([]Policy{openScope}, command)
		if err != nil || !decision.Allowed {
			t.Fatalf("non-remote under remote risk: %s decision=%+v err=%v, want allow", command, decision, err)
		}
	}
}

func TestKillZeroDoesNotMaskExistingRiskPolicies(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "old-kill-policy",
		Enabled: true,
		Rules: []Rule{
			{ID: "deny-kill-multiple-pids", Effect: EffectDeny, Match: Match{Argv: exactArgs("kill"), Risk: riskKillMultiplePIDs}},
			{ID: "deny-kill-multiple-pids-with-signal", Effect: EffectDeny, Match: Match{Argv: exactArgs("kill"), Risk: riskKillMultiplePIDsWithSignal}},
			{ID: "deny-kill-negative-pid", Effect: EffectDeny, Match: Match{Argv: exactArgs("kill"), Risk: riskKillNegativePID}},
			{ID: "deny-kill-negative-pid-after-end", Effect: EffectDeny, Match: Match{Argv: exactArgs("kill"), Risk: riskKillNegativePIDAfterEnd}},
		},
	}
	tests := []struct {
		command string
		ruleID  string
	}{
		{`kill 12345 0`, "deny-kill-multiple-pids"},
		{`kill -TERM 12345 0`, "deny-kill-multiple-pids-with-signal"},
		{`kill -- 0 -12345`, "deny-kill-negative-pid-after-end"},
	}
	for _, tt := range tests {
		decision, err := EvaluateCommand([]Policy{policy}, tt.command)
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if decision.Decision != DecisionDeny || decision.RuleID != tt.ruleID {
			t.Fatalf("%s: decision = %+v, want deny %s", tt.command, decision, tt.ruleID)
		}
	}
}

func TestRiskMatchIsScopedToKillCommand(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "risk-scope",
		Enabled: true,
		Rules: []Rule{
			{ID: "allow-risk", Effect: EffectAllow, Match: Match{Risk: riskKillMultiplePIDs}},
			{ID: "deny-push", Effect: EffectDeny, Match: Match{Argv: exactArgs("git", "push")}},
		},
	}
	decision, err := EvaluateCommand([]Policy{policy}, `git push origin 12345 23456`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != DecisionDeny || decision.RuleID != "deny-push" {
		t.Fatalf("decision = %+v, want deny-push", decision)
	}

	policy.Rules = []Rule{{ID: "deny-risk", Effect: EffectDeny, Match: Match{Risk: riskKillMultiplePIDs}}}
	decision, err = EvaluateCommand([]Policy{policy}, `echo 12345 23456`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != DecisionAllow {
		t.Fatalf("decision = %+v, want allow", decision)
	}
}

func TestLiteralExceptDoesNotIgnoreRisk(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "literal-risk-except",
		Enabled: true,
		Rules: []Rule{{
			ID:     "deny-push",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("git", "push")},
			Except: []Match{{Argv: exactArgs("git", "push"), Risk: riskKillMultiplePIDs}},
		}},
	}
	for _, command := range []string{`git push origin main`} {
		decision, err := EvaluateCommand([]Policy{policy}, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if decision.Decision != DecisionDeny || decision.RuleID != "deny-push" {
			t.Fatalf("%s: decision = %+v, want deny-push", command, decision)
		}
	}
}

func TestCommandFromHookEventAcceptsCommandAndCmd(t *testing.T) {
	for _, key := range []string{"command", "cmd"} {
		b, err := json.Marshal(map[string]any{
			"tool_input": map[string]any{key: "git push origin main"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := CommandFromHookEvent(b)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != "git push origin main" {
			t.Fatalf("%s: command = %q", key, got)
		}
	}
}

func TestParseShellInvocationsNormalizesWrappers(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "absolute command", command: filepath.Join(string(filepath.Separator), "usr", "bin", "git") + " push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "dashed git helper", command: filepath.Join(string(filepath.Separator), "usr", "libexec", "git-core", "git-push") + " origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env wrapper", command: "env FOO=bar git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env path wrapper", command: "env -P /usr/bin git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env inline path wrapper", command: "env -P/usr/bin git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env inline unset wrapper", command: "env -uPATH git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env grouped unset wrapper", command: "env -iuPATH git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo wrapper", command: "sudo -u nobody git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo env assignment wrapper", command: "sudo FOO=bar git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo chdir wrapper", command: "sudo -D /tmp git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo inline user wrapper", command: "sudo -unobody git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "nested shell", command: "sh -c 'git push origin main && gh pr merge 7'", want: [][]string{{"git", "push", "origin", "main"}, {"gh", "pr", "merge", "7"}}},
		{name: "gh inline repo option", command: "gh -Rcli/cli pr merge 7", want: [][]string{{"gh", "pr", "merge", "7"}}},
		{name: "quoted literal", command: `gh pr create --title "hello world"`, want: [][]string{{"gh", "pr", "create", "--title", "hello world"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShellInvocations(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			var argvs [][]string
			for _, inv := range got {
				argvs = append(argvs, inv.Argv)
			}
			if !reflect.DeepEqual(argvs, tt.want) {
				t.Fatalf("argvs = %#v, want %#v", argvs, tt.want)
			}
		})
	}
}

func TestGhConfigWrapperCanChangeCommandDispatch(t *testing.T) {
	tests := []string{
		"command sudo GH_CONFIG_DIR=/tmp/ghcfg gh pr view 12",
		"env FOO=bar sudo --preserve-env=GH_CONFIG_DIR gh pr view 12",
		"exec sudo -E gh pr view 12",
	}
	for _, command := range tests {
		invocations, err := ParseShellInvocations(command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if len(invocations) != 1 || !invocations[0].DynamicCommand {
			t.Fatalf("%s: argv = %q dynamic=%v dynamicCommand=%v reason=%q, want one dynamic command", command, invocations[0].Argv, invocations[0].Dynamic, invocations[0].DynamicCommand, invocations[0].DynamicReason)
		}
	}
}

func TestNormalizeEnvSudoWrapper(t *testing.T) {
	invocations, err := ParseShellInvocations("env FOO=bar sudo --preserve-env=GH_CONFIG_DIR gh pr view 12")
	if err != nil || len(invocations) != 1 || !reflect.DeepEqual(invocations[0].Argv, []string{"gh", "pr", "view", "12"}) {
		t.Fatalf("invocations = %v, err = %v", invocations, err)
	}
}

func TestParseShellInvocationsMarksExpansionDynamic(t *testing.T) {
	tests := []struct {
		command        string
		dynamicCommand bool
	}{
		{`g{i..i}t push origin main`, true},
		{`g{i,j}it push origin main`, true},
		{`/usr/bin/g[i]t push origin main`, true},
		{`git pu*sh origin main`, false},
	}
	for _, tt := range tests {
		invocations, err := ParseShellInvocations(tt.command)
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if len(invocations) == 0 || !invocations[0].Dynamic {
			t.Fatalf("%s: want dynamic invocation, got %#v", tt.command, invocations)
		}
		if invocations[0].DynamicCommand != tt.dynamicCommand {
			t.Fatalf("%s: dynamicCommand = %v, want %v", tt.command, invocations[0].DynamicCommand, tt.dynamicCommand)
		}
	}
}

func TestRemovedGitSubcommandsFailClosed(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policies := []Policy{policy}
	// Optional/separately-packaged tools and subcommands first shipped after git
	// 2.30 were removed from the known list: they may be absent, so a pre-existing
	// alias could shadow them, and they must fall back to the fail-closed deny.
	for _, sub := range []string{"svn", "scalar", "replay", "repo", "refs", "diagnose", "gui", "gitk", "p4", "send-email", "backfill", "last-modified"} {
		decision, err := EvaluateCommand(policies, "git "+sub+" origin main")
		if err != nil {
			t.Fatalf("git %s: %v", sub, err)
		}
		if decision.Allowed {
			t.Fatalf("git %s: decision = %+v, want deny (unknown subcommand fail-closed)", sub, decision)
		}
	}
	// Guaranteed builtins/core commands (git 2.30) stay recognized and are not
	// treated as an unknown alias dispatch.
	for _, sub := range []string{"maintenance", "status", "bugreport", "commit-tree", "multi-pack-index"} {
		decision, err := EvaluateCommand(policies, "git "+sub+" -h")
		if err != nil {
			t.Fatalf("git %s: %v", sub, err)
		}
		if !decision.Allowed {
			t.Fatalf("git %s: decision = %+v, want allow", sub, decision)
		}
	}
}

func TestKnownGitSubcommandsWithoutFlagTablesAllowReadOptions(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policies := []Policy{policy}
	for _, command := range []string{
		`git log --oneline`,
		`git log --oneline --decorate=short`,
		`git log --color=never --oneline`,
		`git log --stat=80 -n 1`,
		`git log --no-walk=sorted HEAD`,
		`git log -n 1 --pretty`,
		`git show --stat`,
		`git show --stat=80 --no-patch HEAD`,
		`git show --pretty --no-patch HEAD`,
		`git rev-parse --show-toplevel`,
		`git rev-parse --short=7 HEAD`,
		`git rev-parse --abbrev-ref=loose HEAD`,
		`git rev-parse --show-object-format=input`,
		`git rev-parse --glob=refs/heads/main`,
		`git rev-parse --exclude=refs/heads/main --branches`,
		`git rev-parse --parseopt -- --foo`,
	} {
		decision, err := EvaluateCommand(policies, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !decision.Allowed {
			t.Fatalf("%s: decision = %+v, want allow", command, decision)
		}
	}
	decision, err := EvaluateCommand(policies, `git push --force origin main`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed {
		t.Fatalf("git push --force: decision = %+v, want deny", decision)
	}
	decision, err = EvaluateCommand(policies, `git clean -fd`)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatalf("git clean -fd: decision = %+v, want local operation allowed", decision)
	}
}

func TestGitReadOptionTableMatchesGitAcceptedForms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	probe := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Skip("not inside a git worktree")
	}

	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"log", "--decorate", "--oneline", "-n", "1"},
		{"log", "--decorate=short", "--oneline", "-n", "1"},
		{"log", "--color", "--oneline", "-n", "1"},
		{"log", "--color=never", "--oneline", "-n", "1"},
		{"log", "--stat", "-n", "1"},
		{"log", "--stat=80", "-n", "1"},
		{"log", "--pretty", "-n", "1"},
		{"log", "--pretty=oneline", "-n", "1"},
		{"log", "--branches", "-n", "1"},
		{"log", "--branches=main", "-n", "1"},
		{"log", "--tags", "-n", "1"},
		{"log", "--tags=no-such-tag", "-n", "1"},
		{"log", "--remotes", "-n", "1"},
		{"log", "--remotes=origin/main", "-n", "1"},
		{"log", "--no-walk", "HEAD"},
		{"log", "--no-walk=sorted", "HEAD"},
		{"show", "--decorate", "--stat", "--no-patch", "HEAD"},
		{"show", "--decorate=short", "--stat", "--no-patch", "HEAD"},
		{"show", "--color", "--stat", "--no-patch", "HEAD"},
		{"show", "--color=never", "--stat", "--no-patch", "HEAD"},
		{"show", "--stat=80", "--no-patch", "HEAD"},
		{"show", "--pretty", "--no-patch", "HEAD"},
		{"show", "--pretty=oneline", "--no-patch", "HEAD"},
		{"rev-parse", "--short", "HEAD"},
		{"rev-parse", "--short=7", "HEAD"},
		{"rev-parse", "--abbrev-ref", "HEAD"},
		{"rev-parse", "--abbrev-ref=loose", "HEAD"},
		{"rev-parse", "--show-object-format"},
		{"rev-parse", "--show-object-format=input"},
		{"rev-parse", "--default", "HEAD"},
		{"rev-parse", "--branches=main"},
		{"rev-parse", "--tags=no-such-tag"},
		{"rev-parse", "--remotes=origin/main"},
		{"rev-parse", "--glob=refs/heads/main"},
		{"rev-parse", "--exclude=refs/heads/main", "--branches"},
	} {
		run(args...)
		command := "git " + strings.Join(args, " ")
		decision, err := EvaluateCommand([]Policy{policy}, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !decision.Allowed {
			t.Fatalf("%s: decision = %+v, want allow", command, decision)
		}
	}
}

func TestGitReadOptionSyntaxDoesNotBlockRemotePolicy(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := EvaluateCommand([]Policy{policy}, `git rev-parse --default=HEAD`)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatalf("git rev-parse --default=HEAD: decision = %+v, want read operation allowed", decision)
	}
}

func TestReflogSelectorStaysLiteral(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{`git show HEAD@{u}`, []string{"git", "show", "HEAD@{u}"}},
		{`git stash drop stash@{0}`, []string{"git", "stash", "drop", "stash@{0}"}},
		{`git show HEAD@\{u\}`, []string{"git", "show", "HEAD@{u}"}},
	}
	for _, tt := range tests {
		invocations, err := ParseShellInvocations(tt.command)
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if len(invocations) != 1 || invocations[0].Dynamic {
			t.Fatalf("%s: reflog selector should stay literal, got %#v", tt.command, invocations)
		}
		if !reflect.DeepEqual(invocations[0].Argv, tt.want) {
			t.Fatalf("%s: argv = %#v, want %#v", tt.command, invocations[0].Argv, tt.want)
		}
	}
}

func TestQuotedExpansionMetaStaysLiteral(t *testing.T) {
	invocations, err := ParseShellInvocations(`git show 'HEAD@{u}'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 1 || invocations[0].Dynamic {
		t.Fatalf("quoted metacharacters should stay literal, got %#v", invocations)
	}
	if want := []string{"git", "show", "HEAD@{u}"}; !reflect.DeepEqual(invocations[0].Argv, want) {
		t.Fatalf("argv = %#v, want %#v", invocations[0].Argv, want)
	}
}

func TestEvaluateAllowRuleDoesNotShortCircuitCompound(t *testing.T) {
	policies := []Policy{{
		Version: PolicyVersion,
		ID:      "compound",
		Enabled: true,
		Rules: []Rule{
			{ID: "allow-git-status", Effect: EffectAllow, Match: Match{Argv: exactArgs("git", "status")}},
			{ID: "deny-git-push", Effect: EffectDeny, Match: Match{Argv: exactArgs("git", "push")}},
		},
	}}
	for _, command := range []string{
		`git status && git push origin main`,
		`git status; git push origin main`,
	} {
		decision, err := EvaluateCommand(policies, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if decision.Allowed || decision.RuleID != "deny-git-push" {
			t.Fatalf("%s: decision = %+v, want deny-git-push", command, decision)
		}
	}
	allow, err := EvaluateCommand(policies, `git status && git status --short`)
	if err != nil {
		t.Fatal(err)
	}
	if !allow.Allowed {
		t.Fatalf("all-allow compound = %+v, want allow", allow)
	}
}

func TestCommandTextIsData(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`echo git push`,
		`echo 'git push'`,
		`rg "git push"`,
		`custom-tool git push`,
		`git status; foo git push origin main`,
		`gh pr view 12; echo 'git push origin main'`,
		`export NOTE='git push'; echo ok`,
		`declare NOTE='git push'; echo ok`,
		`[[ 'git push' == x ]]; echo ok`,
		`case x in ('git push') echo ok;; esac; echo ok`,
		`for x in 'git push'; do :; done`,
		`for n in 1; do echo 'git push'; done`,
	} {
		decision, err := EvaluateCommand([]Policy{policy}, command)
		if err != nil || !decision.Allowed {
			t.Fatalf("%s: decision = %+v err=%v, want allow", command, decision, err)
		}
	}

	for _, tc := range []struct {
		command string
		ruleID  string
	}{
		{`echo 'gh pr create --title change'`, "deny-gh-pr-create-without-head"},
		{`echo 'gh pr create --head feature --title change'`, "deny-gh-pr-create-without-head"},
		{`echo 'gh pr create --head= --title change'`, "deny-gh-pr-create-without-head"},
		{`echo 'gh pr create --title change' 'gh pr create --head feature'`, "deny-gh-pr-create-without-head"},
		{`echo 'gh pr create --head feature' 'gh pr create --title change'`, "deny-gh-pr-create-without-head"},
		{`for x in 'gh pr create --title change'; do :; done`, "deny-gh-pr-create-without-head"},
		{`python3 -c 'import os; os.system("gh pr create --title --head --body body")'`, "deny-gh-pr-create-without-head"},
		{`echo 'git commit --amend'`, "deny-git-commit-amend"},
		{`echo 'gh pr close 23 --delete-branch'`, "deny-gh-pr-close-delete-branch"},
		{`rg 'gh repo create --push'`, "deny-gh-repo-create"},
		{`echo 'gh repo deploy-key add --allow-write'`, "deny-gh-repo-deploy-key-add-write"},
		{`echo 'gh stack unlink 123 456'`, "deny-gh-stack-except-link-two-ints"},
		{`echo 'gh stack link 123 456' 'gh stack unlink 123 456'`, "deny-gh-stack-except-link-two-ints"},
	} {
		decision, err := EvaluateCommand([]Policy{policy}, tc.command)
		if err != nil || !decision.Allowed {
			t.Fatalf("%s: decision = %+v err=%v, want %s", tc.command, decision, err, tc.ruleID)
		}
	}

	for _, command := range []string{
		`echo git pushy`,
		`echo notgit push`,
		`echo gitpush`,
		`echo 'gh pr close 23 --comment ok'`,
		`echo 'gh stack link 123 456'`,
		`gh pr create --head feature --title ok --body 'git push origin main'`,
		`for n in 1; do gh pr create --head feature --title change --body body; done`,
		`case x in (x) gh pr create --head feature --title change --body body;; esac`,
	} {
		decision, err := EvaluateCommand([]Policy{policy}, command)
		if err != nil || !decision.Allowed {
			t.Fatalf("%s: decision = %+v err=%v, want allow", command, decision, err)
		}
	}
}

func TestGitCommitFlagValuesThroughHookEvent(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		command string
		allowed bool
	}{
		{`git commit -m --amend`, true},
		{`git commit -m--amend`, true},
		{`git commit -qam--amend`, true},
		{`git commit --message --amend`, true},
		{`git commit --message=--amend`, true},
		{`git commit --mess --amend`, true},
		{`git commit -F --amend`, true},
		{`git commit --template --amend`, true},
		{`git commit -S--amend -m message`, true},
		{`git commit -m message -- --amend`, true},
		{`git commit --verbose -- --amend`, true},
		{`git commit --no-message -m message`, true},
		{`git commit --no-amend -m message`, true},
		{`git commit --no-am -m message`, true},
		{`git commit --amend --no-amend -m message`, true},
		{`git commit --amen --no-am -m message`, true},
		{`git commit --amend --no-amend --amend -m message`, true},
		{`git commit --amend -m --no-amend`, true},
		{`git commit --verbose=1 -m message`, true},
		{`git commit --status=true -m message`, true},
		{`git commit --allow-empty=true -m message`, true},
		{`git commit --amend -m message`, true},
		{`git commit -m -- --amend`, true},
		{`git commit --mess -- --amend`, true},
		{`git commit -qm -- --amend`, true},
		{`git commit -m-- --amend`, true},
		{`git commit -m --amend --amend`, true},
		{`git commit --message=--amend --amend`, true},
		{`git commit --no-message --amend`, true},
		{`git commit --no-message --amen`, true},
		{`git commit -S --amend`, true},
		{`git commit -u --amend`, true},
		{`git commit --unknown-option -- --amend`, true},
		{`git commit --m -- --amend`, true},
		{`git commit -m --amend && git push origin main`, false},
		{`git commit -m --amend; git commit --amend -m message`, true},
		{`command git -C . commit -m --amend`, true},
		{`sh -c 'git commit -m -- --amend'`, true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": tt.command}})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := EvaluateHookEvent([]Policy{policy}, input)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
}

func TestEvaluateCommandEmptyCommitArgs(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	wrappers := []string{
		``, `command -- `, `builtin command `, `exec -a '' `,
		`env -u '' `,
	}
	for _, wrapper := range wrappers {
		for _, args := range []string{
			`--allow-empty-message -m ""`,
			`--allow-empty-message --message ''`,
			`--allow-empty-message -qm ''""`,
			`--allow-empty-message --mess ""`,
			`--allow-empty-message -m $''`,
			`--allow-empty-message --message=`,
			`-m message`,
			`-m --amend`,
		} {
			for _, amend := range []bool{false, true} {
				command := wrapper + `git -C '' commit ` + args
				if amend {
					command += ` --amend`
				}
				t.Run(command, func(t *testing.T) {
					decision, err := EvaluateCommand([]Policy{policy}, command)
					if err != nil || !decision.Allowed {
						t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, true)
					}
				})
			}
		}
	}
	for _, tt := range []struct {
		command string
		allowed bool
	}{
		{`git commit --allow-empty-message -m ""`, true},
		{`git commit --allow-empty-message -m "" --amend`, true},
		{`git-commit --allow-empty-message -m "" --amend`, true},
		{`git commit -m '' -- --amend`, true},
		{`git commit -m '' --amend --no-amend`, true},
		{`git commit -m '' --no-amend --amend`, true},
		{`git commit --author '' --amend`, true},
		{`git commit --trailer '' --amend`, true},
		{`git commit -F '' --amend`, true},
		{`sh -c 'git commit -m "" --amend'`, true},
		{`sh -c '' 'git commit --amend'`, true},
		{`env -S 'git commit -m' '' --amend`, true},
		{`env -S 'git commit --allow-empty-message -m' ''`, true},
		{`env -S 'git commit -m' 'message --amend'`, true},
		{`command env -S 'git commit -m' '' --amend`, true},
		{`eval 'git commit -m' '' --amend`, true},
		{`command eval 'git commit -m' '' --amend`, true},
		{`'' git commit --amend`, true},
		{`command '' git commit --amend`, true},
		{`env '' git commit --amend`, true},
		{`exec -a '' '' git commit --amend`, true},
		{`sudo -p '' '' git commit --amend`, false},
	} {
		t.Run(tt.command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, tt.command)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
}

func TestEvaluateHookEventDeniesProtectedCommand(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`)
	decision, err := EvaluateHookEvent([]Policy{policy}, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.RuleID != "deny-git-push" {
		t.Fatalf("decision = %+v, want deny-git-push", decision)
	}
}

func TestNonCommandStatementTextIsData(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"cat <<'EOF'\ngit push origin main\nEOF",
		"cmd='git push origin main'; echo ok",
	} {
		t.Run(command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, command)
			if err != nil {
				t.Fatal(err)
			}
			if !decision.Allowed {
				t.Fatalf("decision = %+v, want allow", decision)
			}
		})
	}
}

func findPresetTestRule(policy Policy, name string) string {
	for _, test := range policy.Tests {
		if test.Name == name {
			return test.RuleID
		}
	}
	return ""
}

func findPresetTestSource(policy Policy, name string) string {
	for _, test := range policy.Tests {
		if test.Name == name {
			return test.Source
		}
	}
	return ""
}

func TestStaticEmptyArgumentRemainsVisibleInDecision(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policy.Rules = []Rule{{ID: "local-amend-opt-in", Effect: EffectDeny, Match: Match{Argv: exactArgs("git", "commit"), HasFlag: []string{"--amend"}}}}
	result, err := EvaluateCommand([]Policy{policy}, `git commit --allow-empty-message -m "" --amend`)
	if err != nil || result.Allowed || len(result.Command) != 6 || result.Command[4] != "" {
		t.Fatalf("decision=%+v err=%v", result, err)
	}
}

func TestEnvSplitPreservesAssignmentsAndLiteralMessages(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{`env -S '' FOO=bar git push origin main`, false},
		{`command env --split-string='' FOO=bar git commit -m '' --amend`, true},
		{`env -iS '' FOO=bar git push origin main`, false},
		{`env -S '' GIT_CONFIG_GLOBAL=fixture git status`, false},
		{`env -S 'git commit -m' 'subject
body'`, true},
		{"env -S 'git commit -m' 'subject\tbody'", true},
		{`env -S 'git commit -m' 'subject --amend'`, true},
		{`env -S '' FOO=bar git commit -m ''`, true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || got.Allowed != tc.allowed {
				t.Fatalf("decision = %+v, err=%v, want allowed=%v", got, err, tc.allowed)
			}
		})
	}
}

func TestRecursiveShellParsingPreservesStartupEnvironment(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{`BASH_ENV=fixture command env -S 'bash -c true'`, false},
		{`BASH_ENV=fixture env --split-string='bash -c true'`, false},
		{`env BASH_ENV=fixture -S 'bash -c true'`, true},
		{`env -S 'BASH_ENV=fixture bash -c true'`, false},
		{`BASH_ENV=fixture command eval 'bash -c true'`, false},
		{`ENV=fixture env -S 'sh -c true'`, false},
		{`ZDOTDIR=fixture command env -S 'zsh -c true'`, false},
		{`export BASH_ENV=fixture; bash -c true`, false},
		{`export $v=fixture; bash -c true`, false},
		{`declare $v=fixture; bash -c true`, false},
		{`declare -x BASH_ENV=fixture; bash -c true`, false},
		{`declare -n ref=BASH_ENV; bash -c true`, false},
		{`declare -n $v=BASH_ENV; bash -c true`, false},
		{`typeset -x ENV=fixture; sh -c true`, false},
		{`command export BASH_ENV=fixture; bash -c true`, false},
		{`builtin export BASH_ENV=fixture; bash -c true`, false},
		{`command declare -x BASH_ENV=fixture; bash -c true`, false},
		{`command export BASH_ENV+=fixture; bash -c true`, false},
		{`\export BASH_ENV=fixture; bash -c true`, false},
		{`"export" BASH_ENV=fixture; bash -c true`, false},
		{`ex\port BASH_ENV=fixture; bash -c true`, false},
		{`'export' BASH_ENV=fixture; bash -c true`, false},
		{`\declare -x BASH_ENV=fixture; bash -c true`, false},
		{`\typeset -x ENV=fixture; sh -c true`, false},
		{`set -a; BASH_ENV=fixture; bash -c true`, false},
		{`set -o allexport; BASH_ENV=fixture; bash -c true`, false},
		{`set -eo allexport; BASH_ENV=fixture; bash -c true`, false},
		{`set -o errexit -o allexport; BASH_ENV=fixture; bash -c true`, false},
		{`set -o pipefail -a; BASH_ENV=fixture; bash -c true`, false},
		{`set -euo pipefail -a; BASH_ENV=fixture; bash -c true`, false},
		{`mode=allexport; set -o "$mode"; BASH_ENV=fixture; bash -c true`, false},
		{`mode=-a; set +o "$mode"; BASH_ENV=fixture; bash -c true`, false},
		{`flags=-a; set "$flags"; BASH_ENV=fixture; bash -c true`, false},
		{`BASH_ENV=fixture env -S 'env -S "bash -c true"'`, false},
		{`BASH_ENV=fixture env -S 'echo safe'`, true},
		{`export BASH_ENV=fixture; export -n BASH_ENV; bash -c true`, false},
		{`export BASH_ENV=fixture; unset BASH_ENV; bash -c true`, false},
		{`export BASH_ENV=fixture; unset -f BASH_ENV; bash -c true`, false},
		{`export -n BASH_ENV; bash -c true`, true},
		{`command export -n BASH_ENV; bash -c true`, true},
		{`env export BASH_ENV=fixture; bash -c true`, true},
		{`env \export BASH_ENV=fixture; bash -c true`, true},
		{`declare -i count=1; bash -c true`, true},
		{`set +a; FOO=fixture; bash -c true`, true},
		{`set -- "$flags"; BASH_ENV=fixture; bash -c true`, true},
		{`env -S 'bash -c true'`, true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || got.Allowed != tc.allowed {
				t.Fatalf("decision=%+v error=%v, want allowed=%v", got, err, tc.allowed)
			}
		})
	}
}

func TestWrapperFamilyOptionTables(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		command string
		allowed bool
	}
	families := map[string][]row{
		"command": {
			{`command git status`, true},
			{`command -p git status`, true},
			{`command -- git status`, true},
			{`command -- git push`, false},
			{`command -pp git push`, false},
			{`command -pp git status`, false},
			{`command -x git status`, false},
		},
		"builtin": {
			{`builtin git status`, true},
			{`builtin git push`, false},
			{`builtin -- echo ok`, true},
			{`builtin -- git push`, false},
			{`builtin -x git status`, false},
		},
		"exec": {
			{`exec git status`, true},
			{`exec -l git status`, true},
			{`exec -cl git status`, true},
			{`exec -a quota git status`, true},
			{`exec -aquota git status`, true},
			{`exec -- git status`, true},
			{`exec -- git push`, false},
			{`exec -a quota git push`, false},
			{`exec -aquota git push`, false},
			{`exec -caquota git push`, false},
			{`exec -cc -l bash -c true`, false},
			{`exec -a -bash bash -c true`, false},
			{`exec -z git status`, false},
			{`exec --clean git status`, false},
			{`exec -a`, false},
		},
		"env": {
			{`env git status`, true},
			{`env -i git status`, true},
			{`env -iu EXAMPLE git status`, true},
			{`env EXAMPLE=value -- git status`, true},
			{`env -- git status`, true},
			{`env -- git push`, false},
			{`env -q git status`, false},
			{`env --unknown git status`, false},
			{`git --unknown status`, false},
			{`gh --unknown pr view 123`, false},
			{`bash -c "$SCRIPT"`, false},
			{`env -u`, false},
			{`env -S`, false},
		},
		"sudo": {
			{`sudo git status`, false},
			{`sudo -n git status`, false},
			{`sudo -h`, false},
			{`sudo -u nobody git status`, false},
			{`sudo -nu nobody git status`, false},
			{`sudo -- git status`, false},
			{`sudo -- git push`, false},
			{`sudo -h host git push`, false},
			{`sudo -Z git status`, false},
			{`sudo --unknown git status`, false},
			{`sudo -u`, false},
			{`sudo -E git status`, false},
			{`sudo -i bash -c true`, false},
			{`sudo -s "git push"`, false},
			{`sudo -i "git push"`, false},
			{`sudo --shell "git push"`, false},
			{`sudo --login "git push"`, false},
		},
		"nohup": {
			{`nohup git status`, true},
			{`nohup -- git status`, true},
			{`nohup -- git push`, false},
			{`nohup --unknown git status`, false},
		},
		"nice": {
			{`nice -n 5 git status`, true},
			{`nice -n 5 -- git status`, true},
			{`nice -n 5 -- git push`, false},
			{`nice --unknown git status`, false},
			{`nice -n`, false},
		},
		"timeout": {
			{`timeout 5 git status`, true},
			{`timeout -- 5 git status`, true},
			{`timeout -- 5 git push`, false},
			{`timeout --unknown 5 git status`, false},
		},
		"eval": {
			{`eval 'git status'`, true},
			{`eval 'git push'`, false},
			{`eval -- 'git push'`, false},
			{`eval -- 'git status'`, true},
			{`eval --unknown 'git status'`, false},
			{`eval 'exec -z git status'`, false},
		},
		"shell": {
			{`bash +c -e "git push"`, false},
			{`bash -c -e "git push"`, false},
			{`bash -h script.sh`, false},
			{`bash -n script.sh`, false},
			{`dash -n script.sh`, false},
			{`bash -n -c 'git push'`, false},
			{`bash -o noexec -c 'git push'`, false},
			{`bash -D -c 'git push'`, false},
			{`bash +D -c 'git push'`, false},
			{`bash -D +n -c 'git push'`, false},
			{`ksh -D -c 'git push'`, false},
			{`bash --dump-strings -c 'git push'`, false},
			{`bash --dump-po-strings -c 'git push'`, false},
			{`bash -n +n -c 'git push'`, false},
			{`bash -n +o noexec script.sh`, false},
			{`bash -c 'git push' -n`, false},
			{`bash -c "if"`, false},
			{`env -S "'unterminated"`, false},
			{`bash -c "git status"`, true},
			{`sh -c 'git status'`, true},
			{`zsh -D -c 'git status'`, true},
			{`dash -I -c 'git status'`, true},
			{`bash -e -c 'git status'`, true},
			{`bash -eo pipefail -c 'git status'`, true},
			{`bash --norc -c 'git status'`, true},
			{`sh -c 'git push'`, false},
			{`zsh -D -c 'git push'`, false},
			{`dash -I -c 'git push'`, false},
			{`bash -- -c 'git status'`, false},
			{`bash -z -c 'git status'`, false},
			{`bash --unknown -c 'git status'`, false},
			{`bash -c`, false},
			{`bash -l -c true`, false},
			{`bash -i -c true`, false},
			{`bash --rcfile x -c true`, false},
			{`bash script.sh`, false},
		},
	}
	for family, rows := range families {
		for _, tt := range rows {
			t.Run(family+"/"+tt.command, func(t *testing.T) {
				decision, err := EvaluateCommand([]Policy{policy}, tt.command)
				if err != nil || decision.Allowed != tt.allowed {
					t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
				}
			})
		}
	}
}

func TestEvaluateUndecidableWrappersWithAllowOnlyPolicy(t *testing.T) {
	policies := []Policy{{
		Version: PolicyVersion,
		ID:      "allow-only",
		Enabled: true,
		Rules: []Rule{{
			ID:     "allow-git-status",
			Effect: EffectAllow,
			Match:  Match{Argv: exactArgs("git", "status")},
		}},
	}}
	for _, tt := range []struct {
		command string
		allowed bool
	}{
		{`env --unknown git status`, false},
		{`git --unknown status`, false},
		{`git status --future-option`, true},
		{`gh --unknown pr view 123`, false},
		{`bash -c "$SCRIPT"`, false},
		{`sudo --unknown git status`, false},
		{`bash --unknown -c "git status"`, false},
		{`bash -c "if"`, false},
		{`env -S "'unterminated"`, false},
		{`bash -c "git status"`, true},
	} {
		t.Run(tt.command, func(t *testing.T) {
			decision, err := EvaluateCommand(policies, tt.command)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
}

func TestCommandInterpretationDoesNotDependOnPolicy(t *testing.T) {
	allow := Policy{Version: PolicyVersion, ID: "allow", Enabled: true,
		Rules: []Rule{{ID: "allow-all", Effect: EffectAllow, Match: Match{Argv: []ArgPattern{{Type: "nonempty"}}}}}}
	disabled := allow
	disabled.Enabled = false
	preset, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, policies := range [][]Policy{nil, {allow}, {disabled}, {allow, preset}, {preset}} {
		for _, key := range []string{"command", "cmd"} {
			for _, command := range []string{
				"gh repo edit --description", "git unknown-helper",
				`git "$subcommand"`,
			} {
				input, err := json.Marshal(map[string]any{"tool_input": map[string]string{key: command}})
				if err != nil {
					t.Fatal(err)
				}
				decision, err := EvaluateHookEvent(policies, input)
				if err != nil || decision.Allowed || decision.RuleID != "" || decision.Reason == "" {
					t.Fatalf("%s policies=%v: %+v err=%v", command, policies, decision, err)
				}
			}
			for _, command := range []string{`git status`, `git tag -mfeature example`, `gh pr view 7`, `git status --future-option`, `git commit --unknown`, `gh pr view --unknown`, `gh pr view "$number"`} {
				input, err := json.Marshal(map[string]any{"tool_input": map[string]string{key: command}})
				if err != nil {
					t.Fatal(err)
				}
				decision, err := EvaluateHookEvent(policies, input)
				if err != nil || !decision.Allowed {
					t.Fatalf("%s: %+v err=%v", command, decision, err)
				}
			}
		}
	}
}

func TestShellOptionSigns(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []string{"+i", "-i +i"} {
		for _, command := range []string{"git status", "git push"} {
			decision, err := EvaluateCommand([]Policy{policy}, "bash -c "+options+" '"+command+"'")
			if err != nil || decision.Allowed != (command == "git status") {
				t.Fatalf("%s %s: %+v err=%v", options, command, decision, err)
			}
		}
	}
	for _, options := range []string{"-i", "-l", "+l", "-l +l", "+i -i", "+l -l"} {
		decision, err := EvaluateCommand([]Policy{policy}, "bash -c "+options+" 'git status'")
		if err != nil || decision.Allowed {
			t.Fatalf("%s: %+v err=%v", options, decision, err)
		}
	}
}
