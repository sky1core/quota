package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

type WorktreeIncludeSetup struct {
	Path    string   `json:"path"`
	Added   []string `json:"added,omitempty"`
	Applied bool     `json:"applied"`
}

func SetupWorktreeInclude(ctx context.Context, dir string, dryRun bool) (WorktreeIncludeSetup, error) {
	var result WorktreeIncludeSetup
	if err := ValidateGitEnvironment(ctx, dir); err != nil {
		return result, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return result, err
	}
	result.Path = filepath.Join(r.Top, ".worktreeinclude")
	if !dryRun {
		home, err := os.UserHomeDir()
		if err != nil {
			return result, err
		}
		lockDir := filepath.Join(home, ".config", "quota", "instructions", "setup")
		if err := os.MkdirAll(lockDir, 0o700); err != nil {
			return result, err
		}
		lock, err := os.OpenFile(filepath.Join(lockDir, instructionHash([]byte(result.Path))+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return result, err
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return result, fmt.Errorf("cannot lock %s for setup: %w", result.Path, err)
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	before, notice, present, err := readInstructions(result.Path)
	if err != nil {
		return result, err
	}
	if notice != "" {
		return result, fmt.Errorf("%s", notice)
	}
	files, err := gitOutput(ctx, r.Top, "ls-files", "--others", "-z", "--", ":(glob)**/AGENTS.md")
	if err != nil {
		return result, err
	}
	for _, name := range bytes.Split(files, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		rel := string(name)
		path := filepath.Join(r.Top, rel)
		if _, _, err := checkInstructionSource(path, true); err != nil {
			return result, err
		}
		if !utf8.ValidString(rel) || strings.ContainsAny(rel, "\r\n") {
			return result, fmt.Errorf("cannot represent %q as a .worktreeinclude line", path)
		}
		_, err := gitOutput(ctx, r.Top, "check-ignore", "-q", "--", rel)
		ignored, err := gitBoolean(err)
		if err != nil {
			return result, err
		}
		if !ignored {
			return result, fmt.Errorf("%s is untracked but not gitignored; .worktreeinclude cannot copy it; Git tracking and ignore settings were left unchanged", path)
		}
		matched := false
		if present {
			matched, err = r.worktreeIncludeMatches(result.Path, name)
			if err != nil {
				return result, err
			}
		}
		_, err = gitOutput(ctx, r.Top, "check-ignore", "-q", "--", filepath.Dir(rel)+"/")
		ignoredParent, err := gitBoolean(err)
		if err != nil {
			return result, err
		}
		needsExplicit := ignoredParent && !explicitIncludePath(before, rel)
		escaped := includeLiteralPath(rel)
		if !matched || (needsExplicit && escaped == rel) {
			result.Added = append(result.Added, "/"+escaped)
		}
		if needsExplicit && escaped != rel {
			result.Added = append(result.Added, "/"+rel)
		}
	}
	if len(result.Added) == 0 || dryRun {
		return result, nil
	}
	after := before
	if after != "" && !strings.HasSuffix(after, "\n") {
		after += "\n"
	}
	after += strings.Join(result.Added, "\n") + "\n"
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := saveWorktreeInclude(result.Path, before, after, present); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

func includeLiteralPath(path string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(path)
}

func saveWorktreeInclude(path, before, after string, present bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".worktreeinclude-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(after); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if !present {
		return os.Link(temp.Name(), path)
	}
	latest, notice, exists, err := readInstructions(path)
	if err != nil {
		return err
	}
	if notice != "" || !exists || latest != before {
		return fmt.Errorf("%s changed during setup; preserving external edit", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func instructionHash(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}
