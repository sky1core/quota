package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sky1core/quota/internal/agentinstructions"
	"github.com/sky1core/quota/internal/overlayruntime"
)

type instructionAgentReport struct {
	Agent      string                          `json:"agent"`
	State      string                          `json:"state"`
	Delivery   string                          `json:"delivery"`
	Issues     []string                        `json:"issues,omitempty"`
	Repository string                          `json:"repositoryCheck,omitempty"`
	Native     *agentinstructions.NativeReport `json:"native,omitempty"`
}

type instructionsReport struct {
	Operation  string                           `json:"operation"`
	Directory  string                           `json:"directory,omitempty"`
	Scope      string                           `json:"scope,omitempty"`
	DryRun     bool                             `json:"dryRun,omitempty"`
	Plan       *agentinstructions.InstallPlan   `json:"plan,omitempty"`
	Repository []string                         `json:"repositoryChanges,omitempty"`
	Applied    *agentinstructions.InstallResult `json:"accountChanges,omitempty"`
	Agents     []instructionAgentReport         `json:"agents,omitempty"`
	Error      string                           `json:"error,omitempty"`
}

func printAgentInstructionsUsage(out io.Writer) {
	fmt.Fprint(out, `usage:
  quota-cli agent instructions setup [dir] [--agent=all|claude|codex] [--shared-source=checkout|primary] [--local-file=PATH ...] [--dry-run] [--json]
  quota-cli agent instructions status [dir] [--agent=all|claude|codex] [--json]
  quota-cli agent instructions verify [dir] [--agent=all|claude|codex] [--timeout=10m] [--json]
  quota-cli agent instructions uninstall [dir] --scope=account|repository [--agent=all|claude|codex] [--dry-run] [--json]

Manage delivery of AGENTS.md and AGENTS.local.md. verify uses paid CLI model calls.
`)
}

type instructionLocalFiles []string

func (files *instructionLocalFiles) String() string { return strings.Join(*files, ", ") }
func (files *instructionLocalFiles) Set(path string) error {
	if path == "" {
		return fmt.Errorf("local-file requires a nonempty relative path")
	}
	*files = append(*files, path)
	return nil
}

func instructionFlagOrder(fs *flag.FlagSet, args []string) ([]string, error) {
	var options, operands []string
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "help" || name == "h" {
			return []string{"--help"}, nil
		}
		entry := fs.Lookup(name)
		if entry == nil {
			return nil, fmt.Errorf("unknown option: %s", arg)
		}
		if seen[name] && name != "local-file" {
			return nil, fmt.Errorf("option repeated: --%s", name)
		}
		seen[name] = true
		options = append(options, arg)
		boolean, isBool := entry.Value.(interface{ IsBoolFlag() bool })
		if !inline && !(isBool && boolean.IsBoolFlag()) {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("option --%s requires a value", name)
			}
			options = append(options, args[i])
		}
	}
	return append(append(options, "--"), operands...), nil
}

func runAgentInstructions(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAgentInstructionsUsage(stderr)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "_hook" {
		return runInstructionsHook(ctx, args[1:], stdin, stdout, stderr)
	}
	operation := args[0]
	if operation != "setup" && operation != "status" && operation != "verify" && operation != "uninstall" {
		if operation == "--help" || operation == "-h" {
			printAgentInstructionsUsage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "unknown agent instructions command: %q\n", operation)
		printAgentInstructionsUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("quota-cli agent instructions "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAgentInstructionsUsage(stderr) }
	agent := fs.String("agent", "all", "Current CLI account: all, claude or codex")
	jsonOut := fs.Bool("json", false, "Output JSON")
	var dryRun bool
	var scope, sharedSource string
	var localFiles instructionLocalFiles
	timeout := 10 * time.Minute
	if operation == "setup" || operation == "uninstall" {
		fs.BoolVar(&dryRun, "dry-run", false, "Inspect changes without saving")
	}
	if operation == "setup" {
		fs.StringVar(&sharedSource, "shared-source", "", "Shared source: checkout or primary (omitted: keep repository policy)")
		fs.Var(&localFiles, "local-file", "Register an ignored primary-relative file to copy to linked worktrees; repeat for more files")
	}
	if operation == "uninstall" {
		fs.StringVar(&scope, "scope", "", "Required: account or repository")
	}
	if operation == "verify" {
		fs.DurationVar(&timeout, "timeout", timeout, "Total live verification timeout")
	}
	ordered, err := instructionFlagOrder(fs, args[1:])
	if err == nil {
		err = fs.Parse(ordered)
	}
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 1 || (*agent != "all" && *agent != "claude" && *agent != "codex") || timeout <= 0 || (sharedSource != "" && sharedSource != "checkout" && sharedSource != "primary") || (operation == "uninstall" && scope != "account" && scope != "repository") {
		printAgentInstructionsUsage(stderr)
		return 2
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report := instructionsReport{Operation: operation, Directory: dir, Scope: scope, DryRun: dryRun}
	finish := func(err error, failed bool) int {
		if err != nil {
			report.Error = err.Error()
			failed = true
		}
		if *jsonOut {
			return writeJSONWithCode(stdout, stderr, report, failed)
		}
		printInstructionsReport(stdout, report)
		if failed {
			return 1
		}
		return 0
	}
	if err := overlayruntime.ValidateGitEnvironment(ctx); err != nil {
		return finish(err, true)
	}
	executable, err := os.Executable()
	if err != nil {
		return finish(err, true)
	}
	targets, err := agentinstructions.CurrentInstallTargets()
	if err != nil {
		return finish(err, true)
	}
	installation, err := agentinstructions.NewInstallation(executable, targets)
	if err != nil {
		return finish(err, true)
	}
	agents := []string{*agent}
	if *agent == "all" {
		agents = []string{"claude", "codex"}
	}
	switch operation {
	case "setup":
		if *agent == "all" || *agent == "claude" {
			settings, err := overlayruntime.PlannedClaudeSettings(ctx, dir, *agent, sharedSource, localFiles...)
			if err != nil {
				return finish(err, true)
			}
			problems, err := installation.InspectPlannedClaudeRepositoryHooks(ctx, dir, settings)
			if err != nil {
				return finish(err, true)
			}
			if len(problems) > 0 {
				return finish(fmt.Errorf("%s", strings.Join(problems, "; ")), true)
			}
		}
		plan, err := installation.Plan(agents, false)
		report.Plan = &plan
		if err != nil {
			return finish(err, true)
		}
		var hookRemovals []string
		for _, change := range plan.Changes {
			if change.Agent == "codex" && change.Changed {
				hookRemovals = append(hookRemovals, change.Path)
			}
		}
		accountPaths := make([]string, 0, len(plan.Changes))
		for _, change := range plan.Changes {
			accountPaths = append(accountPaths, change.Path)
		}
		var documents overlayruntime.CodexSetupPlan
		var validated overlayruntime.ValidatedCodexSetup
		nativeCodex := *agent == "all" || *agent == "codex"
		if nativeCodex {
			documents, report.Repository, err = overlayruntime.PlanNativeCodexRepository(ctx, dir, *agent, sharedSource, hookRemovals, accountPaths, localFiles...)
		} else {
			report.Repository, err = overlayruntime.PlanRepositoryWithCodexHookRemovals(ctx, dir, *agent, sharedSource, hookRemovals, localFiles...)
		}
		if err != nil {
			return finish(err, true)
		}
		if err := overlayruntime.CheckRepositoryAccountDestinations(ctx, dir, *agent, sharedSource, accountPaths, localFiles...); err != nil {
			return finish(err, true)
		}
		if nativeCodex {
			validated, err = agentinstructions.PreflightNativeCodex(ctx, documents)
			if err != nil {
				return finish(err, true)
			}
		}
		if dryRun {
			return finish(nil, false)
		}
		applyAccount := func() error {
			result, err := installation.Apply(plan)
			report.Applied = &result
			return err
		}
		if nativeCodex {
			err = validated.Apply(ctx, applyAccount)
		} else {
			err = overlayruntime.SetupRepositoryWithCodexHookRemovals(ctx, dir, *agent, sharedSource, hookRemovals, localFiles...)
			if err == nil {
				err = applyAccount()
			}
		}
		if err != nil {
			return finish(err, true)
		}
	case "uninstall":
		if scope == "account" {
			plan, err := installation.Plan(agents, true)
			report.Plan = &plan
			if err != nil {
				return finish(err, true)
			}
			if dryRun {
				return finish(nil, false)
			}
			result, err := installation.Apply(plan)
			report.Applied = &result
			return finish(err, err != nil)
		}
		if _, err := overlayruntime.ReadRepositoryState(ctx, dir); err != nil {
			return finish(err, true)
		}
		if dryRun {
			report.Repository = []string{"Disable instruction delivery for " + *agent + " in this Git worktree group; preserve source and user changes"}
			if err := overlayruntime.ValidateRepositoryUninstall(ctx, dir, *agent); err != nil {
				return finish(err, true)
			}
			return finish(nil, false)
		}
		if err := overlayruntime.UninstallRepository(ctx, dir, *agent); err != nil {
			return finish(err, true)
		}
		return finish(nil, false)
	}
	statuses, failed := inspectInstructions(ctx, installation, dir, agents)
	report.Agents = statuses
	if operation != "verify" || failed {
		return finish(nil, failed)
	}
	ctx, deadline := context.WithTimeout(ctx, timeout)
	defer deadline()
	for n := range report.Agents {
		entry := &report.Agents[n]
		if err := agentinstructions.VerifyCanary(ctx, entry.Agent); err != nil {
			entry.Delivery = "failed"
			entry.Issues = append(entry.Issues, err.Error())
			failed = true
		} else {
			entry.Delivery = "verified-new-main-session"
		}
	}
	return finish(nil, failed)
}

func inspectInstructions(ctx context.Context, installation *agentinstructions.Installation, dir string, agents []string) ([]instructionAgentReport, bool) {
	var reports []instructionAgentReport
	statuses, err := installation.Inspect(agents)
	if err != nil {
		return []instructionAgentReport{{State: "unknown", Delivery: "not-verified", Issues: []string{err.Error()}}}, true
	}
	failed := false
	state, stateErr := overlayruntime.ReadRepositoryState(ctx, dir)
	for _, status := range statuses {
		r := instructionAgentReport{Agent: status.Agent, State: "configured", Delivery: "not-verified", Issues: append([]string(nil), status.Problems...)}
		if !status.Configured {
			r.State = "blocked"
		}
		if stateErr != nil {
			r.State = "blocked"
			r.Issues = append(r.Issues, stateErr.Error())
		} else if state.Disabled[status.Agent] {
			r.State = "disabled"
			r.Issues = append(r.Issues, "Instruction delivery is disabled for this repository")
		}
		var activeHookFiles map[string][]string
		if status.Agent == "codex" {
			native, err := agentinstructions.InspectNativeCodex(ctx, dir, installation.ExpectedCodexHooks())
			r.Native = &native
			if r.State == "configured" && (err != nil || native.State != "configured") {
				r.State = native.State
			}
			if err != nil {
				r.Issues = append(r.Issues, err.Error())
			} else {
				documents, planErr := overlayruntime.PlanCodexDocuments(ctx, dir, "codex", "")
				if planErr == nil {
					var validated overlayruntime.ValidatedCodexSetup
					validated, planErr = agentinstructions.PreflightNativeCodex(ctx, documents)
					activeHookFiles = validated.ActiveHookFiles
				}
				if planErr != nil {
					var discoveryErr *agentinstructions.NativeCodexDiscoveryError
					if errors.As(planErr, &discoveryErr) {
						r.Native.State = "unknown"
						if r.State != "blocked" && r.State != "disabled" {
							r.State = "unknown"
						}
					} else {
						r.State = "blocked"
					}
					r.Issues = append(r.Issues, planErr.Error())
				}
			}
		}
		var output bytes.Buffer
		var code int
		if status.Agent == "codex" {
			var err error
			code, err = overlayruntime.CheckRepositoryWithNativeCodexSettings(ctx, dir, activeHookFiles, &output)
			if err != nil {
				fmt.Fprintln(&output, err)
			}
		} else {
			code = overlayruntime.Run(ctx, []string{"check", "--runtime=" + status.Agent, dir}, nil, &output, &output)
		}
		if code != 0 {
			r.State = "blocked"
			r.Issues = append(r.Issues, "Repository instruction checks failed")
		}
		r.Repository = strings.TrimSpace(output.String())
		if status.Agent == "claude" {
			problems, err := installation.InspectClaudeRepositoryHooks(ctx, dir)
			if err != nil {
				problems = append(problems, err.Error())
			}
			if len(problems) > 0 {
				r.State = "blocked"
				r.Issues = append(r.Issues, problems...)
			}
		}

		if r.State != "configured" {
			failed = true
		}
		reports = append(reports, r)
	}
	return reports, failed
}

func printInstructionsReport(out io.Writer, r instructionsReport) {
	fmt.Fprintf(out, "instructions %s: %s\n", r.Operation, r.Directory)
	if r.Scope != "" {
		fmt.Fprintf(out, "scope: %s\n", r.Scope)
	}
	if r.DryRun {
		fmt.Fprintln(out, "dry-run: no source, managed, or account configuration files changed; provider startup may update runtime caches")
	}
	if r.Plan != nil {
		for _, change := range r.Plan.Changes {
			fmt.Fprintf(out, "%s: %s: %s (change=%t)\n", change.Agent, change.Operation, change.Path, change.Changed)
		}
	}
	for _, change := range r.Repository {
		fmt.Fprintln(out, change)
	}
	if r.Applied != nil {
		for _, path := range r.Applied.Applied {
			fmt.Fprintln(out, "saved:", path)
		}
	}
	for _, agent := range r.Agents {
		fmt.Fprintf(out, "%s: %s; delivery=%s\n", agent.Agent, agent.State, agent.Delivery)
		for _, issue := range agent.Issues {
			fmt.Fprintln(out, "  "+issue)
		}
		if agent.Repository != "" {
			fmt.Fprintln(out, agent.Repository)
		}
		if agent.Native != nil {
			for _, issue := range agent.Native.Issues {
				fmt.Fprintln(out, "  "+issue)
			}
		}
	}
	if r.Error != "" {
		fmt.Fprintln(out, "error:", r.Error)
	}
}

func runInstructionsHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("quota-cli agent instructions _hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "Agent")
	event := fs.String("event", "", "Event")
	ordered, err := instructionFlagOrder(fs, args)
	if err == nil {
		err = fs.Parse(ordered)
	}
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "instruction hooks do not accept positional arguments")
		return 2
	}
	var command []string
	switch {
	case *agent == "claude" && *event == "SessionStart":
		command = []string{"json", *event, "CLAUDE.md", "CLAUDE.local.md", ".", "claude-session"}
	case *agent == "claude" && *event == "WorktreeCreate":
		if err := overlayruntime.CreateWorktreeWithNativeCodexPreflight(ctx, stdin, stdout, agentinstructions.PreflightNativeCodex); err != nil {
			fmt.Fprintf(stderr, "quota-cli agent instructions: %v\n", err)
			return 1
		}
		return 0
	case *agent == "claude" && *event == "WorktreeRemove":
		command = []string{"claude-worktree-remove"}
	case *agent == "codex" && (*event == "SessionStart" || *event == "SubagentStart"):
		policy := "codex-session"
		if *event == "SubagentStart" {
			policy = "codex-subagent"
		}
		command = []string{"json", *event, "AGENTS.md", "-", ".", policy}
	default:
		fmt.Fprintln(stderr, "unsupported instruction agent/event combination")
		return 2
	}
	return overlayruntime.Run(ctx, command, stdin, stdout, stderr)
}
