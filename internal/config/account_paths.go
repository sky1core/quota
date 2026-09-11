package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func CanonicalAccountDirectory(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" || strings.ContainsRune(dir, 0) {
		return "", errors.New("account directory is required")
	}
	expanded, err := absoluteAccountPath(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(expanded); err == nil {
		if !info.IsDir() {
			return "", errors.New("account path is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	suffix := []string{}
	current := expanded
	visited := map[string]bool{}
	for {
		if visited[current] {
			return "", fmt.Errorf("account directory symlink cycle: %s", dir)
		}
		visited[current] = true
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				parent, _ := splitAccountPath(current)
				target = parent + string(filepath.Separator) + target
			}
			current = target
			continue
		}
		parent, name := splitAccountPath(current)
		if name == ".." {
			return "", fmt.Errorf("account directory traverses a missing parent: %s", dir)
		}
		if parent == current {
			return "", fmt.Errorf("cannot resolve directory %s", dir)
		}
		suffix = append(suffix, name)
		current = parent
	}
}

func absoluteAccountPath(dir string) (string, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = home + strings.TrimPrefix(dir, "~")
	}
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd + string(filepath.Separator) + dir, nil
}

func splitAccountPath(path string) (string, string) {
	path = strings.TrimRight(path, string(filepath.Separator))
	index := strings.LastIndexByte(path, filepath.Separator)
	if index < 0 {
		return string(filepath.Separator), path
	}
	if index == 0 {
		return string(filepath.Separator), path[1:]
	}
	return path[:index], path[index+1:]
}

func DefaultAccountDirectory(provider string) (string, error) {
	dir, err := defaultAccountDirectoryPath(provider)
	if err != nil {
		return "", err
	}
	return CanonicalAccountDirectory(dir)
}

func defaultAccountDirectoryPath(provider string) (string, error) {
	var env string
	switch provider {
	case "claude":
		env = "CLAUDE_CONFIG_DIR"
	case "codex":
		env = "CODEX_HOME"
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	dir := os.Getenv(env)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "."+provider)
	}
	return dir, nil
}

type accountDirectory struct{ key, dir string }

func resolveAccountDirectories(provider string, extras []accountDirectory, keyPattern *regexp.Regexp) ([]accountDirectory, []string) {
	defaultDir, defaultErr := defaultAccountDirectoryPath(provider)
	entries := append([]accountDirectory{{provider, defaultDir}}, extras...)
	valid := make([]bool, len(entries))
	canonicalDirs := make([]string, len(entries))
	keys, dirs := map[string]int{}, map[string]int{}
	var skipped []string
	for i, entry := range entries {
		if i > 0 && !keyPattern.MatchString(entry.key) {
			skipped = append(skipped, fmt.Sprintf("%s account key %q must match %s-<N>, skipped", provider, entry.key, provider))
			continue
		}
		keys[entry.key]++
		dir, err := CanonicalAccountDirectory(entry.dir)
		if i == 0 && defaultErr != nil {
			err = defaultErr
		}
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s account %q directory is invalid: %v, skipped", provider, entry.key, err))
			continue
		}
		abs, err := absoluteAccountPath(entry.dir)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s account %q directory is invalid: %v, skipped", provider, entry.key, err))
			continue
		}
		for _, component := range strings.Split(abs, string(filepath.Separator)) {
			if component == ".." {
				abs = dir
				break
			}
		}
		entries[i].dir = filepath.Clean(abs)
		canonicalDirs[i] = dir
		dirs[dir]++
		valid[i] = true
	}
	var resolved []accountDirectory
	for i, entry := range entries {
		if !valid[i] {
			continue
		}
		if keys[entry.key] > 1 || dirs[canonicalDirs[i]] > 1 {
			skipped = append(skipped, fmt.Sprintf("%s account %q has a duplicate key or directory; ownership is ambiguous, skipped", provider, entry.key))
			continue
		}
		resolved = append(resolved, entry)
	}
	return resolved, skipped
}
