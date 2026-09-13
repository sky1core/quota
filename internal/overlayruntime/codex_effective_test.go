package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveCodexBudgetUsesCheckoutSourceBytes(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "한글\n")
	child := filepath.Join(repo, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		budget int64
		bad    bool
	}{{0, true}, {-1, true}, {6, true}, {7, false}, {8, false}} {
		err := ValidateCodexDocumentBudget(context.Background(), child, tc.budget)
		if (err != nil) != tc.bad {
			t.Errorf("budget=%d err=%v", tc.budget, err)
		}
	}
}

func TestPlannedCodexDocumentsUseFutureSourcesAndInvocation(t *testing.T) {
	repo, linked := regressionLinkedRepo(t)
	write(t, filepath.Join(repo, sharedRule), "primary\n")
	write(t, filepath.Join(linked, sharedRule), "linked\n")
	write(t, filepath.Join(repo, localRule), "private\n")
	if err := SetupRepository(context.Background(), repo, "codex", "checkout"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, localRule), "한글\n")
	child := filepath.Join(linked, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanCodexDocuments(context.Background(), child, "codex", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Documents) != 3 {
		t.Fatalf("wrong selected contexts: %+v", plan.Documents)
	}
	for _, document := range plan.Documents {
		shared := "linked\n"
		if document.Directory == resolvePath(repo) {
			shared = "primary\n"
		}
		want := int64(len(mergedCodexInstructions([]byte(shared), []byte("한글\n"))))
		if document.Bytes != want {
			t.Fatalf("used old on-disk merged bytes: %+v, want %d", document, want)
		}
		if document.Directory == resolvePath(child) && document.Path != filepath.Join(resolvePath(linked), codexRule) {
			t.Fatalf("child context selected wrong root document: %+v", document)
		}
	}
}

func TestPlannedCodexDocumentsUseFuturePrimaryCopies(t *testing.T) {
	repo, linked := untrackedSharedWorktrees(t)
	if err := SetupRepository(context.Background(), repo, "codex", "primary"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, sharedRule), "한글 source\n")
	plan, err := PlanCodexDocuments(context.Background(), linked, "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Documents) != 2 {
		t.Fatalf("wrong selected contexts: %+v", plan.Documents)
	}
	for _, document := range plan.Documents {
		if document.Bytes != int64(len("한글 source\n")) || filepath.Base(document.Path) != sharedRule {
			t.Fatalf("used old shared copy instead of future primary source: %+v", document)
		}
	}
	if got := regressionRead(t, filepath.Join(linked, sharedRule)); got == "한글 source\n" {
		t.Fatal("document planning rewrote the shared copy")
	}
}
