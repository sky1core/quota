package overlayruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sky1core/quota/internal/childprocess"
)

func ValidateGitEnvironment(ctx context.Context) error {
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
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := childprocess.Run(cmd); err != nil {
		return fmt.Errorf("inspect Git environment: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	var overrides []string
	for _, name := range strings.Fields(out.String()) {
		if _, exists := os.LookupEnv(name); exists {
			overrides = append(overrides, name)
		}
	}
	if len(overrides) > 0 {
		return fmt.Errorf("instruction preparation refuses repository-local Git environment variables: %s; unset them before running", strings.Join(overrides, ", "))
	}
	return nil
}
