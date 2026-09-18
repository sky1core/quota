package overlayruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	goldmarktext "github.com/yuin/goldmark/text"
	"golang.org/x/sys/unix"
)

type PrepareSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type PrepareResult struct {
	Checkout     string `json:"checkout"`
	Primary      string `json:"primary"`
	LocalPresent bool   `json:"localPresent"`
	LocalBody    string `json:"-"`
	// OverrideBody is the merged AGENTS.override.md content when this call
	// wrote it.
	OverrideBody string        `json:"-"`
	Created      []string      `json:"created,omitempty"`
	Updated      []string      `json:"updated,omitempty"`
	Removed      []string      `json:"removed,omitempty"`
	Skipped      []PrepareSkip `json:"skipped,omitempty"`
}

func (p PrepareResult) Changed(path string) bool {
	return contains(p.Created, path) || contains(p.Updated, path)
}

func (p PrepareResult) SkipReasons() []string {
	var out []string
	for _, s := range p.Skipped {
		out = append(out, s.Path+": "+s.Reason)
	}
	return out
}

type generatedFile struct {
	Path, Rel       string
	Data            []byte
	Private         bool
	OwnerExecutable bool
	Bridge          bool
	SourceErr       error
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

func sharedRuleProblem(w string) string {
	path := filepath.Join(w, sharedRule)
	if !exists(path) {
		return ""
	}
	data, err := readRegular(path)
	if err != nil {
		return err.Error()
	}
	if _, err = decodeRule(data, path); err != nil {
		return err.Error()
	}
	return ""
}

func claudeSharedBridgeFindings(worktree string) []string {
	shared := filepath.Join(worktree, sharedRule)
	if !exists(shared) {
		return nil
	}
	bridge := filepath.Join(worktree, "CLAUDE.md")
	data, err := readRegular(bridge)
	if os.IsNotExist(err) {
		return []string{bridge + " is missing; Claude does not load " + shared + " without an @AGENTS.md import"}
	}
	if err != nil {
		return []string{err.Error()}
	}
	if !importsShared(data) {
		return []string{bridge + " does not import @AGENTS.md; Claude does not load " + shared}
	}
	return nil
}

func bridgeSource(b []byte) ([]byte, bool) {
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return nil, false
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return []byte(text), true
}

func bridgeLines(b []byte) []string {
	source, ok := bridgeSource(b)
	if !ok {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(source), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " \t"))
		}
	}
	return lines
}

// bridgeImportsLocal accepts only a single-line CLAUDE.local.md import.
func bridgeImportsLocal(b []byte) bool {
	lines := bridgeLines(b)
	return len(lines) == 1 && (lines[0] == "@"+localRule || lines[0] == "@./"+localRule)
}

var sharedImportRe = regexp.MustCompile(`(^|[\s(\[{;:])@(\./)?` + regexp.QuoteMeta(sharedRule) + `($|[\s)\]};:,])`)

// importsShared reports whether any non-code part of CLAUDE.md imports AGENTS.md.
func importsShared(b []byte) bool {
	source, ok := bridgeSource(b)
	if !ok {
		return false
	}
	root := goldmark.DefaultParser().Parse(goldmarktext.NewReader(source))
	found := false
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if found {
			return ast.WalkStop, nil
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindCodeSpan, ast.KindHTMLBlock, ast.KindRawHTML:
			return ast.WalkSkipChildren, nil
		case ast.KindText:
			if sharedImportRe.Match(n.(*ast.Text).Text(source)) {
				found = true
				return ast.WalkStop, nil
			}
		case ast.KindString:
			s := n.(*ast.String)
			if !s.IsCode() && !s.IsRaw() && sharedImportRe.Match(s.Text(source)) {
				found = true
				return ast.WalkStop, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return found
}

// readLocalSource returns the primary AGENTS.local.md bytes and decoded text, or
// nil bytes when the file is absent.
func (r repoContext) readLocalSource() ([]byte, string, error) {
	p := r.localSource()
	if !exists(p) {
		return nil, "", nil
	}
	if !r.Bare() {
		tracked, e := r.tracked(r.Root, localRule)
		if e != nil {
			return nil, "", e
		}
		if tracked {
			return nil, "", fmt.Errorf("%s is tracked; it must stay local-only", p)
		}
	}
	data, e := readRegular(p)
	if e != nil {
		return nil, "", e
	}
	text, e := decodeRule(data, p)
	if e != nil {
		return nil, "", e
	}
	return data, text, nil
}

func readSharedSource(w string) ([]byte, error) {
	shared := filepath.Join(w, sharedRule)
	if !exists(shared) {
		return nil, nil
	}
	data, err := readRegular(shared)
	if err == nil {
		_, err = decodeRule(data, shared)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// planGenerated lists every quota-generated file of checkout w. Data is nil
// when the local source is absent, which marks the file for removal.
func (r repoContext) planGenerated(w string, state RepositoryState, local []byte) []generatedFile {
	var plans []generatedFile
	bridge := generatedFile{Path: filepath.Join(w, localBridge), Rel: localBridge, Bridge: true}
	override := generatedFile{Path: filepath.Join(w, codexRule), Rel: codexRule, Private: true}
	sharedData, sharedErr := readSharedSource(w)
	if sharedErr != nil {
		override.SourceErr = sharedErr
	}
	if local != nil {
		bridge.Data = []byte(localBridgeBody)
		override.Data = mergedCodexInstructions(sharedData, local)
		if len(override.Data) > maxRuleBytes {
			override.SourceErr = fmt.Errorf("merged instructions exceed %d-byte file size limit", maxRuleBytes)
		}
	}
	plans = append(plans, bridge, override)
	if w == r.Root {
		return plans
	}
	copyLocal := generatedFile{Path: filepath.Join(w, localRule), Rel: localRule, Private: true, Data: local}
	plans = append(plans, copyLocal)
	for _, rel := range state.LocalFiles {
		plan := generatedFile{Path: filepath.Join(w, rel), Rel: rel, Private: true}
		data, err := r.localFileSource(rel)
		if err != nil {
			plan.SourceErr = err
		} else {
			if local != nil {
				plan.Data = data
			}
			info, err := os.Lstat(filepath.Join(r.Root, rel))
			if err != nil {
				plan.SourceErr = err
			} else {
				plan.OwnerExecutable = info.Mode().Perm()&0o100 != 0
			}
		}
		plans = append(plans, plan)
	}
	return plans
}

// inspectGenerated reads the current file at path and decides ownership.
// A problem means the file must be preserved untouched.
func inspectGenerated(path string, state RepositoryState) (current []byte, owned bool, problem error) {
	current, _, _, owned, problem = inspectGeneratedWithStat(path, state)
	return current, owned, problem
}

func inspectGeneratedWithStat(path string, state RepositoryState) (current []byte, stat unix.Stat_t, statOK bool, owned bool, problem error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, stat, false, false, nil
	}
	if err != nil {
		return nil, stat, false, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, stat, false, false, fmt.Errorf("is not a regular file; preserved")
	}
	current, stat, err = readRegularWithStat(path)
	if err != nil {
		return nil, stat, false, false, err
	}
	statOK = true
	known := state.Generated[path]
	if known == "" {
		return current, stat, statOK, false, nil
	}
	if known != digest(current) {
		return current, stat, statOK, true, fmt.Errorf("has user edits; preserved")
	}
	if err := checkRecordedGeneratedMode(path, state); err != nil {
		return current, stat, statOK, true, fmt.Errorf("permissions changed after generation; preserved")
	}
	return current, stat, statOK, true, nil
}

func generatedModeMatches(path string, plan generatedFile) bool {
	if !plan.Private {
		return true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&^0o700 == 0 && (info.Mode().Perm()&0o100 != 0) == plan.OwnerExecutable
}

type fileAction int

const (
	actionNone fileAction = iota
	actionCreate
	actionUpdate
	actionRemove
	actionSkip
)

// plannedAction is the decision for one generated path. It is computed
// without writing so that preparation and status share one evaluation.
type plannedAction struct {
	Path           string
	Action         fileAction
	Reason         string
	plan           generatedFile
	current        []byte
	currentStat    unix.Stat_t
	hasCurrentStat bool
	// forget drops a stale ownership record whose file no longer exists.
	forget bool
	// recordMode backfills the permission record of an unchanged owned file.
	recordMode bool
}

// evaluateGenerated runs the same guards for every generated path — parent
// directories, Git tracking, ownership, source errors — before deciding
// whether the path is created, refreshed, removed, or left alone.
func (r repoContext) evaluateGenerated(w string, plan generatedFile, state RepositoryState) plannedAction {
	return r.evaluateGeneratedWithOptions(w, plan, state, false)
}

func (r repoContext) evaluateGeneratedForRemoval(w string, plan generatedFile, state RepositoryState) plannedAction {
	return r.evaluateGeneratedWithOptions(w, plan, state, true)
}

func (r repoContext) evaluateGeneratedWithOptions(w string, plan generatedFile, state RepositoryState, ignoreSourceErr bool) plannedAction {
	a := plannedAction{Path: plan.Path, plan: plan}
	skip := func(reason string) plannedAction {
		a.Action, a.Reason = actionSkip, reason
		return a
	}
	if err := managedParents(w, plan.Rel, false); err != nil {
		return skip(err.Error())
	}
	tracked, err := r.tracked(w, plan.Rel)
	if err != nil {
		return skip(err.Error())
	}
	if tracked {
		return skip("is tracked; it must stay local-only")
	}
	current, stat, statOK, owned, problem := inspectGeneratedWithStat(plan.Path, state)
	if problem != nil {
		return skip(problem.Error())
	}
	a.current = current
	a.currentStat, a.hasCurrentStat = stat, statOK
	if plan.SourceErr != nil && !ignoreSourceErr {
		return skip(plan.SourceErr.Error())
	}
	if plan.Data == nil {
		switch {
		case current == nil:
			a.forget = state.Generated[plan.Path] != ""
		case owned:
			a.Action = actionRemove
		case plan.Rel == codexRule:
			return skip("exists and is not quota-generated; preserved")
		}
		return a
	}
	if current != nil && !owned {
		if plan.Bridge && bridgeImportsLocal(current) {
			return a
		}
		return skip("exists and is not quota-generated; preserved")
	}
	ignored, err := r.ignored(w, plan.Rel)
	if err != nil {
		return skip(err.Error())
	}
	if !ignored {
		return skip(fmt.Sprintf("is not git-ignored; add the line %q to the global git ignore file or to the repository .gitignore", plan.Rel))
	}
	if current != nil && bytes.Equal(current, plan.Data) && generatedModeMatches(plan.Path, plan) {
		_, recorded := state.GeneratedModes[plan.Path]
		a.recordMode = !recorded
		return a
	}
	if current == nil {
		a.Action = actionCreate
	} else {
		a.Action = actionUpdate
	}
	return a
}

// orphanPlans lists recorded generated files of checkout w that the current
// plan no longer produces, so they can be removed under the ownership rules.
// Shared AGENTS.md copies and CLAUDE.md bridges of earlier versions are left
// to the user because they may be the checkout's active native instruction path.
func orphanPlans(w string, state RepositoryState, plans []generatedFile) []generatedFile {
	planned := map[string]bool{}
	for _, plan := range plans {
		planned[plan.Path] = true
	}
	var orphans []generatedFile
	for path := range state.Generated {
		if planned[path] {
			continue
		}
		rel, err := filepath.Rel(w, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || rel == "CLAUDE.md" || rel == sharedRule {
			continue
		}
		orphans = append(orphans, generatedFile{Path: path, Rel: rel})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Path < orphans[j].Path })
	return orphans
}

// evaluateWithSource reads the primary AGENTS.local.md and evaluates checkout
// w from it. A source error yields no actions; preparation aborts on it and
// status reports it instead of any file decision.
func (r repoContext) evaluateWithSource(w string, state RepositoryState) (actions []plannedAction, local []byte, text string, err error) {
	local, text, err = r.readLocalSource()
	if err != nil {
		return nil, nil, "", err
	}
	return r.evaluateCheckout(w, state, local), local, text, nil
}

// evaluateCheckout decides every generated path of checkout w, including
// recorded files the plan no longer produces, without writing anything.
func (r repoContext) evaluateCheckout(w string, state RepositoryState, local []byte) []plannedAction {
	plans := r.planGenerated(w, state, local)
	plans = append(plans, orphanPlans(w, state, plans)...)
	actions := make([]plannedAction, 0, len(plans))
	for _, plan := range plans {
		actions = append(actions, r.evaluateGenerated(w, plan, state))
	}
	return actions
}

func (r repoContext) evaluateCheckoutRemoval(w string, state RepositoryState) []plannedAction {
	plans := r.planGenerated(w, state, nil)
	plans = append(plans, orphanPlans(w, state, plans)...)
	actions := make([]plannedAction, 0, len(plans))
	for _, plan := range plans {
		actions = append(actions, r.evaluateGeneratedForRemoval(w, plan, state))
	}
	return actions
}

func removeGeneratedFile(path string, state RepositoryState, expected []byte, expectedStat unix.Stat_t, hasExpectedStat bool) (bool, error) {
	if expected == nil || !hasExpectedStat {
		return false, fmt.Errorf("changed before removal")
	}
	current, currentStat, currentStatOK, owned, problem := inspectGeneratedWithStat(path, state)
	if problem != nil {
		return false, problem
	}
	if current == nil {
		return false, nil
	}
	if !owned {
		return false, fmt.Errorf("exists and is not quota-generated; preserved")
	}
	if !currentStatOK || !sameFileSnapshot(currentStat, expectedStat) || !bytes.Equal(current, expected) {
		return false, fmt.Errorf("changed before removal")
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func (r repoContext) applyAction(w string, a plannedAction, state *RepositoryState, res *PrepareResult) {
	skip := func(reason string) { res.Skipped = append(res.Skipped, PrepareSkip{Path: a.Path, Reason: reason}) }
	switch a.Action {
	case actionSkip:
		skip(a.Reason)
	case actionNone:
		if a.forget {
			forgetGeneratedFile(state, a.Path)
		}
		if a.recordMode {
			if err := recordGeneratedFile(state, a.Path, a.plan.Data); err != nil {
				skip(err.Error())
			}
		}
	case actionRemove:
		removed, err := removeGeneratedFile(a.Path, *state, a.current, a.currentStat, a.hasCurrentStat)
		if err != nil {
			skip(err.Error())
			return
		}
		forgetGeneratedFile(state, a.Path)
		if removed {
			res.Removed = append(res.Removed, a.Path)
		}
	case actionCreate, actionUpdate:
		if err := managedParents(w, a.plan.Rel, true); err != nil {
			skip(err.Error())
			return
		}
		if err := atomicWriteFile(a.Path, a.plan.Data, a.plan.Private, a.plan.OwnerExecutable, a.current); err != nil {
			skip(err.Error())
			return
		}
		if err := recordGeneratedFile(state, a.Path, a.plan.Data); err != nil {
			skip(err.Error())
			return
		}
		if a.plan.Rel == codexRule {
			res.OverrideBody = string(a.plan.Data)
		}
		if a.Action == actionCreate {
			res.Created = append(res.Created, a.Path)
		} else {
			res.Updated = append(res.Updated, a.Path)
		}
	}
}

func (r repoContext) prepareCheckoutFiles(w string, state *RepositoryState, res *PrepareResult) error {
	actions, local, text, err := r.evaluateWithSource(w, *state)
	if err != nil {
		return err
	}
	res.LocalPresent, res.LocalBody = local != nil, text
	for _, a := range actions {
		r.applyAction(w, a, state, res)
	}
	return nil
}

// PrepareCheckout creates, refreshes, or removes the quota-generated files of
// the checkout containing dir according to the primary AGENTS.local.md.
func PrepareCheckout(ctx context.Context, dir string) (PrepareResult, error) {
	var res PrepareResult
	if e := ValidateGitEnvironment(ctx); e != nil {
		return res, e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return res, e
	}
	if problems := r.topologyProblems(); len(problems) > 0 {
		return res, errors.New(strings.Join(problems, "; "))
	}
	res.Checkout, res.Primary = r.Top, r.Root
	e = withStateLock(r, func() error {
		state, e := readState(r)
		if e != nil {
			return e
		}
		if e = r.prepareCheckoutFiles(r.Top, &state, &res); e != nil {
			return e
		}
		return writeState(r, state)
	})
	return res, e
}
