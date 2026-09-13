package overlayruntime

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

func (r repoContext) plannedIgnoreProblems(agent string, state RepositoryState, planned map[string]plannedSettingsFile) []string {
	var problems []string
	exclude := filepath.Join(r.Common, "info", "exclude")
	for _, worktree := range r.Checkouts() {
		for _, rel := range append(ignoreTargets(agent), state.LocalFiles...) {
			out, err := gitOutputInput(r.Context, worktree, strings.NewReader(rel+"\x00"), "check-ignore", "--no-index", "-v", "-z", "--stdin")
			if _, err := gitBoolean(err); err != nil {
				problems = append(problems, err.Error())
				continue
			}
			fields := bytes.Split(out, []byte{0})
			if len(out) == 0 {
				continue
			}
			if len(fields) != 5 {
				problems = append(problems, fmt.Sprintf("cannot inspect Git ignore rule for %s in %s", rel, worktree))
				continue
			}
			if !bytes.HasPrefix(fields[2], []byte("!")) {
				continue
			}
			source := string(fields[0])
			if !filepath.IsAbs(source) {
				source = filepath.Join(worktree, source)
			}
			source = filepath.Clean(source)
			pattern := rel
			if contains(state.LocalFiles, rel) {
				pattern = "/" + strings.ReplaceAll(rel, " ", "\\ ")
			}
			if file, ok := planned[source]; ok && !file.Remove {
				before, err := readRegular(source)
				if err != nil {
					problems = append(problems, err.Error())
					continue
				}
				if !contains(strings.Split(string(before), "\n"), pattern) && contains(strings.Split(string(file.Data), "\n"), pattern) {
					continue
				}
			}
			if source != exclude && !(instructionPathWithin(source, worktree) && filepath.Base(source) == ".gitignore") {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s is still not ignored in %s; check negated ignore rules in %s", rel, worktree, source))
		}
	}
	return problems
}
