package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sky1core/quota/internal/overlayruntime"
)

func prepareDelegatedInstructions(ctx context.Context, provider string, args []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	createsWorktree := false
	switch provider {
	case "codex":
		dir, createsWorktree, err = delegatedCodexTarget(dir, args)
		if err != nil {
			return err
		}
	case "claude":
		createsWorktree = claudeCreatesWorktree(args)
	}
	if err := overlayruntime.PrepareDelegationInstructions(ctx, dir, provider, createsWorktree); err != nil {
		return fmt.Errorf("delegation instruction preparation failed: %w", err)
	}
	return nil
}

func execPreparedDelegated(ctx context.Context, provider, bin string, prefix, forwarded, env []string) error {
	if err := prepareDelegatedInstructions(ctx, provider, forwarded); err != nil {
		return err
	}
	return execDelegated(ctx, bin, prefix, forwarded, env)
}

func delegatedCodexTarget(cwd string, args []string) (string, bool, error) {
	dir := cwd
	worktree := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "--worktree":
			worktree = true
		case arg == "-C" || arg == "--cd":
			i++
			if i == len(args) {
				return "", false, fmt.Errorf("%s requires a directory", arg)
			}
			dir = args[i]
		case strings.HasPrefix(arg, "--cd="):
			dir = strings.TrimPrefix(arg, "--cd=")
		case strings.HasPrefix(arg, "-C"):
			dir = strings.TrimPrefix(strings.TrimPrefix(arg, "-C"), "=")
		default:
			switch arg {
			case "-c", "--config", "--enable", "--disable", "-i", "--image", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "--add-dir", "--thread-source", "--output-schema", "--color", "-o", "--output-last-message", "--base", "--commit", "--title":
				i++
			}
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return dir, worktree, nil
}

func claudeCreatesWorktree(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			if name == "--worktree" {
				return true
			}
			if claudeOptionRequiresValue(name) && !attached {
				i++
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
	shortOptions:
		for j := 1; j < len(arg); j++ {
			switch arg[j] {
			case 'w':
				return true
			case 'm', 'n':
				if j+1 == len(arg) {
					i++
				}
				break shortOptions
			case 'c', 'h', 'p', 'v':
				continue
			default:
				break shortOptions
			}
		}
	}
	return false
}
