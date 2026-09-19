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
	user := filepath.Join(nativeConfigHome("CLAUDE_CONFIG_DIR", ".claude"), "settings.json")
	root := r.Top
	if !r.Bare() {
		root = r.Root
	}
	paths := []string{user, filepath.Join(r.Start, ".claude", "settings.json")}
	if r.Start != root {
		paths = append(paths, filepath.Join(r.Start, ".claude", "settings.local.json"))
	}
	paths = append(paths, filepath.Join(root, ".claude", "settings.local.json"))
	return []ClaudeSessionSettings{{Directory: r.Start, Worktree: r.Top, Paths: paths}}
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

func readSettingsFile(path string) ([]byte, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	data, err := readRegular(target)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("non-UTF-8 settings: %s", path)
	}
	return data, nil
}

func readJSONSettingsLayer(path string, problems *[]string) settingsLayer {
	layer := settingsLayer{path: path}
	data, err := readSettingsFile(path)
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
	layer := readJSONSettingsLayer(path, &problems)
	if len(problems) != 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	if layer.data == nil {
		return map[string]any{}, nil
	}
	return layer.data, nil
}

func claudeInstructionFilesOption(data map[string]any) (string, bool) {
	plugins, ok := data["pluginConfigs"].(map[string]any)
	if !ok {
		return "", false
	}
	plugin, ok := plugins["agents-md@builtin"].(map[string]any)
	if !ok {
		return "", false
	}
	options, ok := plugin["options"].(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := options["instructionFiles"].(string)
	return value, ok
}

func claudeProjectInstructionsOption(data map[string]any) (string, bool) {
	plugins, ok := data["pluginConfigs"].(map[string]any)
	if !ok {
		return "", false
	}
	plugin, ok := plugins["agents-md@builtin"].(map[string]any)
	if !ok {
		return "", false
	}
	options, ok := plugin["options"].(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := options["projectInstructions"].(string)
	return value, ok
}

const (
	claudeModeClaudeOnly    = "claude-md"
	claudeModeAgentsDefault = "claude-md-or-agents-md"
	claudeModeClaudeAnd     = "claude-md-and-agents-md"
	claudeModeManagedOnly   = "managed-only"
)

func claudeInstructionMode(data map[string]any) (string, bool) {
	newModeDefault := false
	if value, ok := claudeInstructionFilesOption(data); ok {
		switch value {
		case claudeModeClaudeOnly, claudeModeClaudeAnd, claudeModeManagedOnly:
			return value, true
		case claudeModeAgentsDefault:
			newModeDefault = true
		default:
			newModeDefault = true
		}
	}
	if value, ok := claudeProjectInstructionsOption(data); ok {
		switch value {
		case "none":
			return claudeModeManagedOnly, true
		case "claude":
			return claudeModeClaudeOnly, true
		case "agents-fallback":
			return claudeModeAgentsDefault, true
		case "both":
			return claudeModeClaudeAnd, true
		default:
			return claudeModeClaudeOnly, true
		}
	}
	if newModeDefault {
		return claudeModeAgentsDefault, true
	}
	return claudeModeAgentsDefault, false
}

func claudeEffectiveInstructionMode(r repoContext) (string, bool) {
	var problems []string
	for _, session := range claudeSessionSettings(r) {
		if len(session.Paths) == 0 {
			continue
		}
		return claudeInstructionMode(readJSONSettingsLayer(session.Paths[0], &problems).data)
	}
	return claudeModeAgentsDefault, false
}

func claudeEffectiveExclusionPatterns(r repoContext) []string {
	var problems []string
	var patterns []string
	for _, session := range claudeSessionSettings(r) {
		for _, path := range session.Paths {
			layer := readJSONSettingsLayer(path, &problems)
			entries, ok := layer.data["claudeMdExcludes"].([]any)
			if !ok {
				continue
			}
			for _, entry := range entries {
				pattern, ok := entry.(string)
				if ok && pattern != "" {
					patterns = append(patterns, strings.ReplaceAll(pattern, "\\", "/"))
				}
			}
		}
	}
	return patterns
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

func claudeExclusionFindings(patterns []string, instructionFile string) []string {
	var problems []string
	candidates := []string{filepath.ToSlash(instructionFile), filepath.ToSlash(resolvePath(instructionFile))}
	for _, pattern := range patterns {
		matched := pattern == candidates[0] || pattern == candidates[1]
		if !matched && strings.ContainsAny(pattern, "{}[]()!") {
			problems = append(problems, fmt.Sprintf("claudeMdExcludes pattern %q cannot evaluate whether %s still loads", pattern, instructionFile))
			continue
		}
		expression, err := regexp.Compile(claudeExclusionPattern(pattern))
		if err != nil {
			problems = append(problems, fmt.Sprintf("claudeMdExcludes pattern %q cannot evaluate: %v", pattern, err))
			continue
		}
		if matched || expression.MatchString(candidates[0]) || expression.MatchString(candidates[1]) {
			problems = append(problems, fmt.Sprintf("claudeMdExcludes matches %s; Claude will not load that instruction file", instructionFile))
		}
	}
	return problems
}

func claudeInstructionFileFindings(r repoContext, mode string) []string {
	if !exists(filepath.Join(r.Top, sharedRule)) && !exists(r.localSource()) {
		return nil
	}
	var problems []string
	files := r.claudeInstructionFiles(r.Top, RepositoryState{}, false, nil)
	var projectClaude []claudeInstructionFile
	for _, file := range files {
		if file.Local {
			problems = append(problems, file.Path+" is a local Claude instruction file; remove it so AGENTS.local.md is the single local instruction source")
			continue
		}
		projectClaude = append(projectClaude, file)
	}
	if len(projectClaude) == 0 {
		return uniqueStrings(problems...)
	}
	shared := filepath.Join(r.Top, sharedRule)
	if exists(shared) && mode != claudeModeClaudeAnd && mode != claudeModeManagedOnly {
		imported, err := claudeFilesImportTarget(projectClaude, shared)
		if err != nil {
			problems = append(problems, err.Error())
		} else if !imported {
			problems = append(problems, claudeFilePaths(projectClaude)+" do not import "+shared+"; Claude's default AGENTS fallback will not load it")
		}
	}
	local := filepath.Join(r.Top, localRule)
	bridge := filepath.Join(r.Top, claudeBridgeRule)
	if exists(r.localSource()) && exists(bridge) {
		imported, err := claudeFileImports(bridge, local)
		if err != nil {
			problems = append(problems, err.Error())
		} else if !imported {
			problems = append(problems, bridge+" does not import "+local+"; Claude will not load "+localRule)
		}
	}
	return uniqueStrings(problems...)
}

// claudeSettingsFindings reports Claude settings that block hook execution or
// exclude native instruction files from loading.
func claudeSettingsFindings(r repoContext) []string {
	var problems []string
	executable, err := os.Executable()
	if err != nil {
		return []string{err.Error()}
	}
	effectiveMode, _ := claudeEffectiveInstructionMode(r)
	problems = append(problems, claudeInstructionFileFindings(r, effectiveMode)...)
	for _, session := range claudeSessionSettings(r) {
		var layers []settingsLayer
		for _, path := range session.Paths {
			layers = append(layers, readJSONSettingsLayer(path, &problems))
		}
		var disabled sourcedSetting
		instructionMode := sourcedSetting{value: effectiveMode}
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
			if index == 0 {
				if value, ok := claudeInstructionMode(layer.data); ok {
					instructionMode = sourcedSetting{value, layer.path}
				}
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
		projectClaude := r.claudeInstructionFiles(session.Worktree, RepositoryState{}, false, nil)
		var loadedProjectClaude []claudeInstructionFile
		var excludedProjectClaude []string
		for _, file := range projectClaude {
			if file.Local {
				continue
			}
			exclusions := claudeExclusionFindings(patterns, file.Path)
			if len(exclusions) == 0 {
				loadedProjectClaude = append(loadedProjectClaude, file)
			} else {
				excludedProjectClaude = append(excludedProjectClaude, file.Path)
			}
		}
		shared := filepath.Join(session.Worktree, sharedRule)
		localSourcePresent := exists(r.localSource())
		if instructionMode.value == claudeModeManagedOnly && (exists(shared) || localSourcePresent || len(projectClaude) != 0) {
			problems = append(problems, instructionMode.path+": Claude instruction mode managed-only drops project and local instruction files")
		}
		if instructionMode.value == claudeModeClaudeOnly && len(projectClaude) == 0 && (exists(shared) || localSourcePresent) {
			problems = append(problems, instructionMode.path+": Claude instruction mode claude-md disables AGENTS.md native loading and no Claude project instruction file imports "+sharedRule+" or "+localRule)
		}
		if exists(shared) {
			problems = append(problems, claudeExclusionFindings(patterns, shared)...)
			if len(projectClaude) != 0 && instructionMode.value != claudeModeClaudeAnd && instructionMode.value != claudeModeManagedOnly {
				imported := false
				imported, err = claudeFilesImportTarget(loadedProjectClaude, shared)
				if err != nil {
					problems = append(problems, err.Error())
				} else if !imported {
					detail := claudeFilePaths(loadedProjectClaude) + " do not import " + shared
					if len(excludedProjectClaude) != 0 {
						detail = strings.Join(excludedProjectClaude, ", ") + " excluded by claudeMdExcludes; " + detail
					}
					problems = append(problems, detail+"; Claude's default AGENTS fallback will not load it")
				}
			}
		}
		local := filepath.Join(session.Worktree, claudeAgentsRule)
		if len(projectClaude) > 0 {
			local = filepath.Join(session.Worktree, claudeBridgeRule)
		}
		if exists(local) || exists(r.localSource()) {
			problems = append(problems, claudeExclusionFindings(patterns, local)...)
		}
	}
	return uniqueStrings(problems...)
}

func readCodexLayer(path string, problems *[]string) map[string]any {
	data, err := readSettingsFile(path)
	var parsed map[string]any
	if err == nil {
		_, err = toml.Decode(string(data), &parsed)
	}
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("could not parse %s: %v", path, err))
	}
	return parsed
}

func codexHookFeatures(data map[string]any, path string, problems *[]string) {
	value, ok := data["features"]
	if !ok {
		return
	}
	features, ok := value.(map[string]any)
	if !ok {
		*problems = append(*problems, path+": features must be a table")
		return
	}
	for _, key := range []string{"hooks", "codex_hooks"} {
		if value, ok := features[key]; ok {
			if _, valid := value.(bool); !valid {
				*problems = append(*problems, path+": features."+key+" must be a boolean")
			}
		}
	}
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

// codexSettingsFindings reports Codex settings that change project-document
// discovery, block hooks, or leave the effective instruction file over budget.
func codexSettingsFindings(r repoContext, effectiveDocMaxBytes *int64) ([]string, []string) {
	var problems, warnings []string
	configPath := filepath.Join(nativeConfigHome("CODEX_HOME", ".codex"), "config.toml")
	data := map[string]any{}
	if exists(configPath) {
		data = readCodexLayer(configPath, &problems)
		if data == nil {
			return problems, warnings
		}
	}
	worktree := r.Top
	trust := codexProjectTrust(data, worktree)
	if trust == "untrusted" {
		return append(problems, worktree+" is untrusted in "+configPath+"; Codex does not load AGENTS.md"), warnings
	}
	if trust != "trusted" {
		warnings = append(warnings, worktree+" has no trusted entry in "+configPath+"; only user config was evaluated")
	}
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
		for path := r.Start; within(path, worktree); path = filepath.Dir(path) {
			projectConfigs = append(projectConfigs, filepath.Join(path, ".codex", "config.toml"))
			if path == worktree {
				break
			}
		}
	}
	for i := len(projectConfigs) - 1; i >= 0; i-- {
		path := projectConfigs[i]
		if exists(path) {
			layer := readCodexLayer(path, &problems)
			merge(layer, path)
			problems = append(problems, codexInstructionHookFindings(layer, path)...)
		}
		path = filepath.Join(filepath.Dir(path), "hooks.json")
		if exists(path) {
			problems = append(problems, codexInstructionHookFindings(readJSONSettingsLayer(path, &problems).data, path)...)
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
	if effectiveDocMaxBytes != nil {
		if *effectiveDocMaxBytes <= 0 {
			return append(problems, "effective Codex project_doc_max_bytes must be positive"), warnings
		}
		maxBytes, maxPath = *effectiveDocMaxBytes, "the effective Codex config"
	} else if setting, ok := doc["project_doc_max_bytes"]; ok {
		value, valid := setting.value.(int64)
		if !valid || value <= 0 {
			return append(problems, setting.path+": project_doc_max_bytes must be a positive integer"), warnings
		}
		maxBytes, maxPath = value, setting.path
	}
	override := filepath.Join(worktree, codexRule)
	shared := filepath.Join(worktree, sharedRule)
	if exists(override) {
		problems, warnings = appendCodexDocumentBudgetFinding(problems, warnings, override, true, maxBytes, maxPath)
		problems, warnings = appendCodexDocumentBudgetFinding(problems, warnings, shared, false, maxBytes, maxPath)
	} else {
		problems, warnings = appendCodexDocumentBudgetFinding(problems, warnings, shared, true, maxBytes, maxPath)
	}
	return uniqueStrings(problems...), uniqueStrings(warnings...)
}

func appendCodexDocumentBudgetFinding(problems, warnings []string, document string, active bool, maxBytes int64, maxPath string) ([]string, []string) {
	if !exists(document) {
		return problems, warnings
	}
	body, err := readRegular(document)
	if err != nil {
		problems = append(problems, err.Error())
		return problems, warnings
	}
	size := int64(len(body))
	limit, source := codexInstructionLimit(maxBytes, maxPath)
	if size > limit {
		message := fmt.Sprintf("%s is %d bytes, over %s", document, len(body), source)
		if active {
			problems = append(problems, message)
		} else {
			warnings = append(warnings, message+"; inactive while "+codexRule+" exists")
		}
	}
	return problems, warnings
}

func codexInstructionLimit(maxBytes int64, maxPath string) (int64, string) {
	if maxBytes < codexInstructionMaxBytes {
		return maxBytes, fmt.Sprintf("Codex project_doc_max_bytes %d from %s", maxBytes, maxPath)
	}
	return codexInstructionMaxBytes, fmt.Sprintf("quota %d-byte Codex instruction limit", codexInstructionMaxBytes)
}

func codexInstructionHookFindings(data map[string]any, path string) []string {
	executable, err := os.Executable()
	if err != nil {
		return []string{err.Error()}
	}
	hooks, err := agenthooks.CodexInstructionHooks(data, executable)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	var problems []string
	for _, hook := range hooks {
		problems = append(problems, fmt.Sprintf("%s: hooks.%s conflicts with the fixed account installation", path, hook.Event))
	}
	return problems
}
