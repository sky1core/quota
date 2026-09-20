package agenthooks

import (
	"fmt"
	"testing"
)

func TestGhAPIContract(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		remote, unknown bool
	}{
		{[]string{"repos/example/repo"}, false, false},
		{[]string{"repos/example/repo/contents/file", "-XGET"}, false, false},
		{[]string{"repos/example/repo/git/refs", "-X", "POST", "-f", "ref=refs/heads/topic"}, true, false},
		{[]string{"repos/example/repo/git/refs/heads/topic", "-XDELETE"}, true, false},
		{[]string{"repos/example/repo/contents/file", "-XPUT", "-f", "content=abc"}, true, false},
		{[]string{"repos/example/repo/pulls/1/merge", "-XPUT"}, true, false},
		{[]string{"repos/example/repo/pulls/1/update-branch", "-XPUT"}, true, false},
		{[]string{"repos/example/repo", "-XDELETE"}, true, false},
		{[]string{"repos/example/repo/forks", "-XPOST"}, true, false},
		{[]string{"repos/example/repo/issues/1/comments", "-fbody=git push"}, false, false},
		{[]string{"repos/example/repo/issues", "-f", "title=git push"}, false, false},
		{[]string{"repos/example/repo/pulls", "-fhead=topic", "-fbase=main"}, false, false},
		{[]string{"repos/example/repo/pulls/1/reviews", "-fbody=git push", "-fevent=COMMENT"}, false, false},
		{[]string{"repos/example/repo/issues/1/labels", "-XPOST", "-Flabels[]=bug", "-Flabels[]=enhancement"}, false, false},
		{[]string{"repos/example/repo/issues/1/labels", "-XPUT", "-Flabels[]"}, false, false},
		{[]string{"repos/example/repo/releases", "-ftag_name=v1"}, true, false},
		{[]string{"repos/example/repo/releases/1", "-XDELETE"}, false, false},
		{[]string{"repos/example/repo/releases/1", "-XPATCH", "-fbody=git push"}, false, false},
		{[]string{"repos/example/repo/releases/1", "-XPATCH", "-ftag_name=v2"}, true, false},
		{[]string{"repos/example/repo/releases/1", "-XPATCH", "--input", "payload.json"}, false, true},
		{[]string{"repos/example/repo/releases/assets/1", "-XDELETE"}, false, false},
		{[]string{"https://uploads.github.com/repos/example/repo/releases/1/assets?name=file", "-XPOST", "--input", "asset.bin"}, false, false},
		{[]string{"repos/example/repo/dispatches", "-fevent_type=publish"}, false, true},
		{[]string{"repos/example/repo/actions/workflows/ci.yml/dispatches", "-fref=main"}, false, true},
		{[]string{"repos/example/repo/future-api", "-XPOST"}, false, true},
		{[]string{"repos/example/repo", "--method", ""}, false, true},
		{[]string{"repos/example/repo", "-f", ""}, false, true},
		{[]string{""}, false, true},
		{[]string{"repos/example/repo/../other", "-XPOST"}, false, true},
		{[]string{"repos/example/repo", "-H", "X-HTTP-Method-Override:DELETE"}, false, true},
		{[]string{"graphql", "-fquery={ viewer { login } }"}, false, false},
		{[]string{"graphql", "-fquery=query { search(query: \"mutation { deleteRef } git push\", type: ISSUE, first: 1) { issueCount } }"}, false, false},
		{[]string{"graphql", "-fquery=mutation { addComment(input: {subjectId: \"X\", body: \"git push\"}) { clientMutationId } }"}, false, false},
		{[]string{"graphql", "-fquery=mutation($input: AddCommentInput!) { addComment(input: $input) { clientMutationId } }", "-Finput=@payload.json"}, false, false},
		{[]string{"graphql", "-fquery=mutation { updateRepository(input: {repositoryId: \"X\", hasIssuesEnabled: false}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { archiveRepository(input: {repositoryId: \"X\"}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { unarchiveRepository(input: {repositoryId: \"X\"}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { safe: deleteRef(input: {refId: \"X\"}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { enablePullRequestAutoMerge(input: {pullRequestId: \"X\"}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { disablePullRequestAutoMerge(input: {pullRequestId: \"X\"}) { clientMutationId } }"}, false, false},
		{[]string{"graphql", "-fquery=mutation { ...Write } fragment Write on Mutation { createRef(input: {name: \"refs/heads/test\"}) { clientMutationId } }"}, true, false},
		{[]string{"graphql", "-fquery=mutation { ... on Mutation { mergePullRequest(input: {pullRequestId: \"X\"}) { clientMutationId } } }"}, true, false},
		{[]string{"graphql", "-fquery=query Read { viewer { login } } mutation Write { deleteRef(input: {refId: \"X\"}) { clientMutationId } }", "-foperationName=Read"}, false, false},
		{[]string{"graphql", "-fquery=query Read { viewer { login } } mutation Write { deleteRef(input: {refId: \"X\"}) { clientMutationId } }"}, false, true},
		{[]string{"graphql", "-fquery=mutation { unknownMutation { clientMutationId } }"}, false, true},
		{[]string{"graphql", "-fquery=mutation { ...Loop } fragment Loop on Mutation { ...Loop }"}, false, true},
		{[]string{"graphql", "-fquery=mutation { ...Missing }"}, false, true},
		{[]string{"graphql", "-fquery=mutation { broken"}, false, true},
		{[]string{"graphql", "-fquery="}, false, true},
		{[]string{"graphql", "-Fquery=@query.graphql"}, false, true},
		{[]string{"graphql", "--input", "request.json"}, false, true},
		{[]string{"graphql?query=mutation"}, false, true},
	} {
		t.Run(fmt.Sprint(tc.args), func(t *testing.T) {
			parsed, err := parseGhCommand(append([]string{"gh", "api"}, tc.args...))
			if (err != nil || parsed.undecidable != "") != tc.unknown || (parsed.risk == "remote-code-ref-mutation") != tc.remote {
				t.Fatalf("parsed=%+v err=%v; remote=%v unknown=%v", parsed, err, tc.remote, tc.unknown)
			}
		})
	}
}
