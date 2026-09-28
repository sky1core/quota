package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/agentinstructions"
)

func inspectAgentHookActivation(ctx context.Context, plan *agenthooks.HookPlan) {
	if plan.Runtime != "codex" {
		return
	}
	plan.Activation = "unknown"
	if plan.Error != "" {
		return
	}
	if !plan.Present {
		plan.Activation = "missing"
		return
	}
	if len(plan.Reasons) > 0 {
		plan.Activation = "blocked"
		return
	}
	if _, err := agenthooks.ResolveHookBinary(plan.Binary); err != nil {
		plan.Error = err.Error()
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		plan.Error = err.Error()
		return
	}
	report, err := agentinstructions.InspectNativeCodexHooksForHome(ctx, cwd, filepath.Dir(plan.Path), agentinstructions.NativeExpectations{Commands: map[string]string{"preToolUse": plan.Command}})
	if err != nil {
		plan.Reasons = append(plan.Reasons, fmt.Sprintf("Codex hook activation could not be verified: %v; check this account's Codex CLI and /hooks, then run agent hooks doctor again", err))
		return
	}
	if report.State == "configured" {
		plan.Warnings = append(plan.Warnings, report.Issues...)
	} else {
		plan.Reasons = append(plan.Reasons, report.Issues...)
	}
	for _, hook := range report.Hooks {
		if !hook.Matched {
			continue
		}
		actual, actualErr := os.Stat(hook.SourcePath)
		expected, expectedErr := os.Stat(plan.Path)
		if actualErr != nil || expectedErr != nil || !os.SameFile(actual, expected) {
			plan.Reasons = append(plan.Reasons, "Codex discovered the evaluator from a different or unverifiable hook file; review this account's /hooks sources")
			return
		}
	}
	switch report.State {
	case "configured":
		if len(plan.Reasons) == 0 {
			plan.Activation = "ready"
		}
	case "needs-trust":
		plan.Activation = "needs-trust"
		plan.Reasons = append(plan.Reasons, "review and trust this evaluator in the selected Codex account's /hooks, then run agent hooks doctor again")
	case "blocked":
		plan.Activation = "blocked"
		plan.Reasons = append(plan.Reasons, "Codex evaluator is disabled, missing, or conflicting; review this account's /hooks and enable the intended hook, then run agent hooks doctor again")
	default:
		plan.Reasons = append(plan.Reasons, "Codex returned an unknown hook activation state; check this account's /hooks and run agent hooks doctor again")
	}
}
