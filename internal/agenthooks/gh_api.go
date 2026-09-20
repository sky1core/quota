package agenthooks

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

type ghRESTOperation struct {
	methods  string
	endpoint *regexp.Regexp
	effect   string
}

var ghRESTOperations = []ghRESTOperation{
	{"POST PATCH DELETE PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/(git/(refs|commits|trees|blobs|tags)(/.*)?|contents(/.*)?|merges|merge-upstream|forks)$`), "remote"},
	{"PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/pulls/[^/]+/(merge|update-branch)$`), "remote"},
	{"POST", regexp.MustCompile(`^repos/[^/]+/[^/]+/(generate|branches/[^/]+/rename)$`), "remote"},
	{"DELETE", regexp.MustCompile(`^repos/[^/]+/[^/]+$`), "remote"},
	{"POST PATCH DELETE PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/(topics|collaborators|teams|autolinks|keys|rulesets)(/.*)?$`), "remote"},
	{"POST PATCH DELETE", regexp.MustCompile(`^gists(/[^/]+)?$`), "remote"},
	{"POST", regexp.MustCompile(`^repos/[^/]+/[^/]+/(dispatches|actions/workflows/[^/]+/dispatches|actions/(runs|jobs)/[^/]+/rerun(-failed-jobs)?)$`), "indirect"},
	{"POST", regexp.MustCompile(`^repos/[^/]+/[^/]+/releases$`), "remote"},
	{"PATCH", regexp.MustCompile(`^repos/[^/]+/[^/]+/releases/[^/]+$`), "release-edit"},
	{"POST PATCH DELETE PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/issues(/[^/]+)?(/(comments|labels|assignees|lock|reactions)(/[^/]+)?)?$`), "metadata"},
	{"POST PATCH DELETE PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/pulls(/[^/]+)?(/(comments|reviews|requested_reviewers)(/[^/]+)?(/(comments|events|dismissals|reactions))?)?$`), "metadata"},
	{"POST PATCH DELETE PUT", regexp.MustCompile(`^repos/[^/]+/[^/]+/(labels|milestones|comments)(/[^/]+)?(/reactions(/[^/]+)?)?$`), "metadata"},
	{"DELETE", regexp.MustCompile(`^repos/[^/]+/[^/]+/releases/[^/]+$`), "metadata"},
	{"POST PATCH DELETE", regexp.MustCompile(`^repos/[^/]+/[^/]+/releases/(generate-notes|[^/]+/assets|assets/[^/]+)$`), "metadata"},
	{"PATCH", regexp.MustCompile(`^repos/[^/]+/[^/]+$`), "remote"},
}

func classifyGhAPI(parsed *parsedCommand, positionals commandInput) {
	unknown := func(reason string) { parsed.undecidable = "gh api: " + reason }
	if len(positionals.argv) != 1 || positionals.argv[0] == "" || positionals.dynamicAt(0) {
		unknown("endpoint is not statically available")
		return
	}
	endpoint, err := ghAPIEndpoint(positionals.argv[0])
	if err != nil {
		unknown(err.Error())
		return
	}
	fields := make(map[string]string)
	fileFields := make(map[string]bool)
	dynamicFields := make(map[string]bool)
	hasInput := false
	hasFields := false
	unknownFields := false
	for _, flag := range parsed.flags {
		switch flag.name {
		case "--input":
			hasInput = true
		case "-f", "--raw-field", "-F", "--field":
			hasFields = true
			key, value, ok := strings.Cut(flag.value, "=")
			if (!ok && !strings.HasSuffix(key, "[]")) || key == "" {
				unknownFields = true
				continue
			}
			fields[key] = value
			fileFields[key] = (flag.name == "-F" || flag.name == "--field") && strings.HasPrefix(value, "@")
			dynamicFields[key] = flag.valueDynamic
		case "-H", "--header":
			if flag.valueDynamic {
				unknown("request header semantics are not statically available")
				return
			}
			name, _, ok := strings.Cut(flag.value, ":")
			if !ok || name == "" || strings.EqualFold(name, "X-HTTP-Method-Override") || strings.EqualFold(name, "X-Method-Override") {
				unknown("request header semantics are not statically available")
				return
			}
		}
	}
	method, explicitMethod, methodDynamic := ghFlagInfo(parsed.flags, "-X", "--method")
	if explicitMethod && methodDynamic {
		unknown("HTTP method is not statically available")
		return
	}
	if !explicitMethod {
		method = "GET"
		if hasFields || hasInput {
			method = "POST"
		}
	}
	switch method {
	case "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE":
	default:
		unknown("HTTP method is unsupported or dynamic")
		return
	}
	if endpoint == "graphql" {
		if unknownFields {
			unknown("GraphQL request field is not statically available")
			return
		}
		if hasInput {
			unknown("GraphQL input body is not statically available")
			return
		}
		if fileFields["query"] || fileFields["operationName"] || dynamicFields["query"] || dynamicFields["operationName"] {
			unknown("GraphQL document or operation name is not statically available")
			return
		}
		if method != "GET" && method != "POST" {
			unknown("unsupported GraphQL HTTP method")
			return
		}
		query, exists := fields["query"]
		if !exists || query == "" {
			unknown("GraphQL query is not statically available")
			return
		}
		if name, present := fields["operationName"]; present && name == "" {
			unknown("GraphQL operation name is empty or dynamic")
			return
		}
		parsed.risk, parsed.undecidable = classifyGhGraphQL(query, fields["operationName"])
		if parsed.risk == "" && parsed.undecidable == "" {
			parsed.allowDynamicArgs = true
		}
		return
	}
	if method == "GET" || method == "HEAD" || method == "OPTIONS" {
		parsed.allowDynamicArgs = true
		return
	}
	for _, operation := range ghRESTOperations {
		if !strings.Contains(" "+operation.methods+" ", " "+method+" ") || !operation.endpoint.MatchString(endpoint) {
			continue
		}
		switch operation.effect {
		case "remote":
			parsed.risk = "remote-code-ref-mutation"
		case "indirect":
			unknown("workflow execution content is not statically available")
		case "release-edit":
			if hasInput || unknownFields {
				unknown("release input can change or create a tag")
				return
			}
			for key := range fields {
				switch key {
				case "tag_name", "target_commitish":
					parsed.risk = "remote-code-ref-mutation"
				case "draft":
					if fields[key] != "true" || fileFields[key] {
						parsed.risk = "remote-code-ref-mutation"
					}
				case "name", "body", "prerelease", "make_latest", "discussion_category_name":
				default:
					unknown("unsupported release update field")
					return
				}
			}
		case "metadata":
			parsed.allowDynamicArgs = true
		}
		return
	}
	unknown("unsupported REST write endpoint or method")
}

func ghAPIEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" || u.Fragment != "" {
		return "", fmt.Errorf("unsupported API endpoint")
	}
	if u.IsAbs() && (u.Scheme != "https" || u.Host != "api.github.com" && u.Host != "uploads.github.com") {
		return "", fmt.Errorf("absolute endpoint is not a supported GitHub API host")
	}
	if !u.IsAbs() && u.Host != "" {
		return "", fmt.Errorf("unsupported endpoint host")
	}
	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimPrefix(path, "api/v3/")
	if path == "" || strings.ContainsAny(path, "\\\r\n\t ") {
		return "", fmt.Errorf("empty or unsupported API endpoint path")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("ambiguous API endpoint path")
		}
	}
	if path == "graphql" && u.RawQuery != "" {
		return "", fmt.Errorf("GraphQL URL query parameters are not supported")
	}
	return path, nil
}

var ghGraphQLRemoteMutations = strings.Fields(`
 createRef updateRef updateRefs deleteRef createCommitOnBranch mergeBranch mergePullRequest
 enablePullRequestAutoMerge enqueuePullRequest updatePullRequestBranch revertPullRequest
 createLinkedBranch cloneTemplateRepository deleteRepository forkRepository
 updateRepository archiveRepository unarchiveRepository
`)

var ghGraphQLMetadataMutations = strings.Fields(`
 addComment updateIssueComment deleteIssueComment createIssue updateIssue closeIssue reopenIssue
 deleteIssue transferIssue pinIssue unpinIssue lockLockable unlockLockable
 createPullRequest updatePullRequest closePullRequest reopenPullRequest
 convertPullRequestToDraft markPullRequestReadyForReview disablePullRequestAutoMerge dequeuePullRequest
 addPullRequestReview submitPullRequestReview updatePullRequestReview deletePullRequestReview
 dismissPullRequestReview addPullRequestReviewComment updatePullRequestReviewComment
 deletePullRequestReviewComment addPullRequestReviewThread resolveReviewThread unresolveReviewThread requestReviews
 addLabelsToLabelable removeLabelsFromLabelable addAssigneesToAssignable removeAssigneesFromAssignable
 createLabel updateLabel deleteLabel createDiscussion updateDiscussion deleteDiscussion
 addDiscussionComment updateDiscussionComment deleteDiscussionComment
 addReaction removeReaction createProjectV2 updateProjectV2 deleteProjectV2
 addProjectV2ItemById addProjectV2DraftIssue updateProjectV2ItemFieldValue
 clearProjectV2ItemFieldValue deleteProjectV2Item archiveProjectV2Item unarchiveProjectV2Item
 addStar removeStar updateSubscription
`)

func classifyGhGraphQL(query, operationName string) (string, string) {
	document, err := parser.ParseQueryWithTokenLimit(&ast.Source{Input: query}, 10000)
	if err != nil {
		return "", "gh api: invalid GraphQL document"
	}
	var operation *ast.OperationDefinition
	for _, candidate := range document.Operations {
		if operationName == "" || candidate.Name == operationName {
			if operation != nil {
				return "", "gh api: GraphQL operation selection is ambiguous"
			}
			operation = candidate
		}
	}
	if operation == nil {
		return "", "gh api: GraphQL operation is missing"
	}
	if operation.Operation == ast.Query {
		return "", ""
	}
	if operation.Operation != ast.Mutation {
		return "", "gh api: unsupported GraphQL operation"
	}
	fragments := make(map[string]*ast.FragmentDefinition)
	for _, fragment := range document.Fragments {
		if fragments[fragment.Name] != nil {
			return "", "gh api: duplicate GraphQL fragment"
		}
		fragments[fragment.Name] = fragment
	}
	return classifyGhMutationSelections(operation.SelectionSet, fragments, make(map[string]uint8), 0)
}

func classifyGhMutationSelections(selections ast.SelectionSet, fragments map[string]*ast.FragmentDefinition, visited map[string]uint8, depth int) (string, string) {
	if depth > 100 {
		return "", "gh api: GraphQL fragment nesting limit exceeded"
	}
	risk, reason := "", ""
	for _, selection := range selections {
		nextRisk, nextReason := "", ""
		switch selection := selection.(type) {
		case *ast.Field:
			switch {
			case slices.Contains(ghGraphQLRemoteMutations, selection.Name):
				nextRisk = "remote-code-ref-mutation"
			case slices.Contains(ghGraphQLMetadataMutations, selection.Name), selection.Name == "__typename":
			default:
				nextReason = "gh api: unsupported GraphQL mutation " + selection.Name
			}
		case *ast.InlineFragment:
			nextRisk, nextReason = classifyGhMutationSelections(selection.SelectionSet, fragments, visited, depth+1)
		case *ast.FragmentSpread:
			fragment := fragments[selection.Name]
			if fragment == nil || visited[selection.Name] == 1 {
				return "", "gh api: missing or cyclic GraphQL fragment"
			}
			if visited[selection.Name] == 2 {
				continue
			}
			visited[selection.Name] = 1
			nextRisk, nextReason = classifyGhMutationSelections(fragment.SelectionSet, fragments, visited, depth+1)
			visited[selection.Name] = 2
		}
		if nextRisk != "" {
			risk = nextRisk
		}
		if nextReason != "" {
			reason = nextReason
		}
	}
	return risk, reason
}
