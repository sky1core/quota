package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/agentskills"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/overlayruntime"
)

type skillsReport struct {
	Operation string               `json:"operation"`
	Results   []agentskills.Result `json:"results"`
	Error     string               `json:"error,omitempty"`
}

func printAgentSkillsUsage(out io.Writer) {
	fmt.Fprint(out, `usage:
  quota-cli agent skills install <source> [--skill <name>] [--scope global|repo] [--json]
  quota-cli agent skills link [<name>] [--scope global|repo] [--json]
  quota-cli agent skills list [--scope global|repo] [--json]
  quota-cli agent skills remove <name> [--scope global|repo] [--json]

Each skill lives in its own named subdirectory of ".agents/skills".
Global scope uses the home directory; repo scope uses the repository root.
Codex reads it directly; Claude account skill paths are links to it.
Edit skills in place at the path shown by list.
install copies a source there and links Claude accounts; existing paths are not overwritten.
link adds missing Claude links for skills already there (all skills without <name>).
remove moves the skill, its links, and Codex duplicates to a backup directory.
Install uses Vercel skills 1.7.0 and requires Node.js >=22.20.0 and npm.
Source: a repository URL/shorthand or an absolute or ./relative local path.
`)
}

func runAgentSkills(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAgentSkillsUsage(stderr)
		return 2
	}
	operation := args[0]
	if operation == "--help" || operation == "-h" {
		printAgentSkillsUsage(stdout)
		return 0
	}
	if operation != "install" && operation != "link" && operation != "list" && operation != "remove" {
		printAgentSkillsUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("quota-cli agent skills "+operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "Output JSON")
	scope := fs.String("scope", "global", "Skill scope: global or repo")
	name := ""
	if operation == "install" {
		fs.StringVar(&name, "skill", "", "Install one named skill (default: all skills in source)")
	}
	ordered, err := instructionFlagOrder(fs, args[1:])
	if err == nil {
		err = fs.Parse(ordered)
	}
	if err == flag.ErrHelp {
		printAgentSkillsUsage(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *scope != "global" && *scope != "repo" {
		fmt.Fprintln(stderr, "scope must be global or repo")
		return 2
	}
	minArgs, maxArgs := 1, 1
	if operation == "list" {
		maxArgs = 0
	}
	if operation == "list" || operation == "link" {
		minArgs = 0
	}
	if fs.NArg() < minArgs || fs.NArg() > maxArgs {
		printAgentSkillsUsage(stderr)
		return 2
	}
	if operation == "remove" || operation == "link" && fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	if name != "" || operation == "remove" || operation == "link" && fs.NArg() == 1 {
		if err := agentskills.ValidateName(name); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if operation == "install" {
		providedEmpty := false
		fs.Visit(func(f *flag.Flag) { providedEmpty = providedEmpty || f.Name == "skill" && name == "" })
		if providedEmpty || fs.Arg(0) == "" {
			fmt.Fprintln(stderr, "source and explicit --skill must not be empty")
			return 2
		}
	}
	report := skillsReport{Operation: operation, Results: []agentskills.Result{}}
	finish := func(err error) int {
		if err != nil {
			report.Error = err.Error()
		}
		if *jsonOut {
			return writeJSONWithCode(stdout, stderr, report, err != nil)
		}
		for _, result := range report.Results {
			target := result.Target.Role
			if len(result.Target.Accounts) != 0 {
				target += "[" + strings.Join(result.Target.Accounts, ",") + "]"
			}
			fmt.Fprintf(stdout, "%s %s %s: %s\n", result.Name, target, result.Status, result.Path)
			if result.RealPath != "" {
				fmt.Fprintln(stdout, "  real: "+result.RealPath)
			}
			if result.BackupPath != "" {
				fmt.Fprintln(stdout, "  backup: "+result.BackupPath)
			}
			if result.Error != "" {
				fmt.Fprintln(stdout, "  "+result.Error)
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if operation == "list" && len(report.Results) == 0 {
			fmt.Fprintln(stdout, "No skills found.")
		}
		return 0
	}
	cfg := config.Config{}
	if *scope == "global" {
		cfg, err = config.Load()
		if err != nil {
			return finish(err)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	repoRoot := ""
	if *scope == "repo" {
		if err := overlayruntime.ValidateGitEnvironment(ctx, ""); err != nil {
			return finish(err)
		}
		output, repoErr := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
		if repoErr != nil {
			return finish(fmt.Errorf("find repository root: %w", repoErr))
		}
		if len(output) == 0 || output[len(output)-1] != '\n' || !utf8.Valid(output) {
			return finish(fmt.Errorf("Git did not return a newline-terminated UTF-8 path"))
		}
		repoRoot, err = filepath.Abs(string(output[:len(output)-1]))
		if err != nil {
			return finish(err)
		}
	}
	manager, err := agentskills.New(cfg, *scope, repoRoot)
	if err != nil {
		return finish(err)
	}
	var results []agentskills.Result
	switch operation {
	case "install":
		skills, cleanup, fetchErr := agentskills.Fetch(ctx, fs.Arg(0), name)
		if fetchErr != nil {
			return finish(fetchErr)
		}
		defer cleanup()
		results, err = manager.Install(ctx, skills)
	case "link":
		var names []string
		if name != "" {
			names = []string{name}
		}
		results, err = manager.Link(ctx, names)
	case "list":
		results, err = manager.List()
	case "remove":
		results, err = manager.Remove(ctx, name)
	}
	if results != nil {
		report.Results = results
	}
	return finish(err)
}
