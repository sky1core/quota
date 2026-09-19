package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/config"
)

type agentHooksOptions struct {
	policyDir string
	jsonOut   bool
}

type agentHookTarget struct {
	runtime string
	account string
	path    string
}

func runAgent(args []string) int {
	if len(args) == 0 {
		printAgentUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "hooks":
		return runAgentHooks(args[1:], os.Stdout, os.Stderr)
	case "instructions":
		return runAgentInstructions(args[1:], os.Stdin, os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent command: %q\n\n", args[0])
		printAgentUsage(os.Stderr)
		return 2
	}
}

func printAgentUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  quota-cli agent hooks <init|list|plan|apply|verify|doctor|eval> [options]
  quota-cli agent instructions <setup|uninstall|status|local-file> [options]
`)
}

func runAgentHooks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAgentHooksUsage(stderr)
		return 2
	}
	switch args[0] {
	case "init":
		return agentHooksInit(args[1:], stdout, stderr)
	case "list":
		return agentHooksList(args[1:], stdout, stderr)
	case "plan":
		return agentHooksPlan(args[1:], stdout, stderr)
	case "apply":
		return agentHooksApply(args[1:], stdout, stderr)
	case "verify":
		return agentHooksVerify(args[1:], stdout, stderr)
	case "doctor":
		return agentHooksDoctor(args[1:], stdout, stderr)
	case "eval":
		return agentHooksEval(args[1:], os.Stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown agent hooks command: %q\n\n", args[0])
		printAgentHooksUsage(stderr)
		return 2
	}
}

func printAgentHooksUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  quota-cli agent hooks init --preset=github-history-guard [--policy-dir <dir>] [--force]
  quota-cli agent hooks list [--policy-dir <dir>] [--json]
  quota-cli agent hooks plan [--policy-dir <dir>] [--runtime=all|claude|codex] [--binary <quota-cli-path>] [--json]
  quota-cli agent hooks apply [--policy-dir <dir>] [--runtime=all|claude|codex] [--binary <quota-cli-path>]
  quota-cli agent hooks verify [--policy-dir <dir>] [--json]
  quota-cli agent hooks doctor [--policy-dir <dir>] [--runtime=all|claude|codex] [--binary <quota-cli-path>] [--json]
  quota-cli agent hooks eval [--policy-dir <dir>] [--runtime=claude|codex] [--command <shell-command>] [--json]
`)
}

func agentHooksBaseFlags(name string, output io.Writer) (*flag.FlagSet, *agentHooksOptions) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	opts := &agentHooksOptions{}
	fs.StringVar(&opts.policyDir, "policy-dir", "", "Policy directory")
	fs.BoolVar(&opts.jsonOut, "json", false, "Output JSON")
	return fs, opts
}

func agentHooksInit(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks init", io.Discard)
	preset := fs.String("preset", agenthooks.PresetGitHubHistoryGuard, "Preset id")
	force := fs.Bool("force", false, "Overwrite an existing preset policy")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	policy, err := agenthooks.Preset(*preset)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	path, err := agenthooks.SavePolicy(policyDir, policy, *force)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if opts.jsonOut {
		return writeJSON(stdout, map[string]any{"path": path, "preset": *preset}, stderr)
	}
	fmt.Fprintf(stdout, "created %s\n", path)
	return 0
}

func agentHooksList(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks list", io.Discard)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	if opts.jsonOut {
		return writeJSON(stdout, map[string]any{"policies": res.Policies, "errors": errorsAsStrings(res.Errors)}, stderr)
	}
	for _, policy := range res.Policies {
		fmt.Fprintf(stdout, "%s enabled=%v groups=%d rules=%d tests=%d path=%s\n", policy.ID, policy.Enabled, len(policy.Groups), len(policy.Rules), len(policy.Tests), policy.Path)
		printAgentHookGroups(stdout, policy.Groups, "  ")
	}
	for _, err := range res.Errors {
		fmt.Fprintln(stderr, err)
	}
	if len(res.Errors) > 0 {
		return 1
	}
	return 0
}

func agentHooksPlan(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks plan", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	binary := fs.String("binary", "", "quota-cli binary path for hook commands")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	var plans []agenthooks.HookPlan
	cfg, cfgErr := config.Load()
	var targetErrs []error
	if cfgErr != nil {
		targetErrs = append(targetErrs, cfgErr)
	} else {
		var targets []agentHookTarget
		targets, targetErrs = agentHookTargets(cfg, runtimes)
		for _, target := range targets {
			plan := agenthooks.DetectPath(target.runtime, target.path, *binary, policyDir)
			plan.Account = target.account
			plans = append(plans, plan)
		}
	}
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, map[string]any{"policies": res.Policies, "errors": errorsAsStrings(append(res.Errors, targetErrs...)), "hooks": plans}, len(res.Errors)+len(targetErrs) > 0)
	}
	fmt.Fprintln(stdout, "Policies")
	if len(res.Policies) == 0 {
		fmt.Fprintf(stdout, "  none (%s)\n", policyDirForDisplay(policyDir))
	}
	for _, policy := range res.Policies {
		fmt.Fprintf(stdout, "  %s enabled=%v groups=%d rules=%d tests=%d\n", policy.ID, policy.Enabled, len(policy.Groups), len(policy.Rules), len(policy.Tests))
		printAgentHookGroups(stdout, policy.Groups, "    ")
	}
	fmt.Fprintln(stdout, "Hooks")
	for _, plan := range plans {
		status := "missing"
		switch {
		case plan.Error != "":
			status = "error"
		case !plan.Present:
			status = "missing"
		case len(plan.Reasons) > 0:
			status = "blocked"
		default:
			status = "present"
		}
		fmt.Fprintf(stdout, "  %s %s\n    account: %s\n    path: %s\n    command: %s\n", plan.Runtime, status, plan.Account, plan.Path, plan.Command)
		if plan.Error != "" {
			fmt.Fprintf(stdout, "    error: %s\n", plan.Error)
		}
		for _, reason := range plan.Reasons {
			fmt.Fprintf(stdout, "    reason: %s\n", reason)
		}
	}
	for _, err := range res.Errors {
		fmt.Fprintln(stderr, err)
	}
	for _, err := range targetErrs {
		fmt.Fprintln(stderr, err)
	}
	if len(res.Errors)+len(targetErrs) > 0 {
		return 1
	}
	return 0
}

func printAgentHookGroups(stdout io.Writer, groups []agenthooks.Group, indent string) {
	for _, group := range groups {
		if group.Description == "" {
			fmt.Fprintf(stdout, "%sgroup %s\n", indent, group.ID)
			continue
		}
		fmt.Fprintf(stdout, "%sgroup %s: %s\n", indent, group.ID, group.Description)
	}
}

func agentHooksApply(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks apply", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	binary := fs.String("binary", "", "quota-cli binary path for hook commands")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	for _, err := range res.Errors {
		fmt.Fprintln(stderr, err)
	}
	if len(res.Errors) > 0 {
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	targets, targetErrs := agentHookTargets(cfg, runtimes)
	if len(targetErrs) > 0 {
		for _, err := range targetErrs {
			fmt.Fprintln(stderr, err)
		}
		if opts.jsonOut {
			return writeJSONWithCode(stdout, stderr, map[string]any{"hooks": []agenthooks.HookPlan{}, "errors": errorsAsStrings(targetErrs)}, true)
		}
		return 1
	}
	if enabledPolicyCount(res.Policies) == 0 {
		fmt.Fprintf(stderr, "no enabled agent hook policies found in %s\n", policyDirForDisplay(policyDir))
		return 1
	}
	if err := agenthooks.CheckHookBinary(*binary); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var plans []agenthooks.HookPlan
	var applyErrors []string
	for _, target := range targets {
		plan, err := agenthooks.ApplyPath(target.runtime, target.path, *binary, policyDir)
		plan.Account = target.account
		if err != nil {
			fmt.Fprintln(stderr, err)
			applyErrors = append(applyErrors, err.Error())
			if !plan.Present && plan.Error == "" {
				plan.Error = err.Error()
			}
		}
		plans = append(plans, plan)
	}
	failed := len(applyErrors) > 0
	if opts.jsonOut {
		report := map[string]any{"hooks": plans}
		if failed {
			report["errors"] = applyErrors
		}
		return writeJSONWithCode(stdout, stderr, report, failed)
	}
	for _, plan := range plans {
		if plan.Present {
			fmt.Fprintf(stdout, "installed %s hook account=%s: %s\n", plan.Runtime, plan.Account, plan.Path)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func agentHooksVerify(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks verify", io.Discard)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	testResults := agenthooks.RunPolicyTests(res.Policies)
	policyErrs := policyLoadErrors(res, policyDir, true)
	failed := len(policyErrs) > 0
	for _, result := range testResults {
		if !result.Passed {
			failed = true
			break
		}
	}
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, map[string]any{"errors": errorsAsStrings(policyErrs), "tests": testResults}, failed)
	}
	for _, result := range testResults {
		status := "ok"
		if !result.Passed {
			status = "FAIL"
		}
		name := result.Name
		if result.Group != "" {
			name = result.Group + "/" + name
		}
		fmt.Fprintf(stdout, "%s %s/%s decision=%s rule=%s\n", status, result.PolicyID, name, result.Got, result.RuleID)
		if result.Error != "" {
			fmt.Fprintf(stdout, "  error: %s\n", result.Error)
		}
	}
	for _, err := range policyErrs {
		fmt.Fprintln(stderr, err)
	}
	if failed {
		return 1
	}
	return 0
}

func agentHooksDoctor(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks doctor", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	binary := fs.String("binary", "", "quota-cli binary path for hook commands")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	var hooks []agenthooks.HookPlan
	cfg, cfgErr := config.Load()
	var targetErrs []error
	if cfgErr != nil {
		targetErrs = append(targetErrs, cfgErr)
	} else {
		var targets []agentHookTarget
		targets, targetErrs = agentHookTargets(cfg, runtimes)
		for _, target := range targets {
			plan := agenthooks.DetectPath(target.runtime, target.path, *binary, policyDir)
			plan.Account = target.account
			hooks = append(hooks, plan)
		}
	}
	sort.Slice(hooks, func(i, j int) bool {
		if hooks[i].Runtime != hooks[j].Runtime {
			return hooks[i].Runtime < hooks[j].Runtime
		}
		return hooks[i].Account < hooks[j].Account
	})
	policyErrs := policyLoadErrors(res, policyDir, true)
	failed := len(policyErrs)+len(targetErrs) > 0
	statuses := make([]string, len(hooks))
	for i := range hooks {
		switch {
		case hooks[i].Error != "":
			statuses[i] = "error"
			failed = true
			continue
		case !hooks[i].Present:
			statuses[i] = "missing"
			failed = true
			continue
		}
		if len(hooks[i].Reasons) > 0 {
			statuses[i] = "blocked"
			failed = true
		} else {
			statuses[i] = "ok"
		}
		binaryToCheck := *binary
		if strings.TrimSpace(binaryToCheck) == "" && strings.TrimSpace(hooks[i].Binary) != "" {
			binaryToCheck = hooks[i].Binary
		}
		if err := agenthooks.CheckHookBinary(binaryToCheck); err != nil {
			hooks[i].Error = err.Error()
			statuses[i] = "broken"
			failed = true
		}
	}
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, map[string]any{"errors": errorsAsStrings(append(policyErrs, targetErrs...)), "hooks": hooks}, failed)
	}
	for i, hook := range hooks {
		fmt.Fprintf(stdout, "%s %s hook account=%s path=%s\n", statuses[i], hook.Runtime, hook.Account, hook.Path)
		if hook.Error != "" {
			fmt.Fprintf(stdout, "  %s\n", hook.Error)
		}
		for _, reason := range hook.Reasons {
			fmt.Fprintf(stdout, "  reason: %s\n", reason)
		}
	}
	for _, err := range policyErrs {
		fmt.Fprintln(stderr, err)
	}
	for _, err := range targetErrs {
		fmt.Fprintln(stderr, err)
	}
	if failed {
		return 1
	}
	return 0
}

func agentHooksEval(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, opts := agentHooksBaseFlags("quota-cli agent hooks eval", io.Discard)
	runtime := fs.String("runtime", "", "Runtime name: claude or codex")
	command := fs.String("command", "", "Shell command to evaluate instead of reading a hook event from stdin")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentHooksUsage(stderr)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	if *runtime != "" && *runtime != "claude" && *runtime != "codex" {
		fmt.Fprintf(stderr, "--runtime must be claude or codex\n")
		return 2
	}
	policyDir, err := resolveAgentHooksPolicyDir(opts.policyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	res := agenthooks.LoadPolicies(policyDir)
	if len(res.Errors) > 0 {
		for _, err := range res.Errors {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}
	if enabledPolicyCount(res.Policies) == 0 {
		fmt.Fprintf(stderr, "no enabled agent hook policies found in %s\n", policyDirForDisplay(policyDir))
		return 2
	}
	var decision agenthooks.Decision
	var evalErr error
	if *command != "" {
		decision, evalErr = agenthooks.EvaluateCommand(res.Policies, *command)
	} else {
		b, readErr := io.ReadAll(stdin)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 2
		}
		decision, evalErr = agenthooks.EvaluateHookEvent(res.Policies, b)
	}
	if evalErr != nil {
		fmt.Fprintln(stderr, evalErr)
		return 2
	}
	if opts.jsonOut {
		code := 0
		if !decision.Allowed {
			code = 2
		}
		if writeErr := json.NewEncoder(stdout).Encode(decision); writeErr != nil {
			fmt.Fprintf(stderr, "json encode: %v\n", writeErr)
			return 1
		}
		return code
	}
	if !decision.Allowed {
		if decision.Reason != "" {
			fmt.Fprintln(stderr, decision.Reason)
		} else {
			fmt.Fprintln(stderr, "blocked by agent hook policy")
		}
		return 2
	}
	return 0
}

func selectedRuntimes(runtime string) ([]string, error) {
	switch runtime {
	case "all":
		return []string{"claude", "codex"}, nil
	case "claude", "codex":
		return []string{runtime}, nil
	default:
		return nil, fmt.Errorf("--runtime must be all, claude, or codex")
	}
}

func agentHookTargets(cfg config.Config, runtimes []string) ([]agentHookTarget, []error) {
	var targets []agentHookTarget
	var errs []error
	for _, runtime := range runtimes {
		providerTargets, providerErrs := agentHookProviderTargets(cfg, runtime)
		targets = append(targets, providerTargets...)
		errs = append(errs, providerErrs...)
	}
	return targets, errs
}

func agentHookProviderTargets(cfg config.Config, runtime string) ([]agentHookTarget, []error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, []error{err}
	}
	var raw []struct {
		key string
		dir string
	}
	switch runtime {
	case "claude":
		raw = append(raw, struct {
			key string
			dir string
		}{"claude", filepath.Join(home, ".claude")})
		for _, account := range cfg.ClaudeAccounts {
			raw = append(raw, struct {
				key string
				dir string
			}{account.Key, account.ConfigDir})
		}
	case "codex":
		raw = append(raw, struct {
			key string
			dir string
		}{"codex", filepath.Join(home, ".codex")})
		for _, account := range cfg.CodexAccounts {
			raw = append(raw, struct {
				key string
				dir string
			}{account.Key, account.Home})
		}
	default:
		return nil, []error{fmt.Errorf("unsupported runtime %q", runtime)}
	}
	seenKeys := map[string]bool{}
	seenDirs := map[string]string{}
	var targets []agentHookTarget
	var errs []error
	for i, item := range raw {
		if i > 0 {
			switch runtime {
			case "claude":
				if !config.ClaudeExtraKeyRe.MatchString(item.key) {
					errs = append(errs, fmt.Errorf("claude account key %q must match claude-<N>", item.key))
					continue
				}
			case "codex":
				if !config.CodexExtraKeyRe.MatchString(item.key) {
					errs = append(errs, fmt.Errorf("codex account key %q must match codex-<N>", item.key))
					continue
				}
			}
		}
		if seenKeys[item.key] {
			errs = append(errs, fmt.Errorf("%s account key %q is duplicated", runtime, item.key))
			continue
		}
		seenKeys[item.key] = true
		dir, err := config.CanonicalAccountDirectory(item.dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s account %q directory is invalid: %v", runtime, item.key, err))
			continue
		}
		if other := seenDirs[dir]; other != "" {
			errs = append(errs, fmt.Errorf("%s accounts %q and %q use the same directory %s", runtime, other, item.key, dir))
			continue
		}
		seenDirs[dir] = item.key
		path := agenthooks.ClaudeSettingsPathForConfigDir(dir)
		if runtime == "codex" {
			path = agenthooks.CodexHooksPathForHome(dir)
		}
		targets = append(targets, agentHookTarget{runtime: runtime, account: item.key, path: path})
	}
	return targets, errs
}

func writeJSON(output io.Writer, payload any, stderr io.Writer) int {
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		fmt.Fprintf(stderr, "json encode: %v\n", err)
		return 1
	}
	return 0
}

func writeJSONWithCode(output, stderr io.Writer, payload any, failed bool) int {
	code := writeJSON(output, payload, stderr)
	if code != 0 {
		return code
	}
	if failed {
		return 1
	}
	return 0
}

func errorsAsStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func policyLoadErrors(res agenthooks.LoadResult, policyDir string, requireEnabled bool) []error {
	out := append([]error(nil), res.Errors...)
	if requireEnabled && enabledPolicyCount(res.Policies) == 0 {
		out = append(out, fmt.Errorf("no enabled agent hook policies found in %s", policyDirForDisplay(policyDir)))
	}
	return out
}

func resolveAgentHooksPolicyDir(policyDir string) (string, error) {
	policyDir = strings.TrimSpace(policyDir)
	if policyDir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(config.ExpandTilde(policyDir))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func enabledPolicyCount(policies []agenthooks.Policy) int {
	var count int
	for _, policy := range policies {
		if policy.Enabled {
			count++
		}
	}
	return count
}

func policyDirForDisplay(dir string) string {
	if strings.TrimSpace(dir) != "" {
		return dir
	}
	return agenthooks.DefaultPolicyDir()
}
