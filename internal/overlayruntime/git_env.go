package overlayruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sky1core/quota/internal/childprocess"
)

func ValidateGitEnvironment(ctx context.Context, dir string) error {
	hasGitEnvironment := false
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") {
			hasGitEnvironment = true
			break
		}
	}
	if !hasGitEnvironment {
		return nil
	}
	cmd := childprocess.CommandContext(ctx, "git", "rev-parse", "--local-env-vars")
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := childprocess.Run(cmd); err != nil {
		return fmt.Errorf("inspect Git environment: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	var overrides []string
	checkConfig := false
	for _, name := range strings.Fields(out.String()) {
		if _, exists := os.LookupEnv(name); exists {
			if name == "GIT_CONFIG_COUNT" || name == "GIT_CONFIG_PARAMETERS" {
				checkConfig = true
				continue
			}
			overrides = append(overrides, name)
		}
	}
	if len(overrides) > 0 {
		return fmt.Errorf("repository-local Git environment variables are not supported: %s; unset them before running", strings.Join(overrides, ", "))
	}
	if checkConfig {
		return validateCommandGitConfig(ctx, dir)
	}
	return nil
}

func validateCommandGitConfig(ctx context.Context, dir string) error {
	cmd := childprocess.CommandContext(ctx, "git", "config", "--null", "--list", "--name-only", "--show-scope", "--includes")
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := childprocess.Run(cmd); err != nil {
		return fmt.Errorf("inspect Git configuration: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	fields := strings.Split(out.String(), "\x00")
	if len(fields)%2 != 1 || fields[len(fields)-1] != "" {
		return fmt.Errorf("Git returned invalid configuration scope output")
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != "command" {
			continue
		}
		key := fields[i+1]
		if key == "core.worktree" || key == "core.bare" || key == "core.repositoryformatversion" || strings.HasPrefix(key, "extensions.") {
			return fmt.Errorf("repository configuration override is not supported: %s; remove it from the Git command environment before running", key)
		}
	}
	return nil
}
