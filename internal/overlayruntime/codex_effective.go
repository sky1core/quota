package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func ValidateCodexDocumentBudget(ctx context.Context, dir string, maxBytes int64) error {
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	if maxBytes <= 0 {
		return fmt.Errorf("effective Codex project_doc_max_bytes must be positive")
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return err
	}
	path := filepath.Join(r.Top, sharedRule)
	if exists(filepath.Join(r.Top, codexRule)) {
		path = filepath.Join(r.Top, codexRule)
	}
	body, err := readRegular(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("%s is %d bytes, over effective Codex project_doc_max_bytes %d", path, len(body), maxBytes)
	}
	return nil
}
