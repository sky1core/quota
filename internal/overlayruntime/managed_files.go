package overlayruntime

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const codexRule = "AGENTS.override.md"

type managedInstructionFile struct {
	Path            string
	Data            []byte
	Previous        []byte
	Remove          bool
	OwnerExecutable bool
}

func localPathComparisonKey(path string) string {
	return norm.NFC.String(cases.Fold().String(path))
}

func validateLocalFiles(paths []string) error {
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" || filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00\r\n:*?[]") {
			return fmt.Errorf("invalid local file path %q", path)
		}
		parts := strings.Split(path, "/")
		if strings.EqualFold(parts[len(parts)-1], ".gitignore") {
			return fmt.Errorf("local file path %q changes Git ignore rules and cannot be copied", path)
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
				return fmt.Errorf("unsafe local file path %q", path)
			}
		}
		key := localPathComparisonKey(path)
		for _, reserved := range []string{sharedRule, localRule, codexRule, sharedBridge, localBridge} {
			reservedKey := localPathComparisonKey(reserved)
			if key == reservedKey || strings.HasPrefix(key, reservedKey+"/") {
				return fmt.Errorf("local file path %q is reserved", path)
			}
		}
		if seen[key] {
			return fmt.Errorf("duplicate local file path %q", path)
		}
		seen[key] = true
	}
	for path := range seen {
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("overlapping local file paths %q and %q", parent, path)
			}
		}
	}
	return nil
}

func managedParents(root, rel string, create bool) error {
	path := root
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			if parts[i] == "." {
				continue
			}
			path = filepath.Join(path, parts[i])
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && i >= 0 {
			if !create {
				return nil
			}
			if err = os.Mkdir(path, 0o700); err != nil {
				return err
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a regular directory", path)
		}
	}
	return nil
}

func (r repoContext) managedLocalSource(rel string, required bool) ([]byte, error) {
	if err := managedParents(r.Root, rel, false); err != nil {
		return nil, err
	}
	checkouts := []string{r.Root}
	if r.Bare() {
		checkouts = r.Checkouts()
	}
	for _, w := range checkouts {
		if err := r.managedGitTarget(w, rel, required && !r.Bare()); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(r.Root, rel)
	if rel == localRule {
		path = r.localSource()
	}
	for _, metadata := range []string{statePath(r), filepath.Join(r.Common, "quota-instructions.lock"), filepath.Join(r.Common, "info", "exclude")} {
		alias, _, err := sameExistingSettingsEntry(path, metadata)
		if err != nil {
			return nil, err
		}
		if path == metadata || alias {
			return nil, fmt.Errorf("local source %s is a managed repository metadata update path and cannot be copied", path)
		}
	}
	data, err := readRegular(path)
	if os.IsNotExist(err) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("local source %s: %w", path, err)
	}
	if rel == localRule {
		if _, err := decodeRule(data, path); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (r repoContext) managedGitTarget(w, rel string, requireIgnored bool) error {
	tracked, err := r.tracked(w, rel)
	if err != nil {
		return err
	}
	path := filepath.Join(w, rel)
	if tracked {
		return fmt.Errorf("%s is tracked; it must stay local-only", path)
	}
	if requireIgnored {
		ignored, err := r.ignored(w, rel)
		if err != nil {
			return err
		}
		if !ignored {
			return fmt.Errorf("%s is not git-ignored", path)
		}
	}
	return nil
}

func (r repoContext) managedExpectations(agent string, state RepositoryState) ([]managedInstructionFile, error) {
	if agent != "claude" && agent != "codex" && agent != "all" {
		return nil, fmt.Errorf("invalid managed instruction agent %q", agent)
	}
	if err := validateLocalFiles(state.LocalFiles); err != nil {
		return nil, err
	}
	local, err := r.managedLocalSource(localRule, false)
	if err != nil {
		return nil, err
	}
	var plans []managedInstructionFile
	if local == nil && contains(r.Checkouts(), r.Root) && (agent == "claude" || agent == "all") {
		path := filepath.Join(r.Root, localBridge)
		if state.Generated[path] != "" {
			plans = append(plans, managedInstructionFile{Path: path, Remove: true})
		}
	}
	for _, w := range r.Copies() {
		plans = append(plans, managedInstructionFile{Path: filepath.Join(w, localRule), Data: local, Remove: local == nil})
	}
	for _, rel := range state.LocalFiles {
		data, err := r.managedLocalSource(rel, true)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(filepath.Join(r.Root, rel))
		if err != nil {
			return nil, err
		}
		for _, w := range r.Copies() {
			plans = append(plans, managedInstructionFile{Path: filepath.Join(w, rel), Data: data, OwnerExecutable: info.Mode().Perm()&0o100 != 0})
		}
	}
	if agent == "codex" || agent == "all" {
		for _, w := range r.Checkouts() {
			plan := managedInstructionFile{Path: filepath.Join(w, codexRule), Remove: local == nil}
			if local != nil {
				expectation, err := r.sharedExpectation(w, state)
				if err != nil {
					return nil, err
				}
				plan.Data = mergedCodexInstructions(expectation.Data, local)
				if len(plan.Data) > maxRuleBytes {
					return nil, fmt.Errorf("%s exceeds %d-byte file size limit", plan.Path, maxRuleBytes)
				}
			}
			plans = append(plans, plan)
		}
	}
	active := plans[:0]
	for _, plan := range plans {
		if plan.Remove && !exists(plan.Path) && state.Generated[plan.Path] == "" {
			continue
		}
		active = append(active, plan)
	}
	return active, nil
}

func mergedCodexInstructions(shared, local []byte) []byte {
	data := append([]byte{}, shared...)
	if len(data) > 0 {
		if data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, '\n')
	}
	return append(data, local...)
}

func (r repoContext) managedDestination(path string, state RepositoryState) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", fmt.Errorf("invalid managed destination %q", path)
	}
	for _, w := range r.Checkouts() {
		rel, err := filepath.Rel(w, path)
		if err != nil {
			continue
		}
		if rel == codexRule || w == r.Root && rel == localBridge && state.Generated[path] != "" || w != r.Root && (rel == localRule || contains(state.LocalFiles, filepath.ToSlash(rel))) {
			return w, rel, nil
		}
	}
	return "", "", fmt.Errorf("%s is not a managed destination", path)
}

func (r repoContext) inspectManagedFile(plan managedInstructionFile, state RepositoryState, requireIgnored bool) ([]byte, error) {
	w, rel, err := r.managedDestination(plan.Path, state)
	if err != nil {
		return nil, err
	}
	if err = managedParents(w, rel, false); err != nil {
		return nil, err
	}
	if err = r.managedGitTarget(w, rel, requireIgnored); err != nil {
		return nil, err
	}
	previous, err := readRegular(plan.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if known := state.Generated[plan.Path]; known == "" || known != digest(previous) {
		return nil, fmt.Errorf("%s has user edits or unknown ownership; file preserved", plan.Path)
	}
	if err := checkRecordedGeneratedMode(plan.Path, state); err != nil {
		return nil, err
	}
	return previous, nil
}

func (r repoContext) planManagedFiles(agent string, state RepositoryState) ([]managedInstructionFile, error) {
	plans, err := r.managedExpectations(agent, state)
	if err != nil {
		return nil, err
	}
	var problems []error
	for i := range plans {
		plans[i].Previous, err = r.inspectManagedFile(plans[i], state, false)
		if err != nil {
			problems = append(problems, err)
		}
	}
	if err = errors.Join(problems...); err != nil {
		return nil, err
	}
	return plans, nil
}

func (r repoContext) applyManagedFiles(plans []managedInstructionFile, state *RepositoryState, changes *[]string) error {
	if err := validateLocalFiles(state.LocalFiles); err != nil {
		return err
	}
	var problems []error
	seen := make(map[string]bool)
	for _, plan := range plans {
		if seen[plan.Path] {
			problems = append(problems, fmt.Errorf("duplicate managed destination %s", plan.Path))
		}
		seen[plan.Path] = true
		previous, err := r.inspectManagedFile(plan, *state, true)
		if err != nil {
			problems = append(problems, err)
		} else if (previous == nil) != (plan.Previous == nil) || !bytes.Equal(previous, plan.Previous) {
			problems = append(problems, fmt.Errorf("%s changed after planning", plan.Path))
		}
		if !plan.Remove && len(plan.Data) > maxRuleBytes {
			problems = append(problems, fmt.Errorf("%s exceeds file size limit", plan.Path))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	if state.Generated == nil {
		state.Generated = make(map[string]string)
	}
	for _, plan := range plans {
		previous, err := r.inspectManagedFile(plan, *state, true)
		if err != nil {
			return err
		}
		if (previous == nil) != (plan.Previous == nil) || !bytes.Equal(previous, plan.Previous) {
			return fmt.Errorf("%s changed after planning", plan.Path)
		}
		if plan.Remove {
			if previous != nil {
				if err = os.Remove(plan.Path); err != nil {
					return err
				}
				*changes = append(*changes, plan.Path)
			}
			delete(state.Generated, plan.Path)
			delete(state.GeneratedModes, plan.Path)
			continue
		}
		if previous != nil && bytes.Equal(previous, plan.Data) {
			info, err := os.Lstat(plan.Path)
			if err != nil {
				return err
			}
			if info.Mode().Perm()&^0o700 == 0 && (info.Mode().Perm()&0o100 != 0) == plan.OwnerExecutable {
				if err := recordGeneratedFile(state, plan.Path, plan.Data); err != nil {
					return err
				}
				continue
			}
		}
		w, rel, err := r.managedDestination(plan.Path, *state)
		if err != nil {
			return err
		}
		if err = managedParents(w, rel, true); err != nil {
			return err
		}
		if err = atomicWriteFile(plan.Path, plan.Data, true, plan.OwnerExecutable, plan.Previous); err != nil {
			return err
		}
		if err := recordGeneratedFile(state, plan.Path, plan.Data); err != nil {
			return err
		}
		*changes = append(*changes, plan.Path)
	}
	return nil
}

func (r repoContext) checkManagedFiles(agent string, state RepositoryState) []string {
	plans, err := r.managedExpectations(agent, state)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, plan := range plans {
		previous, err := r.inspectManagedFile(plan, state, true)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if previous == nil {
			if !plan.Remove {
				problems = append(problems, plan.Path+" is missing")
			}
			continue
		}
		if plan.Remove || !bytes.Equal(previous, plan.Data) {
			problems = append(problems, plan.Path+" is stale")
		}
		info, err := os.Lstat(plan.Path)
		if err != nil {
			problems = append(problems, err.Error())
		} else if info.Mode().Perm()&^0o700 != 0 {
			problems = append(problems, plan.Path+" requires owner-only permissions")
		} else if (info.Mode().Perm()&0o100 != 0) != plan.OwnerExecutable {
			problems = append(problems, plan.Path+" has stale owner execution permissions")
		}
	}
	return problems
}
