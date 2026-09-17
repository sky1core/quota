package agentinstructions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/childprocess"
)

// GlobalIgnoreLines are the generated file names every repository must ignore.
var GlobalIgnoreLines = []string{"AGENTS.override.md", "CLAUDE.local.md"}

// GlobalIgnoreMarker heads the block of lines that quota manages; lines the
// user wrote elsewhere in the file are never managed.
const GlobalIgnoreMarker = "# quota-cli agent instructions (managed)"

type GlobalIgnorePlan struct {
	Path    string   `json:"path"`
	Add     []string `json:"add,omitempty"`
	Remove  []string `json:"remove,omitempty"`
	Changed bool     `json:"changed"`
	before  []byte
	after   []byte
	perm    os.FileMode
}

// GlobalIgnorePath returns core.excludesFile from user or system Git config, or
// Git's default location when it is unset.
func GlobalIgnorePath(ctx context.Context) (string, error) {
	if value, ok, err := configuredIgnorePath(ctx, "--global"); err != nil {
		return "", err
	} else if ok {
		return value, nil
	}
	systemEnabled, err := systemGitConfigEnabled()
	if err != nil {
		return "", err
	}
	if systemEnabled {
		if value, ok, err := configuredIgnorePath(ctx, "--system"); err != nil {
			return "", err
		} else if ok {
			return value, nil
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "git", "ignore"), nil
}

func systemGitConfigEnabled() (bool, error) {
	value, ok := os.LookupEnv("GIT_CONFIG_NOSYSTEM")
	if !ok || value == "" {
		return true, nil
	}
	noSystem, err := parseGitBool(value)
	if err != nil {
		return false, fmt.Errorf("GIT_CONFIG_NOSYSTEM has invalid boolean value %q", value)
	}
	return !noSystem, nil
}

func parseGitBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	number := value
	if len(number) > 0 {
		switch number[len(number)-1] {
		case 'k', 'K', 'm', 'M', 'g', 'G':
			number = number[:len(number)-1]
		}
	}
	parsed, err := strconv.ParseInt(number, 0, 64)
	return parsed != 0, err
}

func configuredIgnorePath(ctx context.Context, scope string) (string, bool, error) {
	cmd := childprocess.CommandContext(ctx, "git", "config", scope, "--includes", "--path", "--get", "core.excludesFile")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := childprocess.Run(cmd)
	if err == nil {
		value := strings.TrimSuffix(out.String(), "\n")
		if value == "" {
			return "", false, fmt.Errorf("core.excludesFile is set to an empty value")
		}
		if strings.HasPrefix(value, "~/") || value == "~" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", false, err
			}
			value = filepath.Join(home, strings.TrimPrefix(value, "~"))
		}
		if !filepath.IsAbs(value) {
			return "", false, fmt.Errorf("core.excludesFile %q is not an absolute path", value)
		}
		return filepath.Clean(value), true, nil
	}
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return "", false, fmt.Errorf("read core.excludesFile: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return "", false, nil
}

func readGlobalIgnore(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("global ignore file is a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("global ignore file is not a regular file: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if !utf8.Valid(content) {
		return nil, 0, fmt.Errorf("global ignore file is not UTF-8: %s", path)
	}
	return content, info.Mode().Perm(), nil
}

func isManagedIgnoreLine(line string) bool {
	for _, want := range GlobalIgnoreLines {
		if line == want {
			return true
		}
	}
	return false
}

// managedBlock returns the index of the marker line and the index just past
// the managed lines that immediately follow it, or -1 when absent.
func managedBlock(lines []string) (int, int) {
	for i, line := range lines {
		if line != GlobalIgnoreMarker {
			continue
		}
		end := i + 1
		for end < len(lines) && isManagedIgnoreLine(lines[end]) {
			end++
		}
		return i, end
	}
	return -1, -1
}

func splitIgnoreLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
}

func joinIgnoreLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// PlanGlobalIgnore reads the file once and records the content the later
// save must still observe.
func PlanGlobalIgnore(path string, uninstall bool) (GlobalIgnorePlan, error) {
	plan := GlobalIgnorePlan{Path: path}
	content, perm, err := readGlobalIgnore(path)
	if err != nil {
		return plan, err
	}
	plan.before, plan.perm = content, perm
	lines := splitIgnoreLines(content)
	start, end := managedBlock(lines)
	if uninstall {
		if start < 0 {
			return plan, nil
		}
		plan.Remove = append([]string(nil), lines[start+1:end]...)
		next := append([]string(nil), lines[:start]...)
		next = append(next, lines[end:]...)
		plan.after = joinIgnoreLines(next)
	} else {
		present := map[string]bool{}
		for _, line := range lines {
			present[line] = true
		}
		for _, want := range GlobalIgnoreLines {
			if !present[want] {
				plan.Add = append(plan.Add, want)
			}
		}
		if len(plan.Add) == 0 {
			return plan, nil
		}
		var next []string
		if start < 0 {
			next = append(append([]string(nil), lines...), GlobalIgnoreMarker)
			next = append(next, plan.Add...)
		} else {
			next = append(append([]string(nil), lines[:end]...), plan.Add...)
			next = append(next, lines[end:]...)
		}
		plan.after = joinIgnoreLines(next)
	}
	plan.Changed = !bytes.Equal(plan.before, plan.after)
	return plan, nil
}

// Apply writes the planned content only if the file still holds the content
// the plan was made from.
func (p GlobalIgnorePlan) Apply() error {
	if !p.Changed {
		return nil
	}
	current, _, err := readGlobalIgnore(p.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, p.before) {
		return fmt.Errorf("%s changed since it was planned; run again", p.Path)
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.Path), "."+filepath.Base(p.Path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(p.after); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), p.perm); err != nil {
		return err
	}
	current, _, err = readGlobalIgnore(p.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, p.before) {
		return fmt.Errorf("%s changed during save; run again", p.Path)
	}
	return os.Rename(tmp.Name(), p.Path)
}
