package agentskills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/sky1core/quota/internal/config"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	roleSource         = "source"
	roleClaude         = "claude"
	roleCodexDuplicate = "codex-duplicate"
)

type Target struct {
	Role     string   `json:"role"`
	Accounts []string `json:"accounts,omitempty"`
	Dir      string   `json:"dir"`
}

type Result struct {
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Target     Target `json:"target"`
	Path       string `json:"path"`
	RealPath   string `json:"realPath,omitempty"`
	BackupPath string `json:"backupPath,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type Manager struct {
	scope      string
	sourceDir  string
	targets    []Target
	backupRoot string
	lockPath   string
}

func New(cfg config.Config, scope, repoRoot string) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home, err = config.CanonicalAccountDirectory(home)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(home, ".config", "quota", "agent-skills")
	m := &Manager{scope: scope, backupRoot: filepath.Join(stateDir, "backups"), lockPath: filepath.Join(stateDir, "operations.lock")}
	switch scope {
	case "global":
		claude, claudeErrors := cfg.ResolveAccounts()
		codex, codexErrors := cfg.ResolveCodexAccounts()
		if errs := append(claudeErrors, codexErrors...); len(errs) != 0 {
			return nil, errors.New(strings.Join(errs, "; "))
		}
		if m.sourceDir, err = resolveDir(filepath.Join(home, ".agents/skills")); err != nil {
			return nil, err
		}
		source := Target{Role: roleSource, Dir: m.sourceDir}
		for _, account := range codex {
			source.Accounts = append(source.Accounts, account.Key)
		}
		m.targets = []Target{source}
		for _, account := range claude {
			if err := m.addTarget(roleClaude, account.Key, filepath.Join(account.ConfigDir, "skills")); err != nil {
				return nil, err
			}
		}
		for _, account := range codex {
			if err := m.addTarget(roleCodexDuplicate, account.Key, filepath.Join(account.Home, "skills")); err != nil {
				return nil, err
			}
		}
	case "repo":
		if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
			return nil, fmt.Errorf("repository root must be an absolute clean path")
		}
		repoRoot, err = filepath.EvalSymlinks(repoRoot)
		if err != nil {
			return nil, err
		}
		if m.sourceDir, err = resolveDir(filepath.Join(repoRoot, ".agents/skills")); err != nil {
			return nil, err
		}
		m.targets = []Target{{Role: roleSource, Dir: m.sourceDir}}
		if err := m.addTarget(roleClaude, "", filepath.Join(repoRoot, ".claude", "skills")); err != nil {
			return nil, err
		}
		if err := m.addTarget(roleCodexDuplicate, "", filepath.Join(repoRoot, ".codex", "skills")); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported skill scope %q", scope)
	}
	for i, target := range m.targets {
		if err := checkDirectories(target.Dir); err != nil {
			return nil, err
		}
		for _, other := range m.targets[:i] {
			for _, pair := range [][2]string{{target.Dir, other.Dir}, {other.Dir, target.Dir}} {
				for parent := filepath.Dir(pair[0]); ; parent = filepath.Dir(parent) {
					same, err := sameDirectory(parent, pair[1])
					if err != nil {
						return nil, err
					}
					if same {
						return nil, fmt.Errorf("overlapping skill targets: %s and %s", target.Dir, other.Dir)
					}
					if parent == filepath.Dir(parent) {
						break
					}
				}
			}
		}
	}
	return m, nil
}

// addTarget merges account directories that resolve to the same place.
func (m *Manager) addTarget(role, account, dir string) error {
	dir, err := resolveDir(dir)
	if err != nil {
		return err
	}
	for i := range m.targets {
		existing := &m.targets[i]
		same, err := sameDirectory(existing.Dir, dir)
		if err != nil {
			return err
		}
		if !same {
			continue
		}
		switch {
		case existing.Role == role:
		case existing.Role == roleSource && role == roleClaude:
		case existing.Role == roleSource && role == roleCodexDuplicate:
			return nil
		default:
			return fmt.Errorf("skill directory %s is used as both %s and %s", dir, existing.Role, role)
		}
		if account != "" {
			existing.Accounts = append(existing.Accounts, account)
		}
		return nil
	}
	target := Target{Role: role, Dir: dir}
	if account != "" {
		target.Accounts = []string{account}
	}
	m.targets = append(m.targets, target)
	return nil
}

// resolveDir resolves symlinks in the existing part of path and keeps the
// missing remainder as written.
func resolveDir(path string) (string, error) {
	path = filepath.Clean(path)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append([]string{filepath.Base(path)}, missing...)
		path = parent
	}
}

func sameDirectory(a, b string) (bool, error) {
	for a != b {
		first, firstErr := os.Stat(a)
		second, secondErr := os.Stat(b)
		if firstErr != nil && !errors.Is(firstErr, fs.ErrNotExist) {
			return false, firstErr
		}
		if secondErr != nil && !errors.Is(secondErr, fs.ErrNotExist) {
			return false, secondErr
		}
		if firstErr == nil && secondErr == nil {
			return os.SameFile(first, second), nil
		}
		if firstErr == nil || secondErr == nil || filepath.Base(a) != filepath.Base(b) {
			return false, nil
		}
		a, b = filepath.Dir(a), filepath.Dir(b)
	}
	return true, nil
}

func foldedSkillName(name string) string {
	return cases.Fold().String(norm.NFC.String(name))
}

func (m *Manager) sourcePath(name string) string {
	return filepath.Join(m.sourceDir, name)
}

func (m *Manager) List() ([]Result, error) {
	names, err := m.names(false)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(names)*len(m.targets))
	for _, name := range names {
		inspected, err := m.inspect(name)
		if err != nil {
			return append(results, inspected...), err
		}
		results = append(results, inspected...)
	}
	return results, nil
}

func (m *Manager) names(sourceOnly bool) ([]string, error) {
	names := map[string]bool{}
	for _, target := range m.targets {
		if sourceOnly && target.Role != roleSource {
			continue
		}
		if err := checkDirectories(target.Dir); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(target.Dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			// Codex keeps its bundled skills under <CODEX_HOME>/skills/.system.
			if target.Role == roleCodexDuplicate && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			names[entry.Name()] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ordered, nil
}

func (m *Manager) inspect(name string) ([]Result, error) {
	source := m.sourcePath(name)
	results := make([]Result, 0, len(m.targets))
	for _, target := range m.targets {
		path := filepath.Join(target.Dir, name)
		result := Result{Name: name, Scope: m.scope, Target: target, Path: path}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			result.Status = "missing"
			results = append(results, result)
			continue
		}
		if err != nil {
			return results, fmt.Errorf("inspect %s: %w", path, err)
		}
		isLink := info.Mode()&os.ModeSymlink != 0
		if isLink {
			link, err := os.Readlink(path)
			if err != nil {
				return results, err
			}
			if !filepath.IsAbs(link) {
				link = filepath.Join(target.Dir, link)
			}
			result.RealPath = filepath.Clean(link)
		}
		switch target.Role {
		case roleSource:
			followed, broken, err := statLink(path)
			if err != nil {
				return results, err
			}
			valid := false
			if !broken && followed.IsDir() {
				valid, err = isSkillDir(path)
				if err != nil {
					result.Status, result.Error = "failed", err.Error()
					return append(results, result), err
				}
			}
			switch {
			case broken:
				result.Status = "broken"
			case valid:
				result.Status = "source"
				if result.RealPath, err = filepath.EvalSymlinks(path); err != nil {
					return results, err
				}
			default:
				result.Status = "invalid"
			}
		case roleClaude:
			switch {
			case isLink:
				followed, broken, err := statLink(path)
				if err != nil {
					return results, err
				}
				if broken {
					result.Status = "broken"
				} else if sourceInfo, err := os.Stat(source); err == nil && os.SameFile(followed, sourceInfo) {
					result.Status = "linked"
				} else {
					result.Status = "external"
				}
			case info.IsDir():
				result.Status, result.RealPath = "copy", path
			default:
				result.Status = "invalid"
			}
		case roleCodexDuplicate:
			result.Status = "duplicate"
			if !isLink {
				result.RealPath = path
			}
		}
		results = append(results, result)
	}
	return results, nil
}

func statLink(path string) (os.FileInfo, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return nil, true, nil
	}
	return info, false, err
}

func isSkillDir(path string) (bool, error) {
	info, err := os.Stat(filepath.Join(path, "SKILL.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func (m *Manager) Install(ctx context.Context, skills []FetchedSkill) ([]Result, error) {
	if len(skills) == 0 {
		return nil, errors.New("no skills to install")
	}
	seen := map[string]bool{}
	for _, skill := range skills {
		if err := ValidateName(skill.Name); err != nil {
			return nil, err
		}
		key := foldedSkillName(skill.Name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate or ambiguous skill name: %s", skill.Name)
		}
		seen[key] = true
		if err := validateTree(skill.Dir); err != nil {
			return nil, fmt.Errorf("source %s: %w", skill.Name, err)
		}
	}
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	results := []Result{}
	// Links that already point at the canonical path become valid once it is installed.
	pendingLinks := map[string]bool{}
	for _, skill := range skills {
		inspected, err := m.inspect(skill.Name)
		if err != nil {
			return append(results, inspected...), err
		}
		source := m.sourcePath(skill.Name)
		for i := range inspected {
			result := &inspected[i]
			switch result.Target.Role {
			case roleSource:
				if result.Status == "missing" {
					result.Status = "ready"
				} else {
					result.Status, result.Error = "blocked", "canonical path already exists; use link"
				}
			case roleClaude:
				if result.Status == "missing" {
					result.Status = "ready"
				} else if result.Status == "broken" && result.RealPath == source {
					result.Status = "ready"
					pendingLinks[result.Path] = true
				} else {
					result.Status, result.Error = "blocked", "target path is occupied"
				}
			case roleCodexDuplicate:
				if result.Status == "duplicate" {
					result.Status, result.Error = "blocked", "duplicate skill name in Codex skills directory"
				}
			}
		}
		results = append(results, inspected...)
	}
	if err := resultErrors(results); err != nil {
		skipReady(results, "preflight failed; no skill files changed")
		return results, err
	}
	for _, skill := range skills {
		source := m.sourcePath(skill.Name)
		if err := ctx.Err(); err != nil {
			skipReady(results, err.Error())
			return results, err
		}
		if err := installSource(skill.Dir, source); err != nil {
			markFailed(results, skill.Name, source, err)
			return results, resultErrors(results)
		}
		markStatus(results, skill.Name, source, "installed")
		for _, result := range resultsForName(results, skill.Name) {
			if result.Target.Role != roleClaude || result.Status != "ready" {
				continue
			}
			if pendingLinks[result.Path] {
				markStatus(results, skill.Name, result.Path, "linked")
				continue
			}
			if err := ctx.Err(); err != nil {
				markFailed(results, skill.Name, result.Path, err)
				return results, resultErrors(results)
			}
			if err := createLink(result.Target.Dir, source, result.Path); err != nil {
				markFailed(results, skill.Name, result.Path, err)
				return results, resultErrors(results)
			}
			markStatus(results, skill.Name, result.Path, "linked")
		}
	}
	return results, nil
}

// Link creates missing Claude links for skills that already exist at the
// canonical path. With no names it covers every valid skill there.
func (m *Manager) Link(ctx context.Context, names []string) ([]Result, error) {
	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return nil, err
		}
	}
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	all := len(names) == 0
	if all {
		candidates, err := m.names(true)
		if err != nil {
			return nil, err
		}
		for _, name := range candidates {
			if ValidateName(name) == nil {
				names = append(names, name)
			}
		}
	}
	results := []Result{}
	for _, name := range names {
		inspected, err := m.inspect(name)
		if err != nil {
			return append(results, inspected...), err
		}
		sourceReady := inspected[0].Status == "source"
		if all && !sourceReady {
			continue
		}
		for i := range inspected {
			result := &inspected[i]
			switch result.Target.Role {
			case roleSource:
				if !sourceReady {
					result.Error = "no valid skill at the canonical path"
				}
			case roleClaude:
				if result.Status == "missing" && sourceReady {
					result.Status = "ready"
				} else if result.Status != "linked" && result.Status != "missing" {
					result.Error = "target path is occupied"
				}
			case roleCodexDuplicate:
				if result.Status == "duplicate" {
					result.Error = "duplicate skill name in Codex skills directory"
				}
			}
		}
		results = append(results, inspected...)
	}
	for i := range results {
		result := &results[i]
		if result.Status != "ready" {
			continue
		}
		if err := ctx.Err(); err != nil {
			skipReady(results, err.Error())
			return results, resultErrors(results)
		}
		if err := createLink(result.Target.Dir, m.sourcePath(result.Name), result.Path); err != nil {
			result.Status, result.Error = "failed", err.Error()
			continue
		}
		result.Status = "linked"
	}
	return results, resultErrors(results)
}

func createLink(dir, source, path string) error {
	if err := checkDirectories(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	relative, err := filepath.Rel(dir, source)
	if err != nil {
		return err
	}
	return os.Symlink(relative, path)
}

func installSource(from, to string) error {
	if err := checkDirectories(filepath.Dir(to)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(to), ".quota-skill-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, filepath.Base(to))
	if err := copyTree(from, candidate); err != nil {
		return err
	}
	return os.Rename(candidate, to)
}

func (m *Manager) Remove(ctx context.Context, name string) ([]Result, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	results := make([]Result, 0, len(m.targets))
	present := false
	for _, target := range m.targets {
		path := filepath.Join(target.Dir, name)
		result := Result{Name: name, Scope: m.scope, Target: target, Path: path, Status: "ready"}
		_, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			result.Status = "missing"
		case err != nil:
			result.Status, result.Error = "failed", err.Error()
			return append(results, result), err
		default:
			present = true
		}
		results = append(results, result)
	}
	if !present {
		return results, fmt.Errorf("%s: skill is absent in %s scope", name, m.scope)
	}
	if err := checkDirectories(m.backupRoot); err != nil {
		return results, err
	}
	if err := os.MkdirAll(m.backupRoot, 0o700); err != nil {
		return results, err
	}
	backupDevice, err := device(m.backupRoot)
	if err != nil {
		return results, err
	}
	// Directories can only be backed up by rename; links can be recreated anywhere.
	copyLinks := map[string]bool{}
	for i := range results {
		result := &results[i]
		if result.Status == "missing" {
			continue
		}
		info, err := os.Lstat(result.Path)
		if err != nil {
			return results, err
		}
		dirDevice, err := device(result.Target.Dir)
		if err != nil {
			return results, err
		}
		if dirDevice == backupDevice {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			copyLinks[result.Path] = true
			continue
		}
		result.Error = fmt.Sprintf("cannot back up across filesystems to %s", m.backupRoot)
	}
	if err := resultErrors(results); err != nil {
		return results, fmt.Errorf("nothing removed: %w", err)
	}
	backupDir, err := os.MkdirTemp(m.backupRoot, "removed-")
	if err != nil {
		return results, err
	}
	var moved []int
	for i := range results {
		result := &results[i]
		if result.Status == "missing" {
			continue
		}
		if err := ctx.Err(); err != nil {
			result.Status, result.Error = "failed", err.Error()
			return results, rollbackRemove(results, moved, copyLinks)
		}
		backupPath := filepath.Join(backupDir, fmt.Sprintf("%d-%s-%s", i, result.Target.Role, name))
		if copyLinks[result.Path] {
			err = moveLink(result.Path, backupPath)
		} else {
			err = os.Rename(result.Path, backupPath)
		}
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
			return results, rollbackRemove(results, moved, copyLinks)
		}
		result.BackupPath = backupPath
		result.Status = "backed-up"
		moved = append(moved, i)
		if _, err := os.Lstat(result.Path); !os.IsNotExist(err) {
			result.Status, result.Error = "failed", fmt.Sprintf("could not verify removal: %v", err)
			return results, rollbackRemove(results, moved[:len(moved)-1], copyLinks)
		}
	}
	return results, nil
}

func rollbackRemove(results []Result, moved []int, copyLinks map[string]bool) error {
	for j := len(moved) - 1; j >= 0; j-- {
		result := &results[moved[j]]
		var err error
		if copyLinks[result.Path] {
			err = moveLink(result.BackupPath, result.Path)
		} else {
			err = os.Rename(result.BackupPath, result.Path)
		}
		if err != nil {
			result.Status, result.Error = "failed", fmt.Sprintf("restore from backup failed: %v", err)
			continue
		}
		result.Status, result.BackupPath = "restored", ""
	}
	return fmt.Errorf("remove failed, moved entries were restored unless reported otherwise: %w", resultErrors(results))
}

func device(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%s: device is unavailable", path)
	}
	return uint64(stat.Dev), nil
}

func moveLink(path, backupPath string) error {
	link, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if err := os.Symlink(link, backupPath); err != nil {
		return err
	}
	if current, err := os.Readlink(path); err != nil || current != link {
		return fmt.Errorf("link changed during backup: %v", err)
	}
	return os.Remove(path)
}

func (m *Manager) lock() (func(), error) {
	dir := filepath.Dir(m.lockPath)
	if err := checkDirectories(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("invalid skill lock file")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another skill operation is running: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func resultsForName(results []Result, name string) []Result {
	var found []Result
	for _, result := range results {
		if result.Name == name {
			found = append(found, result)
		}
	}
	return found
}

func markStatus(results []Result, name, path, status string) {
	for i := range results {
		if results[i].Name == name && results[i].Path == path {
			results[i].Status = status
		}
	}
}

func markFailed(results []Result, name, path string, err error) {
	for i := range results {
		if results[i].Name == name && results[i].Path == path {
			results[i].Status, results[i].Error = "failed", err.Error()
		} else if results[i].Status == "ready" {
			results[i].Status, results[i].Error = "skipped", "stopped after a partial failure"
		}
	}
}

func resultErrors(results []Result) error {
	var errs []error
	for _, result := range results {
		if result.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", result.Path, result.Error))
		}
	}
	return errors.Join(errs...)
}

func skipReady(results []Result, reason string) {
	for i := range results {
		if results[i].Status == "ready" {
			results[i].Status, results[i].Error = "skipped", reason
		}
	}
}
