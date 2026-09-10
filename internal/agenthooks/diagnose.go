package agenthooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

func unsupportedHookVariants(hook map[string]any) []string {
	var reasons []string
	for _, key := range []string{"if", "args", "async", "asyncRewake", "shell"} {
		value, exists := hook[key]
		if !exists {
			continue
		}
		if (key == "async" || key == "asyncRewake") && value == false {
			continue
		}
		if key == "shell" && value == "bash" {
			continue
		}
		reasons = append(reasons, fmt.Sprintf("managed hook field %q is unsupported", key))
	}
	return reasons
}

func claudeDisableReasons(root map[string]any) []string {
	var reasons []string
	reasons = append(reasons, blockingBool(root, "disableAllHooks", true)...)
	return reasons
}

func blockingBool(root map[string]any, key string, blocked bool) []string {
	value, exists := root[key]
	if !exists {
		return nil
	}
	b, ok := value.(bool)
	if !ok {
		return []string{key + " must be a boolean"}
	}
	if b == blocked {
		return []string{fmt.Sprintf("%s is %t; managed hooks are disabled", key, b)}
	}
	return nil
}

func codexConfigPath() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

func readCodexConfig(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	root := map[string]any{}
	if err := toml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func codexConfigReasons(root map[string]any) []string {
	var reasons []string
	if features, exists := root["features"]; exists {
		if obj, ok := features.(map[string]any); ok {
			_, hasCanonical := obj["hooks"]
			if alias, exists := obj["codex_hooks"]; hasCanonical && exists {
				if _, ok := alias.(bool); !ok {
					reasons = append(reasons, "features.codex_hooks must be a boolean")
				}
			}
			keys := []string{"hooks"}
			if !hasCanonical {
				keys = []string{"codex_hooks"}
			}
			for _, key := range keys {
				for _, reason := range blockingBool(obj, key, false) {
					reasons = append(reasons, "features."+reason)
				}
			}
		} else {
			reasons = append(reasons, "features must be a table")
		}
	}
	return reasons
}
