package overlayruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	claudeModeClaudeOnly    = "claude-md"
	claudeModeAgentsDefault = "claude-md-or-agents-md"
	claudeModeClaudeAnd     = "claude-md-and-agents-md"
	claudeModeManagedOnly   = "managed-only"
)

func readJSONSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("%s must contain a JSON object: %w", path, err)
	}
	if root == nil {
		return nil, fmt.Errorf("%s must contain a JSON object", path)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("%s has trailing content after the JSON object", path)
	}
	return root, nil
}

func claudePluginOption(data map[string]any, name string) (string, bool) {
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
	value, ok := options[name].(string)
	return value, ok
}

func claudeInstructionMode(data map[string]any) (string, bool) {
	newModeDefault := false
	if value, ok := claudePluginOption(data, "instructionFiles"); ok {
		switch value {
		case claudeModeClaudeOnly, claudeModeClaudeAnd, claudeModeManagedOnly:
			return value, true
		default:
			newModeDefault = true
		}
	}
	if value, ok := claudePluginOption(data, "projectInstructions"); ok {
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

func claudeAccountInstructionMode(configDir string) (string, string, error) {
	path := filepath.Join(configDir, "settings.json")
	data, err := readJSONSettings(path)
	if err != nil {
		return "", path, err
	}
	mode, _ := claudeInstructionMode(data)
	return mode, path, nil
}

func claudeProjectInstructionFiles(start, configDir string) []string {
	userClaudeMD := resolvePath(filepath.Join(configDir, "CLAUDE.md"))
	var files []string
	seen := map[string]bool{}
	for dir := resolvePath(start); ; dir = filepath.Dir(dir) {
		for _, rel := range []string{"CLAUDE.md", ".claude/CLAUDE.md", "CLAUDE.local.md"} {
			path := filepath.Join(dir, rel)
			resolved := resolvePath(path)
			if seen[resolved] || resolved == userClaudeMD || !exists(path) {
				continue
			}
			seen[resolved] = true
			files = append(files, path)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return files
}
