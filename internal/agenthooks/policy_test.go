package agenthooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadPolicyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	path, err := SavePolicy(dir, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != PresetGitHubHistoryGuard+".json" {
		t.Fatalf("path = %q", path)
	}
	if _, err := SavePolicy(dir, policy, false); err == nil {
		t.Fatal("second save without force should fail")
	}
	res := LoadPolicies(dir)
	if len(res.Errors) != 0 {
		t.Fatalf("load errors: %v", res.Errors)
	}
	if len(res.Policies) != 1 || res.Policies[0].ID != PresetGitHubHistoryGuard {
		t.Fatalf("policies = %+v", res.Policies)
	}
	rule := findRule(res.Policies[0], "deny-git-config-alias")
	if rule == nil || len(rule.Match.Contains) != 1 || !rule.Match.Contains[0].Fold {
		t.Fatalf("round-tripped alias rule = %+v, want folded glob", rule)
	}
	if mode := fileMode(t, path); mode != 0o600 {
		t.Fatalf("mode = %#o, want 0600", mode)
	}
}

func TestValidatePolicyRejectsInvalidArgPattern(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "bad",
		Enabled: true,
		Rules: []Rule{{
			ID:     "bad-rule",
			Effect: EffectDeny,
			Match:  Match{Argv: []ArgPattern{{Exact: "git", Type: "int"}}},
		}},
	}
	if err := ValidatePolicy(policy); err == nil {
		t.Fatal("invalid arg pattern should fail")
	}
}

func TestValidatePolicyRejectsInvalidGlob(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "bad-glob",
		Enabled: true,
		Rules: []Rule{{
			ID:     "bad-glob-rule",
			Effect: EffectDeny,
			Match:  Match{Argv: []ArgPattern{{Exact: "git"}, {Glob: "push["}}},
		}},
	}
	err := ValidatePolicy(policy)
	if err == nil {
		t.Fatal("invalid glob pattern should fail validation")
	}
	if !strings.Contains(err.Error(), "glob") {
		t.Fatalf("error = %v, want glob error", err)
	}
}

func TestValidatePolicyRejectsInvalidRisk(t *testing.T) {
	policy := Policy{
		Version: PolicyVersion,
		ID:      "bad-risk",
		Enabled: true,
		Rules: []Rule{{
			ID:     "bad-risk-rule",
			Effect: EffectDeny,
			Match:  Match{Argv: exactArgs("kill"), Risk: "unknown-risk"},
		}},
	}
	err := ValidatePolicy(policy)
	if err == nil {
		t.Fatal("invalid risk should fail validation")
	}
	if !strings.Contains(err.Error(), "risk") {
		t.Fatalf("error = %v, want risk error", err)
	}
}

func findRule(policy Policy, id string) *Rule {
	for i := range policy.Rules {
		if policy.Rules[i].ID == id {
			return &policy.Rules[i]
		}
	}
	return nil
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
