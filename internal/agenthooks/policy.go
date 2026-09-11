package agenthooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sky1core/quota/internal/atomicfile"
)

const (
	PolicyVersion = 1

	EffectAllow = "allow"
	EffectDeny  = "deny"

	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

type Policy struct {
	Version     int        `json:"version"`
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Enabled     bool       `json:"enabled"`
	Rules       []Rule     `json:"rules"`
	Tests       []TestCase `json:"tests,omitempty"`
	Path        string     `json:"-"`
}

type Rule struct {
	ID      string  `json:"id"`
	Effect  string  `json:"effect"`
	Match   Match   `json:"match"`
	Except  []Match `json:"except,omitempty"`
	Message string  `json:"message,omitempty"`
}

type Match struct {
	Argv     []ArgPattern `json:"argv,omitempty"`
	Exact    bool         `json:"exact,omitempty"`
	Contains []ArgPattern `json:"contains,omitempty"`
	HasFlag  []string     `json:"hasFlag,omitempty"`
}

type ArgPattern struct {
	Exact string `json:"exact,omitempty"`
	Type  string `json:"type,omitempty"`
	Glob  string `json:"glob,omitempty"`
	Fold  bool   `json:"fold,omitempty"`
}

type TestCase struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Want    string `json:"want"`
	RuleID  string `json:"ruleId,omitempty"`
}

type LoadResult struct {
	Policies []Policy
	Errors   []error
}

func DefaultPolicyDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "agent-hooks.d")
}

func LoadPolicies(dir string) LoadResult {
	if dir == "" {
		dir = DefaultPolicyDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadResult{}
		}
		return LoadResult{Errors: []error{err}}
	}
	var res LoadResult
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", path, err))
			continue
		}
		var policy Policy
		if err := json.Unmarshal(b, &policy); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", path, err))
			continue
		}
		policy.Path = path
		if err := ValidatePolicy(policy); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", path, err))
			continue
		}
		res.Policies = append(res.Policies, policy)
	}
	sort.Slice(res.Policies, func(i, j int) bool {
		return res.Policies[i].ID < res.Policies[j].ID
	})
	return res
}

func SavePolicy(dir string, policy Policy, force bool) (string, error) {
	if dir == "" {
		dir = DefaultPolicyDir()
	}
	if err := ValidatePolicy(policy); err != nil {
		return "", err
	}
	path := filepath.Join(dir, policy.ID+".json")
	policy.Path = ""
	b, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	if err := atomicfile.Save(path, b, 0o600, force); err != nil {
		if errors.Is(err, atomicfile.ErrExists) {
			return "", fmt.Errorf("%s already exists", path)
		}
		return "", err
	}
	return path, nil
}

func ValidatePolicy(policy Policy) error {
	if policy.Version != PolicyVersion {
		return fmt.Errorf("unsupported policy version %d", policy.Version)
	}
	if strings.TrimSpace(policy.ID) == "" {
		return errors.New("policy id is required")
	}
	if strings.ContainsAny(policy.ID, `/\`) || policy.ID == "." || policy.ID == ".." {
		return fmt.Errorf("policy id %q is not a file-safe id", policy.ID)
	}
	seen := map[string]bool{}
	for _, rule := range policy.Rules {
		if strings.TrimSpace(rule.ID) == "" {
			return fmt.Errorf("policy %s has a rule with an empty id", policy.ID)
		}
		if seen[rule.ID] {
			return fmt.Errorf("policy %s has duplicate rule id %q", policy.ID, rule.ID)
		}
		seen[rule.ID] = true
		switch rule.Effect {
		case EffectAllow, EffectDeny:
		default:
			return fmt.Errorf("rule %s has invalid effect %q", rule.ID, rule.Effect)
		}
		if err := validateMatch(rule.Match); err != nil {
			return fmt.Errorf("rule %s match: %w", rule.ID, err)
		}
		for i, except := range rule.Except {
			if err := validateMatch(except); err != nil {
				return fmt.Errorf("rule %s except %d: %w", rule.ID, i+1, err)
			}
		}
	}
	for _, test := range policy.Tests {
		if strings.TrimSpace(test.Name) == "" {
			return fmt.Errorf("policy %s has a test with an empty name", policy.ID)
		}
		if strings.TrimSpace(test.Command) == "" {
			return fmt.Errorf("test %s has an empty command", test.Name)
		}
		switch test.Want {
		case DecisionAllow, DecisionDeny:
		default:
			return fmt.Errorf("test %s has invalid want %q", test.Name, test.Want)
		}
	}
	return nil
}

func validateMatch(match Match) error {
	if len(match.Argv) == 0 && len(match.Contains) == 0 && len(match.HasFlag) == 0 {
		return errors.New("at least one matcher is required")
	}
	for _, p := range append(append([]ArgPattern{}, match.Argv...), match.Contains...) {
		if err := validateArgPattern(p); err != nil {
			return err
		}
	}
	for _, flag := range match.HasFlag {
		if !strings.HasPrefix(flag, "-") {
			return fmt.Errorf("flag %q must start with '-'", flag)
		}
	}
	return nil
}

func validateArgPattern(p ArgPattern) error {
	count := 0
	if p.Exact != "" {
		count++
	}
	if p.Type != "" {
		count++
		switch p.Type {
		case "int", "nonempty":
		default:
			return fmt.Errorf("unsupported arg type %q", p.Type)
		}
	}
	if p.Glob != "" {
		count++
		if _, err := path.Match(p.Glob, ""); err != nil {
			return fmt.Errorf("invalid glob %q: %w", p.Glob, err)
		}
	}
	if count != 1 {
		return fmt.Errorf("arg pattern must set exactly one of exact, type, glob")
	}
	if p.Fold && p.Type != "" {
		return errors.New("fold can only be used with exact or glob")
	}
	return nil
}

func (p *ArgPattern) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*p = ArgPattern{Exact: s}
		return nil
	}
	type raw ArgPattern
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*p = ArgPattern(r)
	return nil
}

func (p ArgPattern) MarshalJSON() ([]byte, error) {
	if p.Exact != "" && p.Type == "" && p.Glob == "" && !p.Fold {
		return json.Marshal(p.Exact)
	}
	type raw ArgPattern
	return json.Marshal(raw(p))
}
