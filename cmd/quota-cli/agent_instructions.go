package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sky1core/quota/internal/agentinstructions"
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
	Operation    string                              `json:"operation"`
	Directory    string                              `json:"directory,omitempty"`
	DryRun       bool                                `json:"dryRun,omitempty"`
	Plan         *agentinstructions.InstallPlan      `json:"plan,omitempty"`
	GlobalIgnore *agentinstructions.GlobalIgnorePlan `json:"globalIgnore,omitempty"`
	Applied      *agentinstructions.InstallResult    `json:"accountChanges,omitempty"`
	LocalFiles   []string                            `json:"localFiles,omitempty"`
	Agents       []instructionAgentReport            `json:"agents,omitempty"`
	Error        string                              `json:"error,omitempty"`
}

func printAgentInstructionsUsage(out io.Writer) {
	fmt.Fprint(out, `usage:
  quota-cli agent instructions setup [--agent=all|claude|codex] [--dry-run] [--no-global-ignore] [--json]
  quota-cli agent instructions uninstall [--agent=all|claude|codex] [--dry-run] [--remove-global-ignore] [--json]
  quota-cli agent instructions status [dir] [--agent=all|claude|codex] [--json]
  quota-cli agent instructions local-file add|remove|list [dir] [PATH ...] [--json]

Deliver AGENTS.md and AGENTS.local.md to Claude Code and Codex CLI. Repositories are
prepared automatically at session start.
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
	case "setup", "uninstall", "status", "local-file":
	default:
		fmt.Fprintf(stderr, "unknown agent instructions command: %q\n", operation)
		printAgentInstructionsUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("quota-cli agent instructions "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAgentInstructionsUsage(stderr) }
	jsonOut := fs.Bool("json", false, "Output JSON")
	agent := "all"
	var dryRun, noGlobalIgnore, removeGlobalIgnore bool
	switch operation {
	case "setup":
		fs.StringVar(&agent, "agent", "all", "Agent runtime: all, claude or codex")
		fs.BoolVar(&dryRun, "dry-run", false, "Print the plan without saving")
		fs.BoolVar(&noGlobalIgnore, "no-global-ignore", false, "Do not add generated file names to the global git ignore file")
	case "uninstall":
		fs.StringVar(&agent, "agent", "all", "Agent runtime: all, claude or codex")
		fs.BoolVar(&dryRun, "dry-run", false, "Print the plan without saving")
		fs.BoolVar(&removeGlobalIgnore, "remove-global-ignore", false, "Also remove the quota-managed lines from the global git ignore file (requires --agent=all)")
	case "status":
		fs.StringVar(&agent, "agent", "all", "Agent runtime: all, claude or codex")
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
	if agent != "all" && agent != "claude" && agent != "codex" {
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
	if operation == "local-file" {
		return runInstructionsLocalFile(ctx, fs.Args(), &report, finish, stderr)
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
	agents := []string{agent}
	if agent == "all" {
		agents = []string{"claude", "codex"}
	}
	installations, targetErrs := instructionInstallations(executable, cfg, agents)
	targetErr := errors.Join(targetErrs...)
	if targetErr != nil && (operation == "setup" || len(installations) == 0) {
		return finish(fmt.Errorf("%s", strings.Join(errorsAsStrings(targetErrs), "; ")), true)
	}
	switch operation {
	case "setup", "uninstall":
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
		if removeGlobalIgnore && agent != "all" {
			return finish(fmt.Errorf("--remove-global-ignore requires --agent=all"), true)
		}
		manageIgnore := !noGlobalIgnore
		if uninstall {
			manageIgnore = removeGlobalIgnore
		}
		if manageIgnore {
			ignorePath, err := agentinstructions.GlobalIgnorePath(ctx)
			if err != nil {
				return finish(err, true)
			}
			ignorePlan, err := agentinstructions.PlanGlobalIgnore(ignorePath, uninstall)
			report.GlobalIgnore = &ignorePlan
			if err != nil {
				return finish(err, true)
			}
		}
		if dryRun {
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
		if manageIgnore {
			if err := report.GlobalIgnore.Apply(); err != nil {
				return finish(err, true)
			}
		}
		return finish(targetErr, targetErr != nil)
	}
	statuses, failed := inspectInstructions(ctx, installations, dir)
	report.Agents = statuses
	return finish(targetErr, failed || targetErr != nil)
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
			out = append(out, instructionInstallation{agent: "claude", account: account.Key, configDir: account.ConfigDir, installation: installation})
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
			out = append(out, instructionInstallation{agent: "codex", account: account.Key, home: account.Home, installation: installation})
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

func runInstructionsLocalFile(ctx context.Context, operands []string, report *instructionsReport, finish func(error, bool) int, stderr io.Writer) int {
	if len(operands) == 0 {
		printAgentInstructionsUsage(stderr)
		return 2
	}
	action, operands := operands[0], operands[1:]
	report.Operation = "local-file " + action
	dir := "."
	switch action {
	case "list":
		if len(operands) > 1 {
			printAgentInstructionsUsage(stderr)
			return 2
		}
		if len(operands) == 1 {
			dir = operands[0]
		}
	case "add", "remove":
		if len(operands) >= 2 {
			if info, err := os.Stat(operands[0]); err == nil && info.IsDir() {
				dir, operands = operands[0], operands[1:]
			}
		}
		if len(operands) == 0 {
			fmt.Fprintln(stderr, "local-file "+action+" requires at least one PATH")
			return 2
		}
		for _, path := range operands {
			if path == "" || strings.HasPrefix(path, "-") {
				fmt.Fprintf(stderr, "invalid local file path %q\n", path)
				return 2
			}
		}
	default:
		fmt.Fprintf(stderr, "unknown local-file action: %q\n", action)
		printAgentInstructionsUsage(stderr)
		return 2
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return finish(err, true)
	}
	report.Directory = dir
	var files []string
	switch action {
	case "list":
		files, err = overlayruntime.ListLocalFiles(ctx, dir)
	case "add":
		files, err = overlayruntime.AddLocalFiles(ctx, dir, operands)
	case "remove":
		files, err = overlayruntime.RemoveLocalFiles(ctx, dir, operands)
	}
	report.LocalFiles = files
	return finish(err, err != nil)
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
			options := overlayruntime.CheckOptions{ClaudeConfigDir: target.configDir, CodexHome: target.home}
			if status.Agent == "codex" {
				native, err := agentinstructions.InspectNativeCodexForHome(ctx, dir, target.home, target.installation.ExpectedCodexHooks())
				r.Native = &native
				if err != nil {
					r.Issues = append(r.Issues, err.Error())
					if r.State == "configured" {
						r.State = "unknown"
					}
				} else {
					options.CodexProjectDocMaxBytes = native.ProjectDocMaxBytes
					if r.State == "configured" && native.State != "configured" {
						r.State = native.State
					}
				}
			}
			repository, err := overlayruntime.CheckRepository(ctx, dir, status.Agent, options)
			if err != nil {
				r.State = "blocked"
				r.Issues = append(r.Issues, err.Error())
			} else {
				r.Repository = &repository
				if len(repository.Problems) > 0 {
					r.State = "blocked"
				}
			}
			if status.Agent == "claude" {
				problems, err := target.installation.InspectClaudeRepositoryHooks(ctx, dir)
				if err != nil {
					problems = append(problems, err.Error())
				}
				if len(problems) > 0 {
					r.State = "blocked"
					r.Issues = append(r.Issues, problems...)
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
		fmt.Fprintln(out, "dry-run: no account configuration or ignore files changed")
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
	if r.GlobalIgnore != nil {
		fmt.Fprintf(out, "global ignore: %s (change=%t", r.GlobalIgnore.Path, r.GlobalIgnore.Changed)
		if len(r.GlobalIgnore.Add) > 0 {
			fmt.Fprintf(out, "; add %s", strings.Join(r.GlobalIgnore.Add, ", "))
		}
		if len(r.GlobalIgnore.Remove) > 0 {
			fmt.Fprintf(out, "; remove %s", strings.Join(r.GlobalIgnore.Remove, ", "))
		}
		fmt.Fprintln(out, ")")
	}
	if r.Applied != nil {
		for _, path := range r.Applied.Applied {
			fmt.Fprintln(out, "saved:", path)
		}
	}
	if strings.HasPrefix(r.Operation, "local-file") && r.Error == "" {
		if len(r.LocalFiles) == 0 {
			fmt.Fprintln(out, "no registered local files")
		}
		for _, path := range r.LocalFiles {
			fmt.Fprintln(out, "local file:", path)
		}
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
			for _, path := range agent.Repository.Generated {
				fmt.Fprintln(out, "  generated: "+path)
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
	claudeConfigDir := fs.String("claude-config-dir", "", "Claude config directory")
	codexHome := fs.String("codex-home", "", "Codex home directory")
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
	input, err := io.ReadAll(io.LimitReader(stdin, 1<<20+1))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options, err := prepareHookOptions(ctx, *agent, *event, *claudeConfigDir, *codexHome, input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return overlayruntime.RunPrepareHookWithOptions(ctx, *agent, *event, bytes.NewReader(input), stdout, stderr, options)
}

func prepareHookOptions(ctx context.Context, agent, event, claudeConfigDir, codexHome string, input []byte) (overlayruntime.PrepareHookOptions, error) {
	var err error
	options := overlayruntime.PrepareHookOptions{}
	switch agent {
	case "claude":
		if claudeConfigDir != "" {
			options.ClaudeConfigDir, err = config.CanonicalAccountDirectory(claudeConfigDir)
		} else {
			options.ClaudeConfigDir, err = config.DefaultAccountDirectory("claude")
		}
	case "codex":
		if codexHome != "" {
			options.CodexHome, err = config.CanonicalAccountDirectory(codexHome)
		} else {
			options.CodexHome, err = config.DefaultAccountDirectory("codex")
		}
	default:
		return options, nil
	}
	if err != nil {
		return options, err
	}
	if agent != "codex" || event != "SessionStart" {
		return options, nil
	}
	var h struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(input, &h); err != nil || h.CWD == "" {
		return options, nil
	}
	native, err := agentinstructions.InspectNativeCodexConfigForHome(ctx, h.CWD, options.CodexHome)
	if err != nil {
		options.CodexNativeIssues = []string{"could not inspect effective Codex config: " + err.Error()}
		return options, nil
	}
	options.CodexProjectDocMaxBytes = native.ProjectDocMaxBytes
	options.CodexNativeIssues = native.Issues
	return options, nil
}
