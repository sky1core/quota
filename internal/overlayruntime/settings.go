package overlayruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/sky1core/quota/internal/agenthooks"
)

type plannedSettingsFile struct {
	Data   []byte
	Remove bool
}

type settingsLayer struct {
	path string
	data map[string]any
}

type sourcedSetting struct {
	value any
	path  string
}

type ClaudeSessionSettings struct {
	Directory string
	Worktree  string
	Paths     []string
}

func claudeSessionSettings(r repoContext) []ClaudeSessionSettings {
	var result []ClaudeSessionSettings
	user := filepath.Join(nativeConfigHome("CLAUDE_CONFIG_DIR", ".claude"), "settings.json")
	for _, start := range instructionUniquePaths(append([]string{r.Start}, r.Checkouts()...)...) {
		worktree := ""
		for _, candidate := range r.Checkouts() {
			if instructionPathWithin(start, candidate) {
				worktree = candidate
				break
			}
		}
		if worktree == "" {
			continue
		}
		root := r.Root
		if r.Bare() {
			root = worktree
		}
		paths := []string{user, filepath.Join(start, ".claude", "settings.json")}
		if start != root {
			paths = append(paths, filepath.Join(start, ".claude", "settings.local.json"))
		}
		paths = append(paths, filepath.Join(root, ".claude", "settings.local.json"))
		result = append(result, ClaudeSessionSettings{Directory: start, Worktree: worktree, Paths: paths})
	}
	return result
}

func ClaudeSettingsLayers(ctx context.Context, dir string) ([]ClaudeSessionSettings, error) {
	if err := ValidateGitEnvironment(ctx); err != nil {
		return nil, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return nil, err
	}
	return claudeSessionSettings(r), nil
}

func PlannedClaudeSettings(ctx context.Context, dir, agent, sharedSource string, localFiles ...string) (map[string]map[string]any, error) {
	if !validRuntime(agent) {
		return nil, fmt.Errorf("invalid agent %q", agent)
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return nil, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return nil, err
	}
	state, err := readState(r)
	if err != nil {
		return nil, err
	}
	if sharedSource != "" {
		if sharedSource != "primary" && sharedSource != "checkout" {
			return nil, fmt.Errorf("invalid shared source policy")
		}
		state.SharedSource = sharedSource
	}
	if err := registerLocalFiles(&state, localFiles); err != nil {
		return nil, err
	}
	plans, err := r.planManagedFiles(agent, state)
	if err != nil {
		return nil, err
	}
	planned := make(map[string]plannedSettingsFile, len(plans))
	for _, plan := range plans {
		planned[plan.Path] = plannedSettingsFile{Data: plan.Data, Remove: plan.Remove}
	}
	expected := map[string]sharedRuleExpectation{}
	for _, worktree := range r.Checkouts() {
		expectation, err := r.sharedExpectation(worktree, state)
		if err != nil {
			return nil, err
		}
		expected[worktree] = expectation
	}
	var local *string
	if exists(r.localSource()) {
		text, err := readRule(r.localSource())
		if err != nil {
			return nil, err
		}
		local = &text
	}
	if err := r.completePlannedSettings(agent, state, expected, local, planned); err != nil {
		return nil, err
	}
	settings := map[string]map[string]any{}
	var problems []string
	for _, session := range claudeSessionSettings(r) {
		for _, path := range session.Paths {
			if _, read := settings[path]; !read {
				settings[path] = readJSONSettingsLayer(path, planned, &problems).data
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return settings, nil
}

func (r repoContext) completePlannedSettings(agent string, state RepositoryState, expected map[string]sharedRuleExpectation, local *string, planned map[string]plannedSettingsFile) error {
	bridge := func(path, marker string) error {
		current, err := inspectBridgeWithOwnership(path, marker, state)
		if err != nil {
			return err
		}
		if current.Exists && !current.Normalizable {
			return fmt.Errorf("%s contains content beyond the %s import", path, marker)
		}
		body := []byte(marker + "\n")
		if current.Exact {
			body = current.Data
		}
		planned[path] = plannedSettingsFile{Data: body}
		return nil
	}
	for _, worktree := range r.Checkouts() {
		expectation := expected[worktree]
		if state.SharedSource == "primary" && worktree != r.Root && expectation.Present {
			if _, err := r.inspectSharedCopy(expectation, state); err != nil {
				return err
			}
			planned[expectation.Path] = plannedSettingsFile{Data: expectation.Data}
		}
		if agent == "claude" || agent == "all" {
			if expectation.Present {
				if err := bridge(filepath.Join(worktree, sharedBridge), "@AGENTS.md"); err != nil {
					return err
				}
			}
			if worktree == r.Root {
				if local != nil {
					if err := bridge(filepath.Join(worktree, localBridge), "@AGENTS.local.md"); err != nil {
						return err
					}
				}
			} else if local != nil {
				path := filepath.Join(worktree, localBridge)
				data := []byte(generatedLocal(*local))
				if len(data) > maxRuleBytes {
					return fmt.Errorf("%s exceeds %d-byte file size limit", path, maxRuleBytes)
				}
				planned[path] = plannedSettingsFile{Data: data}
			} else if exists(filepath.Join(worktree, localBridge)) {
				planned[filepath.Join(worktree, localBridge)] = plannedSettingsFile{Remove: true}
			}
		}
	}
	addIgnore := func(path, pattern string) error {
		file, loaded := planned[path]
		if !loaded {
			data, err := readRegular(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			file.Data = data
		}
		if !utf8.Valid(file.Data) {
			return fmt.Errorf("%s is not UTF-8", path)
		}
		file.Data = withIgnorePattern(file.Data, pattern)
		planned[path] = file
		return nil
	}
	for _, rel := range append(ignoreTargets(agent), state.LocalFiles...) {
		if !r.Bare() && r.Top == r.Root && !contains(state.LocalFiles, rel) {
			ignored, err := r.ignored(r.Root, rel)
			if err != nil {
				return err
			}
			if !ignored {
				if err := addIgnore(filepath.Join(r.Root, ".gitignore"), rel); err != nil {
					return err
				}
			}
		}
		pattern := rel
		if contains(state.LocalFiles, rel) {
			pattern = "/" + strings.ReplaceAll(rel, " ", "\\ ")
		}
		if err := addIgnore(filepath.Join(r.Common, "info", "exclude"), pattern); err != nil {
			return err
		}
	}
	return nil
}

func nativeConfigHome(env, directory string) string {
	if value := os.Getenv(env); value != "" {
		if strings.HasPrefix(value, "~/") {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, value[2:])
		}
		return value
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, directory)
}

func settingsFileExists(path string, plannedFiles map[string]plannedSettingsFile) bool {
	if file, ok := plannedFiles[path]; ok {
		return !file.Remove
	}
	if plannedFiles != nil {
		target, err := plannedSettingsTarget(path, plannedFiles)
		if err != nil {
			if os.IsNotExist(err) {
				return exists(path)
			}
			return true
		}
		if file, ok := plannedFiles[target]; ok {
			return !file.Remove
		}
		return plannedSettingsDirectory(target, plannedFiles) || exists(target)
	}
	return exists(path)
}

func plannedSettingsDirectory(path string, plannedFiles map[string]plannedSettingsFile) bool {
	for plannedPath, plan := range plannedFiles {
		if !plan.Remove && strings.HasPrefix(plannedPath, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type settingsPathPart struct {
	name    string
	linkEnd bool
}

func settingsPathParts(path string) []settingsPathPart {
	var parts []settingsPathPart
	for _, name := range strings.Split(path, string(filepath.Separator)) {
		parts = append(parts, settingsPathPart{name: name})
	}
	return parts
}

func settingsPathHasSuffix(parts []settingsPathPart) bool {
	for _, part := range parts {
		if !part.linkEnd {
			return true
		}
	}
	return false
}

func plannedSettingsTarget(path string, plannedFiles map[string]plannedSettingsFile) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	remaining := settingsPathParts(path)
	resolved := string(filepath.Separator)
	links, unresolvedLinks := 0, 0
	for len(remaining) > 0 {
		item := remaining[0]
		remaining = remaining[1:]
		if item.linkEnd {
			unresolvedLinks--
			continue
		}
		part := item.name
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		next, err := plannedSettingsEntry(filepath.Join(resolved, part), plannedFiles)
		if err != nil {
			return "", err
		}
		if plan, ok := plannedFiles[next]; ok && plan.Remove && unresolvedLinks > 0 {
			return "", fmt.Errorf("cannot resolve planned settings symlink %s: target %s will be removed", path, next)
		}
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			future, filePlanned := plannedFiles[next]
			directoryPlanned := plannedSettingsDirectory(next, plannedFiles)
			if !(filePlanned && !future.Remove) && !directoryPlanned {
				if unresolvedLinks > 0 {
					return "", fmt.Errorf("cannot resolve planned settings symlink %s at %s: %v", path, next, err)
				}
				return "", err
			}
			if settingsPathHasSuffix(remaining) && !directoryPlanned {
				return "", fmt.Errorf("%s is not a directory", next)
			}
			resolved = next
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if settingsPathHasSuffix(remaining) && !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", next)
			}
			resolved = next
			continue
		}
		links++
		unresolvedLinks++
		if links > 255 {
			return "", fmt.Errorf("too many symbolic links in settings path: %s", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			resolved = string(filepath.Separator)
		}
		targetParts := append(settingsPathParts(target), settingsPathPart{linkEnd: true})
		remaining = append(targetParts, remaining...)
	}
	return resolved, nil
}

func plannedSettingsEntry(path string, plannedFiles map[string]plannedSettingsFile) (string, error) {
	for plannedPath := range plannedFiles {
		for candidate := plannedPath; candidate != filepath.Dir(candidate); candidate = filepath.Dir(candidate) {
			if candidate == path {
				return path, nil
			}
			sameEntry, known, err := sameExistingSettingsEntry(path, candidate)
			if err != nil {
				return "", err
			}
			if known {
				if sameEntry {
					return candidate, nil
				}
				continue
			}
			if !strings.EqualFold(candidate, path) {
				continue
			}
			equivalent, err := settingsPathsCaseEquivalent(path, candidate)
			if err != nil {
				return "", err
			}
			if equivalent {
				return candidate, nil
			}
			break
		}
	}
	return path, nil
}

func sameExistingSettingsEntry(path, candidate string) (bool, bool, error) {
	left, leftErr := os.Lstat(path)
	right, rightErr := os.Lstat(candidate)
	if leftErr != nil || rightErr != nil {
		return false, false, nil
	}
	if !os.SameFile(left, right) {
		return false, true, nil
	}
	leftParent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return false, false, err
	}
	rightParent, err := os.Stat(filepath.Dir(candidate))
	if err != nil {
		return false, false, err
	}
	if !os.SameFile(leftParent, rightParent) {
		return false, true, nil
	}
	if filepath.Base(path) == filepath.Base(candidate) {
		return true, true, nil
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false, false, err
	}
	leftExact, rightExact, matchingEntries := false, false, 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return false, false, err
		}
		if !os.SameFile(left, info) {
			continue
		}
		matchingEntries++
		leftExact = leftExact || entry.Name() == filepath.Base(path)
		rightExact = rightExact || entry.Name() == filepath.Base(candidate)
	}
	if leftExact && rightExact {
		return false, true, nil
	}
	if matchingEntries == 1 {
		return true, true, nil
	}
	return false, false, fmt.Errorf("cannot distinguish settings aliases from separate hardlinks: %s and %s", path, candidate)
}

func settingsPathsShareEntry(path, candidate string) (bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	other, err := filepath.EvalSymlinks(candidate)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	same, _, err := sameExistingSettingsEntry(resolved, other)
	return same, err
}

func settingsPathsCaseEquivalent(path, candidate string) (bool, error) {
	for path != candidate {
		if filepath.Base(path) != filepath.Base(candidate) {
			for _, name := range []string{filepath.Base(path), filepath.Base(candidate)} {
				for _, char := range name {
					if char > 127 {
						return false, fmt.Errorf("cannot establish a future non-ASCII settings alias: %s", path)
					}
				}
			}
			insensitive, err := settingsDirectoryCaseInsensitive(filepath.Dir(path))
			if err != nil || !insensitive {
				return false, err
			}
		}
		path, candidate = filepath.Dir(path), filepath.Dir(candidate)
	}
	return true, nil
}

func readSettingsFile(path string, plannedFiles map[string]plannedSettingsFile) ([]byte, error) {
	var data []byte
	if _, ok := plannedFiles[path]; !ok && plannedFiles != nil {
		target, err := plannedSettingsTarget(path, plannedFiles)
		if err != nil {
			return nil, err
		}
		path = target
	}
	if file, ok := plannedFiles[path]; ok {
		if file.Remove {
			return nil, &os.PathError{Op: "read", Path: path, Err: os.ErrNotExist}
		}
		data = file.Data
	} else {
		if plannedSettingsDirectory(path, plannedFiles) {
			return nil, fmt.Errorf("%s is planned as a directory, not a settings file", path)
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		data, err = readRegular(target)
		if err != nil {
			return nil, err
		}
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("non-UTF-8 settings: %s", path)
	}
	return data, nil
}

func readJSONSettingsLayer(path string, plannedFiles map[string]plannedSettingsFile, problems *[]string) settingsLayer {
	layer := settingsLayer{path: path}
	data, err := readSettingsFile(path, plannedFiles)
	if os.IsNotExist(err) {
		return layer
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		err = decoder.Decode(&layer.data)
		if err != nil {
			err = fmt.Errorf("must contain a JSON object: %w", err)
		}
		if err == nil && layer.data == nil {
			err = fmt.Errorf("must contain a JSON object")
		}
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = fmt.Errorf("trailing content after JSON object")
		}
	}
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("could not read %s: %v", path, err))
		layer.data = nil
	}
	return layer
}

func ReadClaudeSettings(path string) (map[string]any, error) {
	var problems []string
	layer := readJSONSettingsLayer(path, nil, &problems)
	if len(problems) != 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	if layer.data == nil {
		return map[string]any{}, nil
	}
	return layer.data, nil
}

func instructionPathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func instructionUniquePaths(paths ...string) []string {
	seen := map[string]bool{}
	var result []string
	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func claudeExclusionPattern(pattern string) string {
	var out strings.Builder
	out.WriteByte('^')
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			j := i + 1
			for j < len(pattern) && pattern[j] == '*' {
				j++
			}
			if j-i >= 2 && (i == 0 || pattern[i-1] == '/') && (j == len(pattern) || pattern[j] == '/') {
				if j < len(pattern) {
					out.WriteString("(?:.*/)?")
					j++
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
			i = j
		case '?':
			out.WriteString("[^/]")
			i++
		default:
			_, size := utf8.DecodeRuneInString(pattern[i:])
			out.WriteString(regexp.QuoteMeta(pattern[i : i+size]))
			i += size
		}
	}
	out.WriteByte('$')
	return out.String()
}

func claudeSettingsFindings(r repoContext, planned ...map[string]sharedRuleExpectation) ([]string, []string) {
	return claudeSettingsFindingsWithPlannedFiles(r, nil, planned...)
}

func claudeSettingsFindingsWithPlannedFiles(r repoContext, plannedFiles map[string]plannedSettingsFile, planned ...map[string]sharedRuleExpectation) ([]string, []string) {
	var problems []string
	executable, err := os.Executable()
	if err != nil {
		return []string{err.Error()}, nil
	}
	user := readJSONSettingsLayer(filepath.Join(nativeConfigHome("CLAUDE_CONFIG_DIR", ".claude"), "settings.json"), plannedFiles, &problems)
	for _, session := range claudeSessionSettings(r) {
		worktree := session.Worktree
		layers := []settingsLayer{user}
		for _, path := range session.Paths[1:] {
			layers = append(layers, readJSONSettingsLayer(path, plannedFiles, &problems))
		}
		var disabled sourcedSetting
		var patterns []string
		for index, layer := range layers {
			hooks, err := agenthooks.ClaudeInstructionHooks(layer.data, executable)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s (session %s): %v", layer.path, session.Directory, err))
			} else if index != 0 {
				for _, hook := range hooks {
					problems = append(problems, fmt.Sprintf("%s (session %s): hooks.%s conflicts with the fixed account installation", layer.path, session.Directory, hook.Event))
				}
			}
			if value, ok := layer.data["disableAllHooks"]; ok {
				if _, valid := value.(bool); !valid {
					problems = append(problems, layer.path+": disableAllHooks must be a boolean")
				}
				disabled = sourcedSetting{value, layer.path}
			}
			if value, ok := layer.data["claudeMdExcludes"]; ok {
				entries, valid := value.([]any)
				if !valid {
					problems = append(problems, layer.path+": claudeMdExcludes must be an array of strings")
					continue
				}
				for _, entry := range entries {
					pattern, valid := entry.(string)
					if !valid || pattern == "" {
						problems = append(problems, layer.path+": claudeMdExcludes must contain non-empty strings")
						continue
					}
					patterns = append(patterns, strings.ReplaceAll(pattern, "\\", "/"))
				}
			}
		}
		if disabled.value == true {
			problems = append(problems, disabled.path+": disableAllHooks is true; Claude instruction hooks are disabled")
		}
		for _, name := range []string{"CLAUDE.md", "CLAUDE.local.md"} {
			bridge := filepath.Join(worktree, name)
			sharedRequired := exists(filepath.Join(worktree, sharedRule))
			if len(planned) > 0 {
				sharedRequired = planned[0][worktree].Present
			}
			required := name == sharedBridge && sharedRequired || name == localBridge && exists(r.localSource())
			if !settingsFileExists(bridge, plannedFiles) && !required {
				continue
			}
			candidates := []string{filepath.ToSlash(bridge), filepath.ToSlash(resolvePath(bridge))}
			for _, pattern := range patterns {
				matched := pattern == candidates[0] || pattern == candidates[1]
				if !matched && strings.ContainsAny(pattern, "{}[]()!") {
					problems = append(problems, fmt.Sprintf("claudeMdExcludes pattern %q cannot evaluate whether %s still loads", pattern, bridge))
					continue
				}
				expression, err := regexp.Compile(claudeExclusionPattern(pattern))
				if err != nil {
					problems = append(problems, fmt.Sprintf("claudeMdExcludes pattern %q cannot evaluate: %v", pattern, err))
					continue
				}
				if matched || expression.MatchString(candidates[0]) || expression.MatchString(candidates[1]) {
					problems = append(problems, fmt.Sprintf("claudeMdExcludes matches %s; Claude will not load that bridge", bridge))
				}
			}
		}
	}
	return instructionUniquePaths(problems...), nil
}

func readCodexLayer(path string, plannedFiles map[string]plannedSettingsFile, problems *[]string) map[string]any {
	data, err := readSettingsFile(path, plannedFiles)
	var parsed map[string]any
	if err == nil {
		_, err = toml.Decode(string(data), &parsed)
	}
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("could not parse %s: %v", path, err))
	}
	return parsed
}

func codexHookFeatures(data map[string]any, path string, problems *[]string) map[string]sourcedSetting {
	settings := map[string]sourcedSetting{}
	value, ok := data["features"]
	if !ok {
		return settings
	}
	features, ok := value.(map[string]any)
	if !ok {
		*problems = append(*problems, path+": features must be a table")
		return settings
	}
	for _, key := range []string{"hooks", "codex_hooks"} {
		if value, ok := features[key]; ok {
			if _, valid := value.(bool); !valid {
				*problems = append(*problems, path+": features."+key+" must be a boolean")
			} else {
				settings[key] = sourcedSetting{value, path}
			}
		}
	}
	return settings
}

func codexProjectTrust(data map[string]any, worktree string) string {
	projects, _ := data["projects"].(map[string]any)
	for _, path := range []string{worktree, resolvePath(worktree)} {
		if entry, ok := projects[path].(map[string]any); ok {
			level, _ := entry["trust_level"].(string)
			return level
		}
	}
	return ""
}

func codexSettingsFindings(r repoContext, planned ...map[string]sharedRuleExpectation) ([]string, []string) {
	return codexSettingsFindingsWithPlannedFiles(r, nil, planned...)
}

func codexSettingsFindingsWithPlannedFiles(r repoContext, plannedFiles map[string]plannedSettingsFile, planned ...map[string]sharedRuleExpectation) ([]string, []string) {
	var problems, warnings []string
	if r.NativeCodexSettings {
		var paths []string
		for _, files := range r.NativeCodexHookFiles {
			paths = append(paths, files...)
		}
		return codexHookFileFindings(r, instructionUniquePaths(paths...), plannedFiles), nil
	}
	configPath := filepath.Join(nativeConfigHome("CODEX_HOME", ".codex"), "config.toml")
	data := map[string]any{}
	if settingsFileExists(configPath, plannedFiles) {
		data = readCodexLayer(configPath, plannedFiles, &problems)
		if data == nil {
			return problems, warnings
		}
	}
	for _, worktree := range r.Checkouts() {
		trust := codexProjectTrust(data, worktree)
		if trust == "untrusted" {
			problems = append(problems, worktree+" is untrusted in "+configPath+"; Codex does not load AGENTS.md")
			continue
		}
		if trust != "trusted" {
			warnings = append(warnings, worktree+" has no trusted entry in "+configPath+"; only user config was evaluated")
		}
		starts := []string{worktree}
		if r.Start != worktree && instructionPathWithin(r.Start, worktree) {
			starts = append(starts, r.Start)
		}
		for _, start := range starts {
			doc := map[string]sourcedSetting{}
			merge := func(layer map[string]any, path string) {
				codexHookFeatures(layer, path, &problems)
				if _, ok := layer["project_root_markers"]; ok {
					problems = append(problems, path+": project_root_markers changes Codex project-doc discovery")
				}
				for _, key := range []string{"project_doc_max_bytes", "project_doc_fallback_filenames"} {
					if value, ok := layer[key]; ok {
						doc[key] = sourcedSetting{value, path}
					}
				}
			}
			merge(data, configPath)
			var projectConfigs []string
			if trust == "trusted" {
				for path := start; instructionPathWithin(path, worktree); path = filepath.Dir(path) {
					projectConfigs = append(projectConfigs, filepath.Join(path, ".codex", "config.toml"))
					if path == worktree {
						break
					}
				}
			}
			for i := len(projectConfigs) - 1; i >= 0; i-- {
				path := projectConfigs[i]
				if settingsFileExists(path, plannedFiles) {
					layer := readCodexLayer(path, plannedFiles, &problems)
					merge(layer, path)
					problems = append(problems, codexInstructionHookFindings(r, layer, path)...)
				}
				path = filepath.Join(filepath.Dir(path), "hooks.json")
				if settingsFileExists(path, plannedFiles) {
					problems = append(problems, codexInstructionHookFindings(r, readJSONSettingsLayer(path, plannedFiles, &problems).data, path)...)
				}
			}
			if setting, ok := doc["project_doc_fallback_filenames"]; ok {
				entries, valid := setting.value.([]any)
				if !valid || len(entries) != 0 {
					problems = append(problems, setting.path+": project_doc_fallback_filenames must be [] or absent")
				}
			}
			maxBytes := int64(32768)
			maxPath := configPath
			if setting, ok := doc["project_doc_max_bytes"]; ok {
				value, valid := setting.value.(int64)
				if !valid || value <= 0 {
					problems = append(problems, setting.path+": project_doc_max_bytes must be a positive integer")
					continue
				}
				maxBytes, maxPath = value, setting.path
			}
			shared := filepath.Join(worktree, sharedRule)
			var body []byte
			if len(planned) > 0 {
				body = planned[0][worktree].Data
			} else {
				body, _ = readRegular(shared)
			}
			if len(planned) > 0 && exists(r.localSource()) {
				local, err := readRegular(r.localSource())
				if err != nil {
					problems = append(problems, err.Error())
					continue
				}
				body = mergedCodexInstructions(body, local)
				shared = filepath.Join(worktree, codexRule)
			} else if len(planned) == 0 && exists(filepath.Join(worktree, codexRule)) {
				shared = filepath.Join(worktree, codexRule)
				var err error
				body, err = readRegular(shared)
				if err != nil {
					problems = append(problems, err.Error())
					continue
				}
			}
			if int64(len(body)) > maxBytes {
				problems = append(problems, fmt.Sprintf("%s is %d bytes, over Codex project_doc_max_bytes %d from %s", shared, len(body), maxBytes, maxPath))
			}
		}
	}
	return instructionUniquePaths(problems...), instructionUniquePaths(warnings...)
}

func codexInstructionHookFindings(r repoContext, data map[string]any, path string) []string {
	var problems []string
	executable, err := os.Executable()
	if err != nil {
		return []string{err.Error()}
	}
	hooks, err := agenthooks.CodexInstructionHooks(data, executable)
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", path, err))
		return problems
	}
	for _, hook := range hooks {
		removing := false
		if hook.Owned {
			for _, accountPath := range r.CodexHookRemovals {
				same, err := settingsPathsShareEntry(path, accountPath)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: %v", path, err))
					return problems
				}
				if same {
					removing = true
					break
				}
			}
		}
		if !removing {
			problems = append(problems, fmt.Sprintf("%s: hooks.%s conflicts with native instruction delivery", path, hook.Event))
		}
	}
	return problems
}

func codexHookFileFindings(r repoContext, paths []string, planned map[string]plannedSettingsFile) []string {
	var problems []string
	for _, path := range paths {
		if !settingsFileExists(path, planned) {
			continue
		}
		var data map[string]any
		switch filepath.Ext(path) {
		case ".toml":
			data = readCodexLayer(path, planned, &problems)
		case ".json":
			data = readJSONSettingsLayer(path, planned, &problems).data
		default:
			problems = append(problems, fmt.Sprintf("cannot reliably check native hook source %s", path))
			continue
		}
		problems = append(problems, codexInstructionHookFindings(r, data, path)...)
	}
	return instructionUniquePaths(problems...)
}
