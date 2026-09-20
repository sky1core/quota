package agenthooks

import (
	"encoding/json"
	"testing"
)

func TestRemotePolicyBoundaryThroughHookEvents(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allow   bool
	}{
		{`git commit --amend -m change`, true},
		{`git commit --amend -m "$message"`, true},
		{`command git commit --amend -m "$message"`, true},
		{`env LANG=C git add *.go`, true},
		{`nice -n 5 git log "$BRANCH"`, true},
		{`command "$cmd"`, false},
		{`nice -n "$priority" git status`, false},
		{`git add *.go`, true},
		{`git log "$BRANCH"`, true},
		{`git config --get alias.publish`, true},
		{`git config alias.publish 'push origin main'`, true},
		{`git merge --abort`, true},
		{`git rebase --abort`, true},
		{`git rebase -i --autosquash HEAD~3`, true},
		{`git replace --list`, true},
		{`git branch -m old new`, true},
		{`git branch -c main copy`, true},
		{`git tag -d v1`, true},
		{`git push --dry-run origin main`, true},
		{`git push --dry-run --receive-pack=/tmp/custom-helper origin main`, false},
		{`git push --dry-run --exec=/tmp/custom-helper origin main`, false},
		{`git send-pack --dry-run --receive-pack=/tmp/custom-helper origin refs/heads/main`, false},
		{`git push --help`, true},
		{`git push origin main`, false},
		{`git push --force-with-lease origin main`, false},
		{`git push --delete origin feature`, false},
		{`git send-pack origin refs/heads/main`, false},
		{`git http-push origin refs/heads/main`, false},
		{`git commit --amend -m change && git push origin main`, false},
		{`echo 'git push origin main'`, true},
		{`rg 'git push' README.md`, true},
		{`echo "$(git push origin main)"`, false},
		{`eval 'git push origin main'`, false},
		{`sh -c 'git push origin main'`, false},
		{`sh -c 'git push origin main' -- "$arg"`, false},
		{`sh -c 'git log "$1"' -- "$branch"`, true},
		{`sh -c "$script"`, false},
		{`git for-each-repo --config=maintenance.repo status`, true},
		{`git for-each-repo --config=maintenance.repo push origin main`, false},
		{`git bisect run true`, true},
		{`git bisect run git push origin main`, false},
		{`git submodule foreach 'git status'`, true},
		{`git submodule foreach --recursive 'git status'`, true},
		{`git submodule foreach --quiet 'git status'`, true},
		{`git submodule foreach git status`, true},
		{`git submodule foreach --help`, true},
		{`git submodule foreach 'git push origin main'`, false},
		{`git submodule foreach git push origin main`, false},
		{`git submodule foreach --recursive git push origin main`, false},
		{`git submodule foreach 'git push origin main; :' --help`, false},
		{`git rebase --exec 'git status' main`, true},
		{`git rebase --exec 'git push origin main' main`, false},
		{`git rebase --exec="$SCRIPT" main`, false},
		{`git rebase -x"$SCRIPT" main`, false},
		{`git rebase --exec "$SCRIPT" main`, false},
		{`git rebase --onto="$BASE" main`, true},
		{`git rebase --onto "$BASE" -- feature`, true},
		{`git rebase -- "$BRANCH"`, true},
		{`OP='push origin '; git ${OP}log`, false},
		{`git filter-branch --tree-filter="$SCRIPT" HEAD`, false},
		{`git bisect "$ACTION" git push origin main`, false},
		{`git bisect run sh -c "$SCRIPT"`, false},
		{`git bisect run git log "$BRANCH"`, true},
		{`git for-each-repo --config=maintenance.repo rebase --exec="$SCRIPT" main`, false},
		{`git submodule`, true},
		{`git submodule --cached`, true},
		{`git submodule status --cached`, true},
		{`git submodule update --init`, true},
		{`git submodule status -- "$path"`, true},
		{`git submodule status --local-data-option "$path"`, true},
		{`git submodule foreach --unsupported 'git push origin main'`, false},
		{`git -C . rebase --exec="$SCRIPT" main`, false},
		{`command git -C . rebase --exec "$SCRIPT" main`, false},
		{`git-rebase --exec="$SCRIPT" main`, false},
		{`git for-each-repo --config=maintenance.repo commit --amend -m "$message"`, true},
		{`git submodule "$ACTION" 'git push origin main'`, false},
		{`git submodule foreach "$SCRIPT"`, false},
		{`printf hello | xargs echo`, true},
		{`printf main | xargs git push origin`, false},
		{`printf push | xargs git`, false},
		{`printf git | xargs -I{} {} push origin main`, false},
		{`printf push | xargs -I{} git {} origin main`, false},
		{`printf push | xargs gh`, false},
		{`printf '%s\n' 'git push origin main' | xargs -I{} sh -c '{}'`, false},
		{`printf '%s\n' 'rm file.txt' | xargs -I{} sh -c '{}'`, false},
		{`printf '%s\n' 'git push origin main' | xargs --replace={} bash -c '{}'`, false},
		{`printf '%s\n' 'git push origin main' | xargs -I{} git rebase --exec='{}' main`, false},
		{`printf '%s\n' 'git push origin main' | xargs -I{} git bisect run sh -c '{}'`, false},
		{`printf main | xargs -I{} git log '{}'`, true},
		{`printf main | xargs -I{} sh -c 'git log "$1"' -- '{}'`, true},
		{`trap -p`, true},
		{`trap 'git push origin main' EXIT`, false},
		{`trap "$script" EXIT`, false},
		{`SCRIPT='; git push origin main'; trap echo"$SCRIPT" EXIT`, false},
		{`source ./publish.sh`, false},
		{`gh pr view "$NUM"`, true},
		{`ACTION='merge '; gh pr ${ACTION}view`, false},
		{`gh pr checkout 12 --force`, true},
		{`gh pr merge 12 --disable-auto`, true},
		{`gh pr merge --disable-auto -- "$NUM"`, true},
		{`gh pr merge 12 --squash --disable-auto"$SUFFIX"`, false},
		{`gh pr merge 12 --squash --disable-auto=${SUFFIX}true`, false},
		{`gh pr merge 12 --squash --disable-auto --auto=${SUFFIX}false --auto=false`, false},
		{`gh pr merge 12 --auto`, false},
		{`gh pr close 12`, true},
		{`gh pr close -- "$NUM"`, true},
		{`gh pr close 12 "$FLAG"`, false},
		{`gh pr close 12 --delete-branch`, false},
		{`gh pr close 12 --"$FLAG"`, false},
		{`gh pr close 12 -${FLAGS}ccomment`, false},
		{`gh issue develop 12 --list`, true},
		{`gh issue develop --list -- "$NUM"`, true},
		{`gh issue develop 12 --name feature`, false},
		{`gh pr create --head=feature-"$SUFFIX" --title change --body body`, true},
		{`gh pr create --head="$SUFFIX" --title change --body body`, false},
		{`gh stack link 12 34`, true},
		{`gh stack link 12"$SUFFIX" 34`, false},
		{`gh stack submit`, false},
		{`gh repo create owner/new-repo --private`, false},
		{`gh repo edit --visibility public`, false},
		{`gh repo deploy-key add key.pub --allow-write`, false},
		{`gh secret set TOKEN --body value --repo owner/repo`, false},
		{`gh secret set TOKEN --body value --repo owner/repo --no-store`, true},
		{`gh secret set TOKEN --body value --repo owner/repo --no-store=false`, false},
		{`gh secret set TOKEN --repo owner/repo --no-store=${SUFFIX}true`, false},
		{`gh variable set MODE --body dev --repo owner/repo`, false},
		{`gh workflow disable ci.yml`, false},
		{`gh repo view owner/repo`, true},
		{`gh repo deploy-key list --repo owner/repo`, true},
		{`gh release create v1 --verify-tag --notes change`, true},
		{`gh release create --verify-tag -- "$TAG"`, true},
		{`gh release create v1 --notes change`, false},
		{`gh release delete v1 --yes`, true},
		{`gh release delete v1 --yes "$FLAG"`, false},
		{`gh release delete v1 --cleanup-tag --yes`, false},
		{`gh api repos/example/project/pulls`, true},
		{`gh api repos/example/project/issues -X POST --input "$bodyFile"`, true},
		{`gh api repos/example/project/issues -f "$field"`, true},
		{`gh api repos/example/project -X PATCH -f visibility=private`, false},
		{`gh api repos/example/project/keys -X POST -f key=abc`, false},
		{`gh api repos/example/project/contents/file -X PUT --input "$bodyFile"`, false},
		{`gh api graphql -f "$query"`, false},
		{`gh api graphql -f 'query=query($login:String!){user(login:$login){login}}' -f login="$LOGIN"`, true},
		{`gh api graphql -f 'query=mutation { updateRepository(input: {repositoryId: "X", hasWikiEnabled: false}) { clientMutationId } }'`, false},
		{`gh api graphql -f 'query=mutation { archiveRepository(input: {repositoryId: "X"}) { clientMutationId } }'`, false},
		{`gh api repos/example/project/git/refs -X POST -f ref=refs/heads/main -f sha=abcdef`, false},
		{`gh pr comment 12 -bprefix"$BODY"`, true},
		{`env -S 'git log' "$BRANCH"`, true},
		{`env LANG="$LANG" git status`, true},
		{`env "$KEY"=/tmp/startup bash -c true`, false},
		{`env BASH"$SUFFIX"=/tmp/startup bash -c true`, false},
		{`env -S 'sh -c' "$SCRIPT"`, false},
		{`exec -c"$FLAGS" bash -c true`, false},
		{`sudo git commit --amend -m change`, false},
		{`git commit --amend -m change; rm file.txt`, false},
		{`kill "$PID"`, false},
		{`kill 12345`, true},
	} {
		for _, key := range []string{"command", "cmd"} {
			t.Run(key+"/"+tc.command, func(t *testing.T) {
				input, err := json.Marshal(map[string]any{"tool_input": map[string]string{key: tc.command}})
				if err != nil {
					t.Fatal(err)
				}
				got, err := EvaluateHookEvent([]Policy{policy}, input)
				if err != nil || got.Allowed != tc.allow {
					t.Fatalf("got %+v, err=%v; want allowed=%v", got, err, tc.allow)
				}
			})
		}
	}
}

func TestLocalAmendRestrictionRequiresSeparateRule(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	extra := Policy{Version: PolicyVersion, ID: "local-amend", Enabled: true, Rules: []Rule{{
		ID: "deny-local-amend", Effect: EffectDeny,
		Match:   Match{Argv: exactArgs("git", "commit"), HasFlag: []string{"--amend"}},
		Message: "separately configured local history protection",
	}}}
	for _, enabled := range []bool{false, true} {
		extra.Enabled = enabled
		got, err := EvaluateCommand([]Policy{policy, extra}, `git commit --amend -m change`)
		if err != nil || got.Allowed == enabled || enabled && got.RuleID != "deny-local-amend" {
			t.Fatalf("enabled=%v: %+v, %v", enabled, got, err)
		}
		got, err = EvaluateCommand([]Policy{policy, extra}, `git commit "$options"`)
		if err != nil || got.Allowed == enabled {
			t.Fatalf("dynamic options, enabled=%v: %+v, %v", enabled, got, err)
		}
	}
}
