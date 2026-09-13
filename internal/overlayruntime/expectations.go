package overlayruntime

import (
	"bytes"
	"fmt"
	"path/filepath"
)

type sharedRuleExpectation struct {
	Path, Source string
	Data         []byte
	Present      bool
}

func (r repoContext) sharedExpectation(worktree string, state RepositoryState) (sharedRuleExpectation, error) {
	expectation := sharedRuleExpectation{Path: filepath.Join(worktree, sharedRule), Source: filepath.Join(worktree, sharedRule)}
	if state.SharedSource == "primary" {
		expectation.Source = filepath.Join(r.Root, sharedRule)
	}
	if !exists(expectation.Source) {
		return expectation, nil
	}
	data, err := readRegular(expectation.Source)
	if err != nil {
		return expectation, err
	}
	if _, err = decodeRule(data, expectation.Source); err != nil {
		return expectation, err
	}
	expectation.Data, expectation.Present = data, true
	return expectation, nil
}
func (r repoContext) requiredSourceProblem(state RepositoryState) error {
	shared := filepath.Join(r.Top, sharedRule)
	if state.SharedSource == "primary" {
		shared = filepath.Join(r.Root, sharedRule)
	}
	if !exists(shared) && !exists(r.localSource()) {
		return fmt.Errorf("instruction sources are missing: create AGENTS.md and/or AGENTS.local.md first")
	}
	return nil
}
func (r repoContext) inspectSharedCopy(expectation sharedRuleExpectation, state RepositoryState) ([]byte, error) {
	if !expectation.Present {
		return nil, fmt.Errorf("primary shared source is missing: %s", expectation.Source)
	}
	tracked, err := r.tracked(filepath.Dir(expectation.Path), sharedRule)
	if err != nil {
		return nil, err
	}
	if !exists(expectation.Path) {
		if tracked {
			return nil, fmt.Errorf("%s is tracked and missing; checkout deletion preserved", expectation.Path)
		}
		return nil, nil
	}
	previous, err := readRegular(expectation.Path)
	if err != nil {
		return nil, err
	}
	known := state.Generated[expectation.Path]
	if known != "" && known != digest(previous) {
		return nil, fmt.Errorf("%s has user edits or unknown ownership; primary copy preserved", expectation.Path)
	}
	if known != "" {
		if err := checkRecordedGeneratedMode(expectation.Path, state); err != nil {
			return nil, err
		}
	}
	if bytes.Equal(previous, expectation.Data) && (known != "" || tracked) {
		return previous, nil
	}
	if known == "" {
		return nil, fmt.Errorf("%s has user edits or unknown ownership; primary copy preserved", expectation.Path)
	}
	if tracked {
		return nil, fmt.Errorf("%s is tracked; cannot replace checkout source", expectation.Path)
	}
	return previous, nil
}
func (r repoContext) inspectLocalCopy(worktree string, state RepositoryState) ([]byte, error) {
	path := filepath.Join(worktree, localBridge)
	if !exists(path) {
		return nil, nil
	}
	previous, err := readRegular(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(previous, []byte(localOwner+"\n")) {
		return nil, fmt.Errorf("%s is not overlay-generated; move its content into the primary %s", path, localRule)
	}
	if known, ok := state.Generated[path]; ok {
		if known != digest(previous) {
			return nil, fmt.Errorf("%s has user edits; generated copy preserved", path)
		}
	} else {
		return nil, fmt.Errorf("%s ownership is not verified; generated copy preserved", path)
	}
	return previous, checkRecordedGeneratedMode(path, state)
}
