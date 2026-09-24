package overlayruntime

import (
	"context"
	"fmt"
	"strings"
)

const claudeHookContextLimit = 10000

type RepositoryStatus struct {
	Checkout          string   `json:"checkout"`
	Primary           string   `json:"primary"`
	LocalInstructions string   `json:"localInstructions"`
	LocalPresent      bool     `json:"localPresent"`
	Problems          []string `json:"problems,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

type CheckOptions struct {
	ClaudeConfigDir string
}

func CheckRepository(ctx context.Context, dir, agent string, options CheckOptions) (RepositoryStatus, error) {
	var status RepositoryStatus
	if agent != "claude" && agent != "codex" {
		return status, fmt.Errorf("invalid agent %q", agent)
	}
	if err := ValidateGitEnvironment(ctx); err != nil {
		return status, err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return status, err
	}
	status.Checkout, status.Primary, status.LocalInstructions = r.Top, r.Root, r.localSource()
	body, notice, present, err := readLocalInstructions(r.localSource())
	if err != nil {
		return status, err
	}
	status.LocalPresent = present
	if notice != "" {
		status.Problems = append(status.Problems, notice)
	} else if agent == "claude" && exceedsClaudeHookLimit(sessionStartContext(body, nil)) {
		status.Problems = append(status.Problems, fmt.Sprintf("AGENTS.local.md exceeds Claude's %d UTF-16-unit hook limit; SessionStart omits the body and UserPromptSubmit asks the agent to report the failure and defer work", claudeHookContextLimit))
	}
	record, remaining, err := r.legacyRemnants()
	if err != nil {
		status.Problems = append(status.Problems, err.Error())
	} else if record != "" {
		warning := "previous generated files are recorded in " + record + "; the next session start removes the ones that still match"
		if len(remaining) > 0 {
			warning += ": " + strings.Join(remaining, ", ")
		}
		status.Warnings = append(status.Warnings, warning)
	}
	if agent == "claude" {
		for _, file := range claudeProjectInstructionFiles(r.Start, options.ClaudeConfigDir) {
			status.Warnings = append(status.Warnings, "native AGENTS.md reading is blocked by "+file)
		}
		mode, settings, err := claudeAccountInstructionMode(options.ClaudeConfigDir)
		switch {
		case err != nil:
			status.Problems = append(status.Problems, err.Error())
		case mode == claudeModeClaudeOnly:
			status.Warnings = append(status.Warnings, settings+": project instruction mode claude-md does not read AGENTS.md natively")
		case mode == claudeModeManagedOnly:
			status.Warnings = append(status.Warnings, settings+": project instruction mode managed-only drops project instruction files")
		}
	}
	return status, nil
}

func exceedsClaudeHookLimit(content string) bool {
	units := 0
	for _, r := range content {
		units++
		if r >= 0x10000 {
			units++
		}
		if units > claudeHookContextLimit {
			return true
		}
	}
	return false
}
