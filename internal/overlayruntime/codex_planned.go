package overlayruntime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type PlannedCodexDocument struct {
	TrustPaths  []string
	Directory   string
	Path        string
	Bytes       int64
	ConfigPaths []string
}

type CodexSetupPlan struct {
	Documents           []PlannedCodexDocument
	files               map[string]plannedSettingsFile
	repo                repoContext
	state               RepositoryState
	agent, sharedSource string
	localFiles          []string
	managed             []managedInstructionFile
	shared              map[string]sharedRuleExpectation
	local               *string
	inputs              map[string]codexPlanInput
	accountPaths        []string
	nativeHookPaths     []string
	worktreeOnly        bool
}

func (p CodexSetupPlan) ProjectHookFileCandidates(configPaths []string) ([]string, error) {
	var paths []string
	for _, config := range configPaths {
		directory := filepath.Dir(filepath.Dir(config))
		folder := filepath.Dir(config)
		if !p.repo.Bare() {
			checkoutRoot := ""
			for _, checkout := range p.repo.Checkouts() {
				if instructionPathWithin(directory, checkout) && len(checkout) > len(checkoutRoot) {
					checkoutRoot = checkout
				}
			}
			if checkoutRoot != "" {
				rel, err := filepath.Rel(checkoutRoot, directory)
				if err != nil {
					return nil, err
				}
				folder = filepath.Join(p.repo.Root, rel, ".codex")
			}
		}
		paths = append(paths, filepath.Join(folder, "config.toml"), filepath.Join(folder, "hooks.json"))
	}
	return instructionUniquePaths(paths...), nil
}

func (p CodexSetupPlan) CheckNewCodexHookLayers(configPaths []string) error {
	for _, config := range configPaths {
		folder := filepath.Dir(config)
		if filepath.Base(folder) != ".codex" || !plannedSettingsDirectory(folder, p.files) {
			continue
		}
		if _, err := os.Stat(folder); os.IsNotExist(err) {
			return fmt.Errorf("cannot reliably check planned Codex hook origin: %s creates a project layer absent from native discovery", folder)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (p *CodexSetupPlan) TrackNativeHookFiles(paths []string) error {
	p.nativeHookPaths = instructionUniquePaths(append(p.nativeHookPaths, paths...)...)
	current, err := p.captureInputs()
	if err != nil {
		return err
	}
	for path, input := range current {
		if _, exists := p.inputs[path]; !exists {
			p.inputs[path] = input
		}
	}
	return nil
}

func (p CodexSetupPlan) RemovesOwnedCodexHook(path string) (bool, error) {
	for _, removal := range p.repo.CodexHookRemovals {
		same, err := settingsPathsShareEntry(path, removal)
		if err != nil || same {
			return same, err
		}
	}
	return false, nil
}

func PlanCodexDocuments(ctx context.Context, dir, agent, sharedSource string, localFiles ...string) (CodexSetupPlan, error) {
	return planCodexDocuments(ctx, dir, agent, sharedSource, false, localFiles...)
}

func planCodexDocuments(ctx context.Context, dir, agent, sharedSource string, worktreeOnly bool, localFiles ...string) (CodexSetupPlan, error) {
	plan := CodexSetupPlan{files: map[string]plannedSettingsFile{}}
	if agent != "codex" && agent != "all" {
		return plan, fmt.Errorf("invalid Codex setup agent %q", agent)
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return plan, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return plan, err
	}
	if worktreeOnly {
		r.Worktrees = []string{r.Top}
	}
	state, err := readState(r)
	if err != nil {
		return plan, err
	}
	if sharedSource != "" {
		if sharedSource != "primary" && sharedSource != "checkout" {
			return plan, fmt.Errorf("invalid shared source policy")
		}
		state.SharedSource = sharedSource
	}
	if err := registerLocalFiles(&state, localFiles); err != nil {
		return plan, err
	}
	managed, err := r.planManagedFiles(agent, state)
	if err != nil {
		return plan, err
	}
	plan.repo, plan.state, plan.agent, plan.sharedSource = r, state, agent, sharedSource
	plan.worktreeOnly = worktreeOnly
	plan.localFiles = append([]string(nil), localFiles...)
	plan.managed = managed
	for _, file := range managed {
		plan.files[file.Path] = plannedSettingsFile{Data: file.Data, Remove: file.Remove}
	}
	expected := map[string]sharedRuleExpectation{}
	if state.SharedSource == "primary" {
		expected[r.Root], err = r.sharedExpectation(r.Root, state)
		if err != nil {
			return plan, err
		}
	}
	var local *string
	if exists(r.localSource()) {
		body, err := readRule(r.localSource())
		if err != nil {
			return plan, err
		}
		local = &body
	}
	for _, worktree := range r.Checkouts() {
		shared, err := r.sharedExpectation(worktree, state)
		if err != nil {
			return plan, err
		}
		expected[worktree] = shared
		path, size := shared.Path, int64(len(shared.Data))
		if merged, ok := plan.files[filepath.Join(worktree, codexRule)]; ok && !merged.Remove {
			path, size = filepath.Join(worktree, codexRule), int64(len(merged.Data))
		}
		starts := []string{worktree}
		if r.Start != worktree && instructionPathWithin(r.Start, worktree) {
			starts = append(starts, r.Start)
		}
		for _, start := range starts {
			trustPaths := []string{r.Root}
			for directory := start; instructionPathWithin(directory, worktree); directory = filepath.Dir(directory) {
				trustPaths = append(trustPaths, directory)
				if directory == worktree {
					break
				}
			}
			paths := []string{filepath.Join(nativeConfigHome("CODEX_HOME", ".codex"), "config.toml")}
			for directory := start; ; directory = filepath.Dir(directory) {
				paths = append(paths, filepath.Join(directory, ".codex", "config.toml"))
				if directory == filepath.Dir(directory) {
					break
				}
			}
			plan.Documents = append(plan.Documents, PlannedCodexDocument{TrustPaths: instructionUniquePaths(trustPaths...), Directory: start, Path: path, Bytes: size, ConfigPaths: instructionUniquePaths(paths...)})
		}
	}
	if err := r.completePlannedSettings(agent, state, expected, local, plan.files); err != nil {
		return plan, err
	}
	plan.shared, plan.local = expected, local
	plan.inputs, err = plan.captureInputs()
	return plan, err
}

func (p CodexSetupPlan) PlannedConfigFile(path string) (string, []byte, bool, error) {
	target, err := plannedSettingsTarget(path, p.files)
	if os.IsNotExist(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if file, ok := p.files[target]; ok {
		if file.Remove {
			return target, nil, true, fmt.Errorf("effective Codex config %s will be removed", path)
		}
		return target, file.Data, true, nil
	}
	return "", nil, false, nil
}

func ReadCodexConfigFile(path string) ([]byte, error) {
	return readSettingsFile(path, nil)
}

type codexPlanInput struct {
	Target    string
	Data      []byte
	Mode      os.FileMode
	Exists    bool
	ReadError string
}

func (p CodexSetupPlan) captureInputs() (map[string]codexPlanInput, error) {
	paths := []string{statePath(p.repo), p.repo.localSource(), filepath.Join(p.repo.Common, "info", "exclude"), filepath.Join(p.repo.Root, ".gitignore")}
	paths = append(paths, p.accountPaths...)
	paths = append(paths, p.nativeHookPaths...)
	if p.agent == "all" {
		for _, layer := range claudeSessionSettings(p.repo) {
			paths = append(paths, layer.Paths...)
		}
	}
	for path := range p.files {
		paths = append(paths, path)
	}
	for _, rel := range p.state.LocalFiles {
		paths = append(paths, filepath.Join(p.repo.Root, rel))
	}
	for _, shared := range p.shared {
		paths = append(paths, shared.Source, shared.Path)
	}
	required := map[string]bool{}
	for _, path := range paths {
		if path != "" {
			required[resolvePath(path)] = true
		}
	}
	for _, doc := range p.Documents {
		paths = append(paths, doc.ConfigPaths...)
		hookFiles, err := p.ProjectHookFileCandidates(doc.ConfigPaths)
		if err != nil {
			return nil, err
		}
		paths = append(paths, hookFiles...)
	}
	inputs := map[string]codexPlanInput{}
	for _, path := range instructionUniquePaths(paths...) {
		if path == "" {
			continue
		}
		input := codexPlanInput{Target: resolvePath(path)}
		info, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			if required[input.Target] {
				return nil, err
			}
			input.ReadError = err.Error()
		}
		if err == nil {
			input.Exists, input.Mode = true, info.Mode()
			input.Data, err = readRegular(input.Target)
			if err != nil {
				if required[input.Target] {
					return nil, err
				}
				input.ReadError = err.Error()
			}
		}
		inputs[path] = input
	}
	return inputs, nil
}

func PlanNativeCodexRepository(ctx context.Context, dir, agent, sharedSource string, removals, accountPaths []string, localFiles ...string) (CodexSetupPlan, []string, error) {
	plan, err := PlanCodexDocuments(ctx, dir, agent, sharedSource, localFiles...)
	if err != nil {
		return plan, nil, err
	}
	plan.accountPaths = append([]string(nil), accountPaths...)
	plan.inputs, err = plan.captureInputs()
	if err != nil {
		return plan, nil, err
	}
	plan.repo.NativeCodexSettings = true
	plan.repo.CodexHookRemovals = append([]string(nil), removals...)
	if problems := plan.repo.preflight(agent, plan.state); len(problems) > 0 {
		return plan, nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	paths, err := plan.repo.plannedPaths(agent, plan.state)
	return plan, paths, err
}

type ValidatedCodexSetup struct {
	Plan            CodexSetupPlan
	Budgets         map[string]int64
	ActiveHookFiles map[string][]string
}

func (v ValidatedCodexSetup) checkSettings() error {
	r := v.Plan.repo
	r.NativeCodexSettings = true
	r.NativeCodexHookFiles = v.ActiveHookFiles
	for _, doc := range v.Plan.Documents {
		if _, ok := v.ActiveHookFiles[doc.Directory]; !ok {
			return fmt.Errorf("missing native hook origin validation for %s", doc.Directory)
		}
	}
	problems, _ := codexSettingsFindingsWithPlannedFiles(r, v.Plan.files, v.Plan.shared)
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func (v ValidatedCodexSetup) CheckConsistency(ctx context.Context) error {
	p := v.Plan
	current, err := planCodexDocuments(ctx, p.repo.Start, p.agent, p.sharedSource, p.worktreeOnly, p.localFiles...)
	if err != nil {
		return fmt.Errorf("native setup inputs changed after validation: %w", err)
	}
	current.accountPaths = p.accountPaths
	current.nativeHookPaths = p.nativeHookPaths
	current.inputs, err = current.captureInputs()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p.inputs, current.inputs) || !reflect.DeepEqual(p.Documents, current.Documents) || !reflect.DeepEqual(p.files, current.files) || !reflect.DeepEqual(p.managed, current.managed) || !reflect.DeepEqual(p.state, current.state) || !reflect.DeepEqual(p.repo.Worktrees, current.repo.Worktrees) || p.repo.Root != current.repo.Root || p.repo.Common != current.repo.Common {
		return fmt.Errorf("native setup inputs changed after validation; run setup again")
	}
	for _, doc := range p.Documents {
		budget, ok := v.Budgets[doc.Directory]
		if !ok || budget <= 0 || doc.Bytes > budget {
			return fmt.Errorf("missing or invalid native validation for %s", doc.Directory)
		}
	}
	return v.checkSettings()
}

func (v ValidatedCodexSetup) Apply(ctx context.Context, applyAccount func() error) error {
	r := v.Plan.repo
	r.Context = ctx
	r.NativeCodexSettings = true
	r.NativeCodexHookFiles = v.ActiveHookFiles
	r.ValidatedCodexPlan = &v.Plan
	return withRepositoryLock(r, func() error {
		if err := v.CheckConsistency(ctx); err != nil {
			return err
		}
		var output bytes.Buffer
		code, err := setupRepositoryWithState(r, v.Plan.agent, v.Plan.state, &output, &output)
		if err != nil {
			return fmt.Errorf("%s%w", output.String(), err)
		}
		if code != 0 {
			return fmt.Errorf("%s", strings.TrimSpace(output.String()))
		}
		return applyAccount()
	})
}

func CheckRepositoryWithNativeCodexSettings(ctx context.Context, dir string, activeHookFiles map[string][]string, output io.Writer) (int, error) {
	if err := ValidateGitEnvironment(ctx); err != nil {
		return 1, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return 1, err
	}
	r.NativeCodexSettings = true
	r.NativeCodexHookFiles = activeHookFiles
	return checkRepository(r, "codex", output)
}
