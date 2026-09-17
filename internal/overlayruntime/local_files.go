package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func localPathComparisonKey(path string) string {
	return norm.NFC.String(cases.Fold().String(path))
}

func validateLocalFiles(paths []string) error {
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" || filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00\r\n:*?[]") {
			return fmt.Errorf("invalid local file path %q", path)
		}
		parts := strings.Split(path, "/")
		if strings.EqualFold(parts[len(parts)-1], ".gitignore") {
			return fmt.Errorf("local file path %q changes Git ignore rules and cannot be copied", path)
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
				return fmt.Errorf("unsafe local file path %q", path)
			}
		}
		key := localPathComparisonKey(path)
		for _, reserved := range []string{sharedRule, localRule, codexRule, "CLAUDE.md", localBridge} {
			reservedKey := localPathComparisonKey(reserved)
			if key == reservedKey || strings.HasPrefix(key, reservedKey+"/") {
				return fmt.Errorf("local file path %q is reserved", path)
			}
		}
		if seen[key] {
			return fmt.Errorf("duplicate local file path %q", path)
		}
		seen[key] = true
	}
	for path := range seen {
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("overlapping local file paths %q and %q", parent, path)
			}
		}
	}
	return nil
}

func managedParents(root, rel string, create bool) error {
	path := root
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			if parts[i] == "." {
				continue
			}
			path = filepath.Join(path, parts[i])
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && i >= 0 {
			if !create {
				return nil
			}
			if err = os.Mkdir(path, 0o700); err != nil {
				return err
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a regular directory", path)
		}
	}
	return nil
}

func updateLocalFiles(ctx context.Context, dir string, update func(*RepositoryState) error) ([]string, error) {
	if e := ValidateGitEnvironment(ctx); e != nil {
		return nil, e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return nil, e
	}
	var files []string
	e = withStateLock(r, func() error {
		s, e := readState(r)
		if e != nil {
			return e
		}
		if e = update(&s); e != nil {
			return e
		}
		if e = validateLocalFiles(s.LocalFiles); e != nil {
			return e
		}
		sort.Strings(s.LocalFiles)
		files = append([]string(nil), s.LocalFiles...)
		return writeState(r, s)
	})
	return files, e
}

// AddLocalFiles registers primary-relative paths for copying into linked
// worktrees. Sources must exist as untracked, ignored regular files.
func AddLocalFiles(ctx context.Context, dir string, paths []string) ([]string, error) {
	return updateLocalFiles(ctx, dir, func(s *RepositoryState) error {
		r, e := resolveContext(ctx, dir)
		if e != nil {
			return e
		}
		for _, path := range paths {
			if e := validateLocalFiles([]string{path}); e != nil {
				return e
			}
			if contains(s.LocalFiles, path) {
				continue
			}
			if _, e := r.localFileSource(path); e != nil {
				return e
			}
			s.LocalFiles = append(s.LocalFiles, path)
		}
		return nil
	})
}

func RemoveLocalFiles(ctx context.Context, dir string, paths []string) ([]string, error) {
	return updateLocalFiles(ctx, dir, func(s *RepositoryState) error {
		for _, path := range paths {
			if !contains(s.LocalFiles, path) {
				return fmt.Errorf("local file %q is not registered", path)
			}
			var kept []string
			for _, existing := range s.LocalFiles {
				if existing != path {
					kept = append(kept, existing)
				}
			}
			s.LocalFiles = kept
		}
		return nil
	})
}

func ListLocalFiles(ctx context.Context, dir string) ([]string, error) {
	if e := ValidateGitEnvironment(ctx); e != nil {
		return nil, e
	}
	r, e := resolveContext(ctx, dir)
	if e != nil {
		return nil, e
	}
	s, e := readState(r)
	if e != nil {
		return nil, e
	}
	return append([]string(nil), s.LocalFiles...), nil
}

// localFileSource validates a registered local file in the primary checkout and
// returns its content.
func (r repoContext) localFileSource(rel string) ([]byte, error) {
	if err := managedParents(r.Root, rel, false); err != nil {
		return nil, err
	}
	path := filepath.Join(r.Root, rel)
	if !r.Bare() {
		tracked, err := r.tracked(r.Root, rel)
		if err != nil {
			return nil, err
		}
		if tracked {
			return nil, fmt.Errorf("%s is tracked; it must stay local-only", path)
		}
		ignored, err := r.ignored(r.Root, rel)
		if err != nil {
			return nil, err
		}
		if !ignored {
			return nil, fmt.Errorf("%s is not git-ignored", path)
		}
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, fmt.Errorf("local file source %s: %w", path, err)
	}
	return data, nil
}
