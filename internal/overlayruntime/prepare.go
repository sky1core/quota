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
	SharedBody   string `json:"-"`
	// OverrideBody is the merged AGENTS.override.md content when this call
	// wrote it.
	OverrideBody       string        `json:"-"`
	ClaudeSharedBefore []string      `json:"-"`
	ClaudeLocalBefore  []string      `json:"-"`
	ClaudeSharedAfter  []string      `json:"-"`
	ClaudeLocalAfter   []string      `json:"-"`
	Created            []string      `json:"created,omitempty"`
	Updated            []string      `json:"updated,omitempty"`
	Removed            []string      `json:"removed,omitempty"`
	Skipped            []PrepareSkip `json:"skipped,omitempty"`
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
	Path, Rel           string
	Data                []byte
	Private             bool
	OwnerExecutable     bool
	Bridge              bool
	SourceErr           error
	RequiresAnyPrepared []string
}

type claudeInstructionFile struct {
	Path    string
	Local   bool
	Planned bool
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

func agentsBridgeImportsLocal(b []byte) bool {
	lines := bridgeLines(b)
	return len(lines) == 1 && lines[0] == "@../"+localRule
}

func importRefs(rel string) []string {
	rel = filepath.ToSlash(rel)
	refs := []string{rel}
	if rel != "" && !strings.HasPrefix(rel, ".") && !strings.HasPrefix(rel, "/") {
		refs = append(refs, "./"+rel)
	}
	return refs
}

func importRefPattern(rel string) *regexp.Regexp {
	var quoted []string
	for _, ref := range importRefs(rel) {
		quoted = append(quoted, regexp.QuoteMeta(ref))
	}
	return regexp.MustCompile(`(^|[\s(\[{;:])@(` + strings.Join(quoted, "|") + `)($|[\s)\]};:,!?]|[.](?:$|\s))`)
}

func bridgeImportsPath(b []byte, rel string) bool {
	source, ok := bridgeSource(b)
	if !ok {
		return false
	}
	re := importRefPattern(rel)
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
			if re.Match(n.(*ast.Text).Text(source)) {
				found = true
				return ast.WalkStop, nil
			}
		case ast.KindString:
			s := n.(*ast.String)
			if !s.IsCode() && !s.IsRaw() && re.Match(s.Text(source)) {
				found = true
				return ast.WalkStop, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return found
}

func claudeFileImports(path, target string) (bool, error) {
	data, err := readRegular(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(filepath.Dir(path), target)
	if err != nil {
		return false, err
	}
	return bridgeImportsPath(data, rel), nil
}

func (r repoContext) claudeInstructionFiles(w string, state RepositoryState, ignoreOwnedGenerated bool, local []byte) []claudeInstructionFile {
	userClaudeMD := resolvePath(filepath.Join(nativeConfigHome("CLAUDE_CONFIG_DIR", ".claude"), "CLAUDE.md"))
	start := r.Start
	if !within(start, w) {
		start = w
	}
	var files []claudeInstructionFile
	seen := map[string]bool{}
	for dir := resolvePath(start); ; dir = filepath.Dir(dir) {
		for _, rel := range []string{"CLAUDE.md", filepath.Join(".claude", "CLAUDE.md"), "CLAUDE.local.md"} {
			path := filepath.Join(dir, rel)
			resolved := resolvePath(path)
			plannedCopy := false
			if !exists(path) && ignoreOwnedGenerated && within(path, w) {
				checkoutRel, err := filepath.Rel(w, path)
				sourceRel := filepath.ToSlash(checkoutRel)
				plannedCopy = err == nil && contains(state.LocalFiles, sourceRel) && r.localFileCopyPrepared(w, state, sourceRel, local != nil)
			}
			if seen[resolved] || resolved == userClaudeMD || (!exists(path) && !plannedCopy) {
				continue
			}
			seen[resolved] = true
			if plannedCopy {
				files = append(files, claudeInstructionFile{Path: path, Local: filepath.Base(path) == "CLAUDE.local.md", Planned: true})
				continue
			}
			if ignoreOwnedGenerated && within(path, w) {
				checkoutRel, err := filepath.Rel(w, path)
				sourceRel := filepath.ToSlash(checkoutRel)
				if err == nil && (isClaudeBridgeRel(checkoutRel) || !contains(state.LocalFiles, sourceRel) || local == nil) {
					_, _, _, owned, problem := inspectGeneratedWithStat(path, state)
					if owned && problem == nil {
						tracked, err := r.tracked(w, checkoutRel)
						if err == nil && !tracked {
							continue
						}
					}
				}
			}
			files = append(files, claudeInstructionFile{Path: path, Local: filepath.Base(path) == "CLAUDE.local.md"})
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return files
}

func claudeFilesImportTarget(files []claudeInstructionFile, target string) (bool, error) {
	for _, file := range files {
		if file.Local {
			continue
		}
		imports, err := claudeFileImports(file.Path, target)
		if err != nil {
			return false, err
		}
		if imports {
			return true, nil
		}
	}
	return false, nil
}

func claudeFilePaths(files []claudeInstructionFile) string {
	var paths []string
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return strings.Join(paths, ", ")
}

func (r repoContext) claudeBridgePlan(w string, state RepositoryState, local []byte) generatedFile {
	rel, body := claudeAgentsRule, claudeAgentsRuleBody
	files := r.claudeInstructionFiles(w, state, true, local)
	if len(files) > 0 {
		rel, body = claudeBridgeRule, claudeBridgeRuleBody
	}
	plan := generatedFile{Path: filepath.Join(w, rel), Rel: rel, Private: true, Bridge: true}
	hasExisting := false
	for _, file := range files {
		if !file.Planned {
			hasExisting = true
			break
		}
	}
	if !hasExisting {
		for _, file := range files {
			if file.Planned {
				plan.RequiresAnyPrepared = append(plan.RequiresAnyPrepared, file.Path)
			}
		}
	}
	if local != nil {
		plan.Data = []byte(body)
	}
	return plan
}

func claudePlannedBridge(actions []plannedAction) (generatedFile, bool) {
	for _, action := range actions {
		if action.plan.Bridge && action.plan.Rel == claudeBridgeRule && len(action.plan.RequiresAnyPrepared) > 0 {
			return action.plan, true
		}
	}
	return generatedFile{}, false
}

func removeCurrentGeneratedFile(path string, state *RepositoryState) (bool, error) {
	current, _, _, owned, problem := inspectGeneratedWithStat(path, *state)
	if problem != nil {
		return false, problem
	}
	if current == nil {
		forgetGeneratedFile(state, path)
		return false, nil
	}
	if !owned {
		return false, fmt.Errorf("exists and is not quota-generated; preserved")
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	forgetGeneratedFile(state, path)
	return true, nil
}

func removePathValue(paths []string, path string) []string {
	out := paths[:0]
	for _, existing := range paths {
		if existing != path {
			out = append(out, existing)
		}
	}
	return out
}

func (r repoContext) rollbackUnbridgedClaudeCopies(w string, state *RepositoryState, res *PrepareResult, actions []plannedAction, before map[string]bool, excludePatterns []string) {
	bridge, ok := claudePlannedBridge(actions)
	if !ok || r.claudePreparedBridgeReady(w, *state, excludePatterns, "") {
		return
	}
	for _, path := range bridge.RequiresAnyPrepared {
		if before[path] || !exists(path) {
			continue
		}
		removed, err := removeCurrentGeneratedFile(path, state)
		if err != nil {
			res.Skipped = append(res.Skipped, PrepareSkip{Path: path, Reason: err.Error()})
			continue
		}
		if removed {
			res.Created = removePathValue(res.Created, path)
			res.Updated = removePathValue(res.Updated, path)
			res.Skipped = append(res.Skipped, PrepareSkip{Path: path, Reason: "replacement Claude bridge was not prepared; rolled back"})
		}
	}
	if !before[bridge.Path] && exists(bridge.Path) {
		removed, err := removeCurrentGeneratedFile(bridge.Path, state)
		if err != nil {
			res.Skipped = append(res.Skipped, PrepareSkip{Path: bridge.Path, Reason: err.Error()})
			return
		}
		if removed {
			res.Created = removePathValue(res.Created, bridge.Path)
			res.Updated = removePathValue(res.Updated, bridge.Path)
			res.Skipped = append(res.Skipped, PrepareSkip{Path: bridge.Path, Reason: "replacement Claude bridge was not prepared; rolled back"})
		}
	}
}

func (r repoContext) claudeNativeLoadPaths(w string, state RepositoryState) (sharedPaths, localPaths []string) {
	shared := filepath.Join(w, sharedRule)
	local := filepath.Join(w, localRule)
	files := r.claudeInstructionFiles(w, state, false, nil)
	for _, file := range files {
		if file.Local {
			if exists(local) {
				if imports, err := claudeFileImports(file.Path, local); err == nil && imports {
					localPaths = append(localPaths, file.Path)
				}
			}
			continue
		}
		if exists(shared) {
			if imports, err := claudeFileImports(file.Path, shared); err == nil && imports {
				sharedPaths = append(sharedPaths, file.Path)
			}
		}
		if exists(local) {
			if imports, err := claudeFileImports(file.Path, local); err == nil && imports {
				localPaths = append(localPaths, file.Path)
			}
		}
	}
	if len(files) == 0 {
		if exists(shared) {
			sharedPaths = append(sharedPaths, shared)
		}
		agents := filepath.Join(w, claudeAgentsRule)
		if exists(local) {
			if imports, err := claudeFileImports(agents, local); err == nil && imports {
				localPaths = append(localPaths, agents)
			}
		}
	}
	return uniqueStrings(sharedPaths...), uniqueStrings(localPaths...)
}

func bridgeImportsRequiredLocal(plan generatedFile, current []byte) bool {
	switch plan.Rel {
	case claudeAgentsRule:
		return agentsBridgeImportsLocal(current)
	case claudeBridgeRule:
		return bridgeImportsPath(current, "../"+localRule)
	default:
		return false
	}
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
	claudeLocal := r.claudeBridgePlan(w, state, local)
	override := generatedFile{Path: filepath.Join(w, codexRule), Rel: codexRule, Private: true}
	sharedData, sharedErr := readSharedSource(w)
	if sharedErr != nil {
		override.SourceErr = sharedErr
	}
	if local != nil {
		override.Data = mergedCodexInstructions(sharedData, local)
		if int64(len(override.Data)) > codexInstructionMaxBytes {
			override.SourceErr = fmt.Errorf("merged %s is %d bytes, over quota %d-byte Codex instruction limit", codexRule, len(override.Data), codexInstructionMaxBytes)
		}
	}
	plans = append(plans, claudeLocal, override)
	if w == r.Root {
		return plans
	}
	copyLocal := generatedFile{Path: filepath.Join(w, localRule), Rel: localRule, Private: true, Data: local}
	var copyPlans []generatedFile
	copyPlans = append(copyPlans, copyLocal)
	for _, rel := range state.LocalFiles {
		copyPlans = append(copyPlans, r.localFileCopyPlan(w, rel, local != nil))
	}
	if !r.claudeBridgeCanBePrepared(w, state, claudeLocal) {
		for i := range copyPlans {
			if contains(claudeLocal.RequiresAnyPrepared, copyPlans[i].Path) {
				copyPlans[i].SourceErr = fmt.Errorf("replacement Claude bridge was not prepared; preserved")
			}
		}
	}
	return append(copyPlans, claudeLocal, override)
}

func (r repoContext) localFileCopyPlan(w, rel string, enabled bool) generatedFile {
	plan := generatedFile{Path: filepath.Join(w, rel), Rel: rel, Private: true}
	data, err := r.localFileSource(rel)
	if err != nil {
		plan.SourceErr = err
		return plan
	}
	if enabled {
		plan.Data = data
	}
	info, err := os.Lstat(filepath.Join(r.Root, rel))
	if err != nil {
		plan.SourceErr = err
	} else {
		plan.OwnerExecutable = info.Mode().Perm()&0o100 != 0
	}
	return plan
}

func (r repoContext) localFileCopyPrepared(w string, state RepositoryState, rel string, enabled bool) bool {
	if !enabled {
		return false
	}
	plan := r.localFileCopyPlan(w, rel, true)
	if plan.SourceErr != nil || plan.Data == nil {
		return false
	}
	action := r.evaluateGenerated(w, plan, state)
	return action.Action == actionNone || action.Action == actionCreate || action.Action == actionUpdate
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
	// removeRequiresClaudeBridge preserves a stale Claude bridge until its
	// replacement is actually present, so upgrades do not delete the only
	// working local-instruction link when the new ignored path is not ready.
	removeRequiresClaudeBridge bool
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
		case plan.Rel == codexRule || plan.Rel == claudeAgentsRule || (plan.Rel == claudeBridgeRule && state.Generated[plan.Path] != ""):
			return skip("exists and is not quota-generated; preserved")
		}
		return a
	}
	if current != nil && !owned {
		if plan.Bridge && bridgeImportsRequiredLocal(plan, current) {
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
// Shared AGENTS.md copies of earlier versions are left to the user because
// they may be the checkout's active native instruction path. Generated
// CLAUDE.md and CLAUDE.local.md bridges are removed under the normal ownership
// checks so they do not block Claude's AGENTS.md loader.
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
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || rel == sharedRule {
			continue
		}
		orphans = append(orphans, generatedFile{Path: path, Rel: rel})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Path < orphans[j].Path })
	return orphans
}

func isClaudeBridgeRel(rel string) bool {
	return rel == "CLAUDE.md" || rel == "CLAUDE.local.md" || rel == claudeAgentsRule || rel == claudeBridgeRule
}

func (r repoContext) claudePreparedBridgeReady(w string, state RepositoryState, excludePatterns []string, replacing string) bool {
	local := filepath.Join(w, localRule)
	if !exists(local) {
		return false
	}
	plan := r.claudeBridgePlan(w, state, []byte{'\n'})
	if resolvePath(plan.Path) == resolvePath(replacing) {
		return false
	}
	if !r.claudeBridgeSettingsAllow(plan, excludePatterns) {
		return false
	}
	action := r.evaluateGenerated(w, plan, state)
	if action.Action != actionNone {
		return false
	}
	current, _, _, _, problem := inspectGeneratedWithStat(plan.Path, state)
	if problem != nil || current == nil {
		return false
	}
	return bridgeImportsRequiredLocal(plan, current)
}

func (r repoContext) claudeBridgeSettingsAllow(plan generatedFile, excludePatterns []string) bool {
	mode, _ := claudeEffectiveInstructionMode(r)
	if mode == claudeModeManagedOnly {
		return false
	}
	if plan.Rel == claudeAgentsRule {
		if mode == claudeModeClaudeOnly {
			return false
		}
	}
	if !claudePathLoaded([]string{plan.Path}, excludePatterns) {
		return false
	}
	return true
}

func (r repoContext) claudeBridgeCanBePrepared(w string, state RepositoryState, plan generatedFile) bool {
	if !r.claudeBridgeSettingsAllow(plan, claudeEffectiveExclusionPatterns(r)) {
		return false
	}
	action := r.evaluateGenerated(w, plan, state)
	return action.Action == actionNone || action.Action == actionCreate || action.Action == actionUpdate
}

func (r repoContext) claudeBridgeReadyAfterActions(w string, state RepositoryState, actions []plannedAction, excludePatterns []string, replacing string) bool {
	if !localRuleReadyAfterActions(w, state, actions) {
		return false
	}
	plan := r.claudeBridgePlan(w, state, []byte{'\n'})
	if resolvePath(plan.Path) == resolvePath(replacing) {
		return false
	}
	if !r.claudeBridgeSettingsAllow(plan, excludePatterns) {
		return false
	}
	for _, action := range actions {
		if action.Path != plan.Path {
			continue
		}
		switch action.Action {
		case actionNone:
			current, _, _, _, problem := inspectGeneratedWithStat(plan.Path, state)
			return problem == nil && current != nil && bridgeImportsRequiredLocal(plan, current)
		case actionCreate, actionUpdate:
			if len(plan.RequiresAnyPrepared) > 0 && !anyPathReadyAfterActions(state, actions, plan.RequiresAnyPrepared) {
				return false
			}
			return action.plan.Data != nil && bridgeImportsRequiredLocal(plan, action.plan.Data)
		default:
			return false
		}
	}
	current, _, _, _, problem := inspectGeneratedWithStat(plan.Path, state)
	return problem == nil && current != nil && bridgeImportsRequiredLocal(plan, current)
}

func anyPathExists(paths []string) bool {
	for _, path := range paths {
		if exists(path) {
			return true
		}
	}
	return false
}

func anyPathReadyAfterActions(state RepositoryState, actions []plannedAction, paths []string) bool {
	for _, path := range paths {
		if exists(path) {
			return true
		}
		for _, action := range actions {
			if action.Path != path {
				continue
			}
			switch action.Action {
			case actionNone:
				current, _, _, _, problem := inspectGeneratedWithStat(path, state)
				if problem == nil && current != nil {
					return true
				}
			case actionCreate, actionUpdate:
				if action.plan.Data != nil {
					return true
				}
			}
			break
		}
	}
	return false
}

func localRuleReadyAfterActions(w string, state RepositoryState, actions []plannedAction) bool {
	local := filepath.Join(w, localRule)
	if exists(local) {
		return true
	}
	for _, action := range actions {
		if action.Path != local {
			continue
		}
		switch action.Action {
		case actionNone:
			current, _, _, _, problem := inspectGeneratedWithStat(local, state)
			return problem == nil && current != nil
		case actionCreate, actionUpdate:
			return action.plan.Data != nil
		default:
			return false
		}
	}
	return false
}

func (r repoContext) statusActions(w string, state RepositoryState, actions []plannedAction) []plannedAction {
	excludePatterns := claudeEffectiveExclusionPatterns(r)
	out := make([]plannedAction, len(actions))
	copy(out, actions)
	for i := range out {
		if out[i].Action == actionRemove && out[i].removeRequiresClaudeBridge && !r.claudeBridgeReadyAfterActions(w, state, out, excludePatterns, out[i].Path) {
			out[i].Action = actionSkip
			out[i].Reason = "replacement Claude bridge was not prepared; preserved"
			continue
		}
		if (out[i].Action == actionCreate || out[i].Action == actionUpdate) && out[i].plan.Bridge && !r.claudeBridgeReadyAfterActions(w, state, out, excludePatterns, "") {
			out[i].Action = actionSkip
			out[i].Reason = "required Claude instruction file was not prepared; preserved"
		}
	}
	for _, action := range out {
		if !action.plan.Bridge || len(action.plan.RequiresAnyPrepared) == 0 || r.claudeBridgeReadyAfterActions(w, state, out, excludePatterns, "") {
			continue
		}
		for i := range out {
			if (out[i].Action == actionCreate || out[i].Action == actionUpdate) && contains(action.plan.RequiresAnyPrepared, out[i].Path) {
				out[i].Action = actionSkip
				out[i].Reason = "replacement Claude bridge was not prepared; preserved"
			}
		}
	}
	return out
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
		action := r.evaluateGenerated(w, plan, state)
		if local != nil && plan.Data == nil && action.Action == actionRemove && isClaudeBridgeRel(plan.Rel) {
			action.removeRequiresClaudeBridge = true
		}
		actions = append(actions, action)
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

func (r repoContext) applyAction(w string, a plannedAction, state *RepositoryState, res *PrepareResult, claudeExcludePatterns []string) {
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
		if a.removeRequiresClaudeBridge && !r.claudePreparedBridgeReady(w, *state, claudeExcludePatterns, a.Path) {
			skip("replacement Claude bridge was not prepared; preserved")
			return
		}
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
		if a.plan.Bridge && !r.claudeBridgeSettingsAllow(a.plan, claudeExcludePatterns) {
			skip("Claude settings prevent loading this bridge; preserved")
			return
		}
		if a.plan.Bridge && !exists(filepath.Join(w, localRule)) {
			skip("required AGENTS.local.md was not prepared; preserved")
			return
		}
		if len(a.plan.RequiresAnyPrepared) > 0 && !anyPathExists(a.plan.RequiresAnyPrepared) {
			skip("required Claude instruction file was not prepared; preserved")
			return
		}
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
	actions = r.statusActions(w, *state, actions)
	res.LocalPresent, res.LocalBody = local != nil, text
	if shared, err := readSharedSource(w); err == nil && shared != nil {
		res.SharedBody, _ = decodeRule(shared, filepath.Join(w, sharedRule))
	}
	res.ClaudeSharedBefore, res.ClaudeLocalBefore = r.claudeNativeLoadPaths(w, *state)
	claudeExcludePatterns := claudeEffectiveExclusionPatterns(r)
	claudeCopyBefore := map[string]bool{}
	if bridge, ok := claudePlannedBridge(actions); ok {
		claudeCopyBefore[bridge.Path] = exists(bridge.Path)
		for _, path := range bridge.RequiresAnyPrepared {
			claudeCopyBefore[path] = exists(path)
		}
	}
	for _, a := range actions {
		r.applyAction(w, a, state, res, claudeExcludePatterns)
	}
	r.rollbackUnbridgedClaudeCopies(w, state, res, actions, claudeCopyBefore, claudeExcludePatterns)
	res.ClaudeSharedAfter, res.ClaudeLocalAfter = r.claudeNativeLoadPaths(w, *state)
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
