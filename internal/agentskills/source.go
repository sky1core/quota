package agentskills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/childprocess"
)

type FetchedSkill struct {
	Name string
	Dir  string
}

const (
	skillsPackage = "skills@1.7.0"
	fetchAgent    = "codex"
	wantAgent     = "Codex"
	wantScope     = "project"
	wantMode      = "copy"
	nodeMajor     = 22
	nodeMinor     = 20
	nodePatch     = 0
)

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is empty")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("skill name %q is not valid UTF-8", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("skill name %q is not a valid path component", name)
	}
	if name[0] == '-' {
		return fmt.Errorf("skill name %q must not begin with '-'", name)
	}
	if name[0] == '.' {
		return fmt.Errorf("skill name %q must not begin with '.'", name)
	}
	for _, r := range name {
		if r == '/' || r == '\\' {
			return fmt.Errorf("skill name %q must not contain a path separator", name)
		}
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("skill name %q must not contain control characters", name)
		}
	}
	return nil
}

type addResult struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Path   string   `json:"path"`
	Scope  string   `json:"scope"`
	Agents []string `json:"agents"`
	Mode   string   `json:"mode"`
	Error  string   `json:"error"`
	Reason string   `json:"reason"`
}

func Fetch(ctx context.Context, source, name string) ([]FetchedSkill, func(), error) {
	if source == "" {
		return nil, nil, fmt.Errorf("source is empty")
	}
	if strings.HasPrefix(source, "-") {
		return nil, nil, fmt.Errorf("source %q must not begin with '-'", source)
	}
	selector := "*"
	if name != "" {
		if err := ValidateName(name); err != nil {
			return nil, nil, err
		}
		selector = name
	}
	if strings.HasPrefix(source, ".") {
		abs, err := filepath.Abs(source)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve source %q: %w", source, err)
		}
		source = abs
	}

	if err := verifyNode(ctx); err != nil {
		return nil, nil, err
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return nil, nil, fmt.Errorf("npm is required but was not found in PATH: %w", err)
	}

	project, err := os.MkdirTemp("", "quota-skills-")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary project: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(project) }

	skills, err := runFetch(ctx, project, source, selector)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return skills, cleanup, nil
}

func runFetch(ctx context.Context, project, source, selector string) ([]FetchedSkill, error) {
	args := []string{
		"exec", "--", skillsPackage,
		"add", source,
		"--agent", fetchAgent,
		"--skill", selector,
		"--copy", "--yes", "--json", "--full-depth",
	}
	cmd := childprocess.CommandContext(ctx, "npm", args...)
	cmd.Dir = project
	cmd.Env = append(os.Environ(),
		"DO_NOT_TRACK=1",
		"DISABLE_TELEMETRY=1",
		"npm_config_yes=true",
		"npm_config_audit=false",
		"npm_config_fund=false",
		"npm_config_update_notifier=false",
		"npm_config_ignore_scripts=true",
		"GIT_TERMINAL_PROMPT=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := childprocess.Run(cmd)

	var results []addResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &results); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("skills add failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("parse skills add JSON output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("skills add reported no results")
	}
	if selector != "*" && len(results) != 1 {
		return nil, fmt.Errorf("skills add returned multiple skills for a single selection")
	}

	realProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		return nil, fmt.Errorf("resolve temporary project: %w", err)
	}
	skillsRoot := filepath.Join(realProject, ".agents/skills")

	skills := make([]FetchedSkill, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		if r.Status != "installed" {
			return nil, fmt.Errorf("skill %q not installed (status %q): %s%s", r.Name, r.Status, r.Error, r.Reason)
		}
		if len(r.Agents) != 1 || r.Agents[0] != wantAgent {
			return nil, fmt.Errorf("skill %q installed for unexpected agents %v", r.Name, r.Agents)
		}
		if r.Scope != wantScope {
			return nil, fmt.Errorf("skill %q has unexpected scope %q", r.Name, r.Scope)
		}
		if r.Mode != wantMode {
			return nil, fmt.Errorf("skill %q has unexpected mode %q", r.Name, r.Mode)
		}
		if r.Path == "" {
			return nil, fmt.Errorf("skill %q has no path", r.Name)
		}
		dir, err := filepath.EvalSymlinks(r.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve skill path %q: %w", r.Path, err)
		}
		rel, err := filepath.Rel(skillsRoot, dir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsRune(rel, filepath.Separator) {
			return nil, fmt.Errorf("skill path %q is not directly under %q", r.Path, skillsRoot)
		}
		if err := ValidateName(rel); err != nil {
			return nil, err
		}
		if seen[rel] {
			return nil, fmt.Errorf("duplicate skill directory %q", rel)
		}
		seen[rel] = true
		info, err := os.Lstat(filepath.Join(dir, "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("skill %q has no SKILL.md", rel)
		}
		skills = append(skills, FetchedSkill{Name: rel, Dir: dir})
	}
	if runErr != nil {
		return nil, fmt.Errorf("skills add failed despite reported results: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return skills, nil
}

func verifyNode(ctx context.Context) error {
	cmd := childprocess.CommandContext(ctx, "node", "--version")
	out, err := childprocess.Output(cmd)
	if err != nil {
		return fmt.Errorf("node is required but could not be run: %w", err)
	}
	major, minor, patch, err := parseNodeVersion(string(out))
	if err != nil {
		return err
	}
	if major < nodeMajor ||
		(major == nodeMajor && minor < nodeMinor) ||
		(major == nodeMajor && minor == nodeMinor && patch < nodePatch) {
		return fmt.Errorf("node %d.%d.%d is too old; skills requires >= %d.%d.%d",
			major, minor, patch, nodeMajor, nodeMinor, nodePatch)
	}
	return nil
}

func parseNodeVersion(s string) (int, int, int, error) {
	v := strings.TrimSpace(s)
	fields := regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+][0-9A-Za-z.-]+)?$`).FindStringSubmatch(v)
	if fields == nil {
		return 0, 0, 0, fmt.Errorf("cannot parse node version %q", strings.TrimSpace(s))
	}
	major, err1 := strconv.Atoi(fields[1])
	minor, err2 := strconv.Atoi(fields[2])
	patch, err3 := strconv.Atoi(fields[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, fmt.Errorf("cannot parse node version %q", strings.TrimSpace(s))
	}
	return major, minor, patch, nil
}
