package agenthooks

import (
	"encoding/json"
	"testing"
)

func TestParseShellInvocationsPreservesEmptyArgs(t *testing.T) {
	tests := []struct {
		command string
		want    [][]string
	}{
		{``, nil},
		{`""`, [][]string{{""}}},
		{`'' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`echo "" '' $'' a""b ''""`, [][]string{{"echo", "", "", "", "ab", ""}}},
		{"echo \"\\\n\"", [][]string{{"echo", ""}}},
		{"git \\\n status", [][]string{{"git", "status"}}},
		{`git "" commit --amend`, [][]string{{"git", "", "commit", "--amend"}}},
		{`git -C "" commit -m '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`gh --repo '' pr create --body ""`, [][]string{{"gh", "pr", "create", "--body", ""}}},
		{`command -- '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`builtin '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`exec -a '' git commit -m "" --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`exec -a '' '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`env '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`env -u '' git commit -m "" --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`sudo -p '' git commit -m "" --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`sudo -p '' '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`sh -c '' 'git commit --amend'`, nil},
		{`command exec -a '' sh -c '' 'git commit --amend'`, nil},
		{`sh -c 'git commit -m "" --amend'`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`eval '' 'git commit -m "" --amend'`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`command eval 'git commit -m' '' --amend`, [][]string{{"git", "commit", "-m", "--amend"}}},
		{`env -S 'git commit -m' '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`env --split-string='git commit -m' '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`env -iS 'git commit -m' '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`env -iSgit\ commit\ -m '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`command env -S 'git commit -m' '' --amend`, [][]string{{"git", "commit", "-m", "", "--amend"}}},
		{`env -S '' '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`env -S '' git status`, [][]string{{"git", "status"}}},
		{`env --split-string= '' git commit --amend`, [][]string{{"", "git", "commit", "--amend"}}},
		{`env -S 'git commit -m' 'message --amend'`, [][]string{{"git", "commit", "-m", "message --amend"}}},
		{`env -S 'echo' 'two words' ';' '$value'`, [][]string{{"echo", "two words", ";", "$value"}}},
		{`env -S 'echo' "it's quoted"`, [][]string{{"echo", "it's quoted"}}},
		{`command exec -a '' '' env GIT_CONFIG_GLOBAL=fixture git status`, [][]string{{"", "env", "GIT_CONFIG_GLOBAL=fixture", "git", "status"}}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got, err := ParseShellInvocations(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			var want []Invocation
			for _, argv := range tt.want {
				want = append(want, Invocation{Argv: argv})
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("invocations = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseShellInvocationsEmptyArgsDoNotHideDynamicDispatch(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`$command git commit -m '' --amend`,
		`"$command" git commit -m '' --amend`,
		`git commit -m $message --amend`,
		`git commit -m "$message" --amend`,
		`git commit -m ${message:-} --amend`,
		`git commit -m "${message:-}" --amend`,
		`sh -c "$script"`,
		`sh -c $script`,
		`command sh -c "$script"`,
		`exec -a '' sh -c "$script"`,
		`command exec -a '' env GIT_CONFIG_GLOBAL=fixture git status`,
		`command exec -a '' env GH_CONFIG_DIR=fixture gh pr view 7`,
		`command exec -a '' env BASH_ENV=fixture bash -c true`,
		`command exec -a '' -l bash -c true`,
	} {
		t.Run(command, func(t *testing.T) {
			got, err := ParseShellInvocations(command)
			if err != nil || len(got) != 1 || !got[0].Dynamic {
				t.Fatalf("invocations = %#v, err = %v, want one dynamic invocation", got, err)
			}
			decision, err := EvaluateCommand([]Policy{policy}, command)
			if err != nil || decision.Allowed {
				t.Fatalf("decision = %+v, err = %v, want deny", decision, err)
			}
		})
	}
}
