package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/sky1core/quota/internal/agentoverlay"
	"github.com/sky1core/quota/internal/config"
)

type agentOverlayOptions struct {
	spec    string
	jsonOut bool
}

func runAgentOverlay(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAgentOverlayUsage(stderr)
		return 2
	}
	switch args[0] {
	case "init":
		return agentOverlayInit(args[1:], stdout, stderr)
	case "plan":
		return agentOverlayPlan(args[1:], stdout, stderr)
	case "apply":
		return agentOverlayApply(args[1:], stdout, stderr)
	case "doctor":
		return agentOverlayDoctor(args[1:], stdout, stderr)
	case "verify":
		return agentOverlayVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown agent overlay command: %q\n\n", args[0])
		printAgentOverlayUsage(stderr)
		return 2
	}
}

func printAgentOverlayUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  quota-cli agent overlay init [--spec <file>] [--force] [--json]
  quota-cli agent overlay plan [--spec <file>] [--runtime=all|claude|codex] [--json]
  quota-cli agent overlay apply [--spec <file>] [--runtime=all|claude|codex] [--json]
  quota-cli agent overlay doctor [--spec <file>] [--runtime=all|claude|codex] [--json]
  quota-cli agent overlay verify [--spec <file>] [--json]
`)
}

func agentOverlayBaseFlags(name string, output io.Writer) (*flag.FlagSet, *agentOverlayOptions) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	opts := &agentOverlayOptions{}
	fs.StringVar(&opts.spec, "spec", "", "Spec file (default ~/.config/quota/agent-overlay.json)")
	fs.BoolVar(&opts.jsonOut, "json", false, "Output JSON")
	return fs, opts
}

func agentOverlayInit(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentOverlayBaseFlags("quota-cli agent overlay init", io.Discard)
	force := fs.Bool("force", false, "Overwrite an existing spec file")
	if code, done := parseAgentOverlayFlags(fs, args, stderr); done {
		return code
	}
	specPath, err := resolveAgentOverlaySpecPath(opts.spec)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	path, err := agentoverlay.SaveSpec(specPath, agentoverlay.InitTemplate(), *force)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if opts.jsonOut {
		return writeJSON(stdout, map[string]any{"path": path}, stderr)
	}
	fmt.Fprintf(stdout, "created %s\n", path)
	return 0
}

func agentOverlayPlan(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentOverlayBaseFlags("quota-cli agent overlay plan", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	if code, done := parseAgentOverlayFlags(fs, args, stderr); done {
		return code
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	spec, code := loadAgentOverlaySpec(opts.spec, stderr)
	if code != 0 {
		return code
	}
	var plans []agentoverlay.RuntimePlan
	failed := false
	for _, rt := range runtimes {
		plan := planForRuntime(rt, spec)
		if plan.Error != "" {
			failed = true
		}
		plans = append(plans, plan)
	}
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, map[string]any{"plans": plans}, failed)
	}
	for _, plan := range plans {
		printOverlayPlan(stdout, plan)
	}
	if failed {
		return 1
	}
	return 0
}

func agentOverlayApply(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentOverlayBaseFlags("quota-cli agent overlay apply", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	if code, done := parseAgentOverlayFlags(fs, args, stderr); done {
		return code
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	spec, code := loadAgentOverlaySpec(opts.spec, stderr)
	if code != 0 {
		return code
	}
	codexRequested := *runtime == "codex"
	var plans []agentoverlay.RuntimePlan
	for _, rt := range runtimes {
		if rt == "codex" {
			fmt.Fprintln(stderr, "codex apply is not supported in v1; use plan/doctor")
			if codexRequested {
				return 1
			}
			continue
		}
		plan, err := agentoverlay.ApplyClaude(spec)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		plans = append(plans, plan)
	}
	if opts.jsonOut {
		return writeJSON(stdout, map[string]any{"plans": plans}, stderr)
	}
	for _, plan := range plans {
		if !plan.Configured {
			fmt.Fprintf(stdout, "%s overlay unconfigured (nothing to apply)\n", plan.Runtime)
			continue
		}
		fmt.Fprintf(stdout, "applied %s overlay: %s\n", plan.Runtime, plan.Path)
	}
	return 0
}

func agentOverlayDoctor(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentOverlayBaseFlags("quota-cli agent overlay doctor", io.Discard)
	runtime := fs.String("runtime", "all", "Runtime: all, claude, or codex")
	if code, done := parseAgentOverlayFlags(fs, args, stderr); done {
		return code
	}
	runtimes, err := selectedRuntimes(*runtime)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	spec, code := loadAgentOverlaySpec(opts.spec, stderr)
	if code != 0 {
		return code
	}
	var docs []agentoverlay.RuntimeDoctor
	failed := false
	for _, rt := range runtimes {
		doc := doctorForRuntime(rt, spec)
		if doc.State == agentoverlay.StateDegraded || doc.State == agentoverlay.StateError {
			failed = true
		}
		docs = append(docs, doc)
	}
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, map[string]any{"runtimes": docs}, failed)
	}
	for _, doc := range docs {
		printOverlayDoctor(stdout, doc)
	}
	if failed {
		return 1
	}
	return 0
}

func agentOverlayVerify(args []string, stdout, stderr io.Writer) int {
	fs, opts := agentOverlayBaseFlags("quota-cli agent overlay verify", io.Discard)
	if code, done := parseAgentOverlayFlags(fs, args, stderr); done {
		return code
	}
	spec, code := loadAgentOverlaySpec(opts.spec, stderr)
	if code != 0 {
		return code
	}
	res := agentoverlay.Verify(spec)
	if opts.jsonOut {
		return writeJSONWithCode(stdout, stderr, res, res.Failed)
	}
	for _, doc := range res.Runtimes {
		printOverlayDoctor(stdout, doc)
	}
	if res.Failed {
		return 1
	}
	return 0
}

func planForRuntime(runtime string, spec *agentoverlay.Spec) agentoverlay.RuntimePlan {
	if runtime == "codex" {
		return agentoverlay.PlanCodex(spec)
	}
	return agentoverlay.PlanClaude(spec)
}

func doctorForRuntime(runtime string, spec *agentoverlay.Spec) agentoverlay.RuntimeDoctor {
	if runtime == "codex" {
		return agentoverlay.DoctorCodex(spec)
	}
	return agentoverlay.DoctorClaude(spec)
}

func parseAgentOverlayFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			printAgentOverlayUsage(stderr)
			return 0, true
		}
		fmt.Fprintln(stderr, err)
		return 2, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2, true
	}
	return 0, false
}

func loadAgentOverlaySpec(specFlag string, stderr io.Writer) (*agentoverlay.Spec, int) {
	specPath, err := resolveAgentOverlaySpecPath(specFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}
	spec, err := agentoverlay.LoadSpec(specPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return spec, 0
}

func resolveAgentOverlaySpecPath(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return agentoverlay.DefaultSpecPath(), nil
	}
	abs, err := filepath.Abs(config.ExpandTilde(spec))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func printOverlayPlan(w io.Writer, plan agentoverlay.RuntimePlan) {
	if !plan.Configured {
		fmt.Fprintf(w, "%s unconfigured\n", plan.Runtime)
		return
	}
	label := "settings"
	if plan.Runtime == "codex" {
		label = "config"
	}
	fmt.Fprintf(w, "%s %s=%s\n", plan.Runtime, label, plan.Path)
	if plan.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", plan.Error)
		return
	}
	for _, reason := range plan.Reasons {
		fmt.Fprintf(w, "  reason: %s\n", reason)
	}
	for _, s := range plan.Settings {
		fmt.Fprintf(w, "  setting %s  %s\n", s.Key, s.Status)
	}
	for _, e := range plan.Entries {
		fmt.Fprintf(w, "  %s %s  %s\n", e.Event, e.Command, e.Status)
		if e.Reason != "" {
			fmt.Fprintf(w, "    reason: %s\n", e.Reason)
		}
	}
}

func printOverlayDoctor(w io.Writer, doc agentoverlay.RuntimeDoctor) {
	fmt.Fprintf(w, "%s %s path=%s\n", doc.Runtime, doc.State, doc.Path)
	if doc.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", doc.Error)
	}
	if doc.Reason != "" {
		fmt.Fprintf(w, "  reason: %s\n", doc.Reason)
	}
	for _, s := range doc.Settings {
		fmt.Fprintf(w, "  setting %s  %s\n", s.Key, s.Status)
	}
	for _, m := range doc.Missing {
		fmt.Fprintf(w, "  %s %s  %s\n", m.Event, m.Command, m.Status)
		if m.Reason != "" {
			fmt.Fprintf(w, "    reason: %s\n", m.Reason)
		}
	}
	if doc.Snippet != "" {
		fmt.Fprintf(w, "  add to %s:\n", doc.Path)
		printIndentedBlock(w, doc.Snippet)
	}
	for _, r := range doc.Replacements {
		printIndentedBlock(w, r)
	}
	if doc.Verify != nil {
		fmt.Fprintf(w, "  live verification: exit %d\n", doc.Verify.ExitCode)
		if doc.Verify.Error != "" {
			fmt.Fprintf(w, "    error: %s\n", doc.Verify.Error)
		}
		printIndentedBlock(w, doc.Verify.Output)
	}
}

func printIndentedBlock(w io.Writer, block string) {
	block = strings.TrimRight(block, "\n")
	if block == "" {
		return
	}
	for _, line := range strings.Split(block, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}
