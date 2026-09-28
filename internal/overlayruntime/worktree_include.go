package overlayruntime

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

func (r repoContext) worktreeIncludeWarnings(agent string) ([]string, error) {
	files, err := gitOutput(r.Context, r.Top, "ls-files", "--others", "-z", "--", ":(glob)**/AGENTS.md")
	if err != nil || len(files) == 0 {
		return nil, err
	}
	var warnings []string
	include := filepath.Join(r.Top, ".worktreeinclude")
	body, notice, present, err := readInstructions(include)
	if err != nil {
		return warnings, fmt.Errorf("cannot inspect %s: %w", include, err)
	}
	if notice != "" {
		return append(warnings, notice), nil
	}
	for _, name := range bytes.Split(files, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		rel := string(name)
		path := filepath.Join(r.Top, rel)
		_, notice, _, err := readInstructions(path)
		if err != nil {
			return warnings, fmt.Errorf("cannot inspect %s: %w", path, err)
		}
		if notice != "" {
			warnings = append(warnings, notice)
			continue
		}
		_, err = gitOutput(r.Context, r.Top, "check-ignore", "-q", "--", rel)
		ignored, err := gitBoolean(err)
		if err != nil {
			return warnings, err
		}
		if !ignored {
			warnings = append(warnings, path+" is untracked but not gitignored; .worktreeinclude only copies gitignored files")
			continue
		}
		if !present {
			warnings = append(warnings, include+" is missing; native worktree creation has no include rule for "+path)
			continue
		}
		matched, err := r.worktreeIncludeMatches(include, name)
		if err != nil {
			return warnings, err
		}
		if !matched {
			warnings = append(warnings, include+" does not include "+path+"; add its relative path to copy it during native worktree creation")
			continue
		}
		if agent == "claude" && filepath.Dir(rel) != "." {
			_, err := gitOutput(r.Context, r.Top, "check-ignore", "-q", "--", filepath.Dir(rel)+"/")
			ignoredParent, err := gitBoolean(err)
			if err != nil {
				return warnings, err
			}
			if ignoredParent && !explicitIncludePath(body, rel) {
				warnings = append(warnings, path+" is inside a gitignored directory; pattern matching alone does not verify Claude's directory traversal. Add the explicit relative path to "+include+" and verify the new worktree")
			}
		}
	}
	return warnings, nil
}

func explicitIncludePath(body, path string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == path || line == "/"+path {
			return true
		}
	}
	return false
}

func (r repoContext) worktreeIncludeMatches(include string, name []byte) (bool, error) {
	matched, err := gitOutput(r.Context, r.Top, "ls-files", "--others", "--ignored", "-z", "--exclude-from="+include, "--", ":(literal)"+string(name))
	return bytes.Equal(matched, append(append([]byte(nil), name...), 0)), err
}

func (r repoContext) claudeWorktreeCreateWarnings(configDir string) ([]string, error) {
	paths := []string{filepath.Join(r.Top, ".claude", "settings.json"), filepath.Join(r.Top, ".claude", "settings.local.json")}
	if configDir != "" {
		paths = append(paths, filepath.Join(configDir, "settings.json"))
	}
	var warnings []string
	for _, path := range paths {
		data, err := readJSONSettings(path)
		if err != nil {
			return warnings, err
		}
		hooks, _ := data["hooks"].(map[string]any)
		entries, _ := hooks["WorktreeCreate"].([]any)
		if len(entries) > 0 {
			warnings = append(warnings, path+": WorktreeCreate replaces native worktree creation; .worktreeinclude copying must be handled by that hook")
		}
	}
	return warnings, nil
}
