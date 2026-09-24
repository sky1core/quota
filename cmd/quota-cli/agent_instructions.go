package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sky1core/quota/internal/agentinstructions"
	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/overlayruntime"
)

type instructionAgentReport struct {
	Agent      string                           `json:"agent"`
	Account    string                           `json:"account,omitempty"`
	State      string                           `json:"state"`
	Delivery   string                           `json:"delivery"`
	Issues     []string                         `json:"issues,omitempty"`
	Repository *overlayruntime.RepositoryStatus `json:"repository,omitempty"`
	Native     *agentinstructions.NativeReport  `json:"native,omitempty"`
}

type instructionsReport struct {
	Operation string                           `json:"operation"`
	Directory string                           `json:"directory,omitempty"`
	DryRun    bool                             `json:"dryRun,omitempty"`
	Plan      *agentinstructions.InstallPlan   `json:"plan,omitempty"`
	Applied   *agentinstructions.InstallResult `json:"accountChanges,omitempty"`
	Agents    []instructionAgentReport         `json:"agents,omitempty"`
	Notices   []string                         `json:"notices,omitempty"`
	Error     string                           `json:"error,omitempty"`
}

func printAgentInstructionsUsage(out io.Writer) {
	fmt.Fprint(out, `usage:
  quota-cli agent instructions setup [--agent=all|claude|codex] [--dry-run] [--json]
  quota-cli agent instructions uninstall [--agent=all|claude|codex] [--dry-run] [--json]
  quota-cli agent instructions status [dir] [--agent=all|claude|codex] [--json]

Install session-start hooks that deliver the primary AGENTS.local.md of the
current Git repository to Claude Code and Codex CLI. AGENTS.md is read natively.
`)
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
		if seen[name] {
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
	operation := args[0]
	switch operation {
	case "_prepare":
		return runInstructionsPrepare(ctx, args[1:], stdin, stdout, stderr)
	case "--help", "-h":
		printAgentInstructionsUsage(stdout)
		return 0
	case "setup", "uninstall", "status":
	default:
		fmt.Fprintf(stderr, "unknown agent instructions command: %q\n", operation)
		printAgentInstructionsUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("quota-cli agent instructions "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAgentInstructionsUsage(stderr) }
	jsonOut := fs.Bool("json", false, "Output JSON")
	agent := fs.String("agent", "all", "Agent runtime: all, claude or codex")
	var dryRun bool
	if operation != "status" {
		fs.BoolVar(&dryRun, "dry-run", false, "Print the plan without saving")
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
	if *agent != "all" && *agent != "claude" && *agent != "codex" {
		printAgentInstructionsUsage(stderr)
		return 2
	}
	report := instructionsReport{Operation: operation, DryRun: dryRun}
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
	dir := ""
	if operation == "status" {
		if fs.NArg() > 1 {
			printAgentInstructionsUsage(stderr)
			return 2
		}
		dir = "."
		if fs.NArg() == 1 {
			dir = fs.Arg(0)
		}
		dir, err = filepath.Abs(dir)
		if err != nil {
			return finish(err, true)
		}
		report.Directory = dir
	} else if fs.NArg() != 0 {
		printAgentInstructionsUsage(stderr)
		return 2
	}
	if err := overlayruntime.ValidateGitEnvironment(ctx); err != nil {
		return finish(err, true)
	}
	executable, err := os.Executable()
	if err != nil {
		return finish(err, true)
	}
	cfg, err := config.Load()
	if err != nil {
		return finish(err, true)
	}
	agents := []string{*agent}
	if *agent == "all" {
		agents = []string{"claude", "codex"}
	}
	installations, targetErrs := instructionInstallations(executable, cfg, agents)
	targetErr := errors.Join(targetErrs...)
	if targetErr != nil && (operation == "setup" || len(installations) == 0) {
		return finish(fmt.Errorf("%s", strings.Join(errorsAsStrings(targetErrs), "; ")), true)
	}
	if operation == "status" {
		statuses, failed := inspectInstructions(ctx, installations, dir)
		report.Agents = statuses
		return finish(targetErr, failed || targetErr != nil)
	}
	uninstall := operation == "uninstall"
	plans := map[int]agentinstructions.InstallPlan{}
	combined := agentinstructions.InstallPlan{Agents: agents, Uninstall: uninstall}
	for n, target := range installations {
		plan, err := target.installation.Plan([]string{target.agent}, uninstall)
		if err != nil {
			report.Plan = &combined
			return finish(err, true)
		}
		if !uninstall && target.agent == "codex" {
			cwd, err := os.Getwd()
			if err != nil {
				report.Plan = &combined
				return finish(err, true)
			}
			trustChange, err := target.installation.PlanCodexHookTrust(ctx, cwd, codexHookInstallChanged(plan))
			if err != nil {
				report.Plan = &combined
				return finish(err, true)
			}
			if trustChange != nil {
				trustChange.Account = target.account
				plan.Changes = append(plan.Changes, *trustChange)
			}
		}
		plans[n] = plan
		combined.Changes = append(combined.Changes, plan.Changes...)
	}
	report.Plan = &combined
	if dryRun {
		if !uninstall {
			for _, target := range installations {
				if target.agent == "claude" {
					report.Notices = append(report.Notices, target.account+": will run claude --init-only after hook installation")
				}
			}
		}
		return finish(targetErr, targetErr != nil)
	}
	result := agentinstructions.InstallResult{}
	report.Applied = &result
	for n, target := range installations {
		applied, err := target.installation.Apply(plans[n])
		report.Applied.Applied = append(report.Applied.Applied, applied.Applied...)
		report.Applied.Unchanged = append(report.Applied.Unchanged, applied.Unchanged...)
		report.Applied.FailedPath = applied.FailedPath
		if err != nil {
			return finish(err, true)
		}
		if !uninstall && target.agent == "codex" {
			cwd, err := os.Getwd()
			if err != nil {
				return finish(err, true)
			}
			trust, err := target.installation.SyncCodexHookTrust(ctx, cwd)
			if err != nil {
				report.Applied.FailedPath = trust.Path
				return finish(err, true)
			}
			if trust.Changed {
				report.Applied.Unchanged = removePath(report.Applied.Unchanged, trust.Path)
				report.Applied.Applied = appendUniquePath(report.Applied.Applied, trust.Path)
			} else if !trust.Skipped && !hasPath(report.Applied.Applied, trust.Path) {
				report.Applied.Unchanged = appendUniquePath(report.Applied.Unchanged, trust.Path)
			}
		}
	}
	if !uninstall {
		for _, target := range installations {
			if target.agent != "claude" {
				continue
			}
			err := initializeClaudeInstructions(ctx, target.configDir)
			if ctx.Err() != nil {
				return finish(ctx.Err(), true)
			}
			if err != nil {
				report.Notices = append(report.Notices, fmt.Sprintf("WARN %s: claude --init-only failed: %v; hooks remain installed. Check Claude login/network and rerun setup --agent=claude.", target.account, err))
			} else {
				report.Notices = append(report.Notices, target.account+": claude --init-only completed; instruction delivery is not verified")
			}
		}
	}
	return finish(targetErr, targetErr != nil)
}

func initializeClaudeInstructions(ctx context.Context, configDir string) error {
	bin, err := claude.FindBinary()
	if err != nil {
		return err
	}
	dir := filepath.Dir(config.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--init-only")
	cmd.Dir = dir
	cmd.Env = claude.EnvForConfigDir(cmd.Environ(), configDir)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	return cmd.Run()
}

type instructionInstallation struct {
	agent        string
	account      string
	configDir    string
	home         string
	installation *agentinstructions.Installation
}

func instructionInstallations(executable string, cfg config.Config, agents []string) ([]instructionInstallation, []error) {
	var out []instructionInstallation
	var errs []error
	include := func(agent string) bool { return instructionAgentsInclude(agents, agent) }
	if include("claude") {
		accounts, skipped := cfg.ResolveAccounts()
		for _, msg := range skipped {
			errs = append(errs, fmt.Errorf("%s", msg))
		}
		for _, account := range accounts {
			targets, err := agentinstructions.InstallTargetsForAccounts(account.Key, account.ConfigDir, "", "")
			if err != nil {
				errs = append(errs, err)
				continue
			}
			installation, err := agentinstructions.NewInstallation(executable, targets)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out = append(out, instructionInstallation{agent: "claude", account: account.Key, configDir: targets.ClaudeConfigDir, installation: installation})
		}
	}
	if include("codex") {
		accounts, skipped := cfg.ResolveCodexAccounts()
		for _, msg := range skipped {
			errs = append(errs, fmt.Errorf("%s", msg))
		}
		for _, account := range accounts {
			targets, err := agentinstructions.InstallTargetsForAccounts("", "", account.Key, account.Home)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			installation, err := agentinstructions.NewInstallation(executable, targets)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out = append(out, instructionInstallation{agent: "codex", account: account.Key, home: targets.CodexHome, installation: installation})
		}
	}
	return out, errs
}

func instructionAgentsInclude(agents []string, target string) bool {
	for _, agent := range agents {
		if agent == target {
			return true
		}
	}
	return false
}

func appendUniquePath(paths []string, path string) []string {
	if hasPath(paths, path) {
		return paths
	}
	return append(paths, path)
}

func hasPath(paths []string, path string) bool {
	for _, existing := range paths {
		if existing == path {
			return true
		}
	}
	return false
}

func removePath(paths []string, path string) []string {
	out := paths[:0]
	for _, existing := range paths {
		if existing != path {
			out = append(out, existing)
		}
	}
	return out
}

func codexHookInstallChanged(plan agentinstructions.InstallPlan) bool {
	for _, change := range plan.Changes {
		if change.Agent == "codex" && change.Operation == "install" && change.Changed {
			return true
		}
	}
	return false
}

func inspectInstructions(ctx context.Context, installations []instructionInstallation, dir string) ([]instructionAgentReport, bool) {
	var reports []instructionAgentReport
	failed := false
	for _, target := range installations {
		statuses, err := target.installation.Inspect([]string{target.agent})
		if err != nil {
			reports = append(reports, instructionAgentReport{Agent: target.agent, Account: target.account, State: "unknown", Delivery: "not-verified", Issues: []string{err.Error()}})
			failed = true
			continue
		}
		for _, status := range statuses {
			r := instructionAgentReport{Agent: status.Agent, Account: target.account, State: "configured", Delivery: "not-verified", Issues: append([]string(nil), status.Problems...)}
			if !status.Configured {
				r.State = "blocked"
			}
			repository, err := overlayruntime.CheckRepository(ctx, dir, status.Agent, overlayruntime.CheckOptions{ClaudeConfigDir: target.configDir})
			if err != nil {
				r.State = "blocked"
				r.Issues = append(r.Issues, err.Error())
			} else {
				r.Repository = &repository
				if len(repository.Problems) > 0 {
					r.State = "blocked"
				}
			}
			if status.Agent == "codex" {
				native, err := agentinstructions.InspectNativeCodexHooksForHome(ctx, dir, target.home, target.installation.ExpectedCodexHooks())
				r.Native = &native
				if err != nil {
					r.Issues = append(r.Issues, err.Error())
					if r.State == "configured" {
						r.State = "unknown"
					}
				} else if native.State != "configured" && r.State == "configured" {
					r.State = "blocked"
				}
			}
			r.Issues = uniqueIssues(r.Issues)
			if r.State != "configured" {
				failed = true
			}
			reports = append(reports, r)
		}
	}
	return reports, failed
}

func uniqueIssues(issues []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, issue := range issues {
		if !seen[issue] {
			seen[issue] = true
			out = append(out, issue)
		}
	}
	return out
}

func printInstructionsReport(out io.Writer, r instructionsReport) {
	fmt.Fprintf(out, "instructions %s", r.Operation)
	if r.Directory != "" {
		fmt.Fprintf(out, ": %s", r.Directory)
	}
	fmt.Fprintln(out)
	if r.DryRun {
		fmt.Fprintln(out, "dry-run: no account configuration changed")
	}
	if r.Plan != nil {
		for _, change := range r.Plan.Changes {
			account := change.Account
			if account == "" {
				account = change.Agent
			}
			fmt.Fprintf(out, "%s/%s: %s: %s (change=%t)\n", change.Agent, account, change.Operation, change.Path, change.Changed)
		}
	}
	if r.Applied != nil {
		for _, path := range r.Applied.Applied {
			fmt.Fprintln(out, "saved:", path)
		}
	}
	for _, notice := range r.Notices {
		fmt.Fprintln(out, notice)
	}
	for _, agent := range r.Agents {
		label := agent.Agent
		if agent.Account != "" && agent.Account != agent.Agent {
			label += "/" + agent.Account
		}
		fmt.Fprintf(out, "%s: %s; delivery=%s\n", label, agent.State, agent.Delivery)
		for _, issue := range agent.Issues {
			fmt.Fprintln(out, "  "+issue)
		}
		if agent.Repository != nil {
			if agent.Repository.LocalPresent {
				fmt.Fprintf(out, "  AGENTS.local.md: %s\n", agent.Repository.LocalInstructions)
			} else {
				fmt.Fprintf(out, "  AGENTS.local.md: %s (absent)\n", agent.Repository.LocalInstructions)
			}
			for _, problem := range agent.Repository.Problems {
				fmt.Fprintln(out, "  FAIL: "+problem)
			}
			for _, warning := range agent.Repository.Warnings {
				fmt.Fprintln(out, "  WARN: "+warning)
			}
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

func runInstructionsPrepare(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("quota-cli agent instructions _prepare", flag.ContinueOnError)
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
	if *event == "SessionStart" && (*agent == "claude" || *agent == "codex") {
		return overlayruntime.RunSessionStartHook(ctx, *agent, stdin, stdout, stderr)
	}
	if *event == "UserPromptSubmit" && *agent == "claude" {
		return overlayruntime.RunClaudePromptHook(ctx, stdin, stdout, stderr)
	}
	fmt.Fprintln(stderr, "unsupported instruction agent/event combination")
	return 2
}
