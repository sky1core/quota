package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o751); err != nil {
		t.Fatal(err)
	}
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: got %q, err %v; want %q", path, got, err, want)
	}
}

func transactionFiles(t *testing.T) (string, []replacement) {
	t.Helper()
	dir := t.TempDir()
	stage := t.TempDir()
	var files []replacement
	for _, name := range []string{"quota-cli", "quota-bar"} {
		file := replacement{source: filepath.Join(stage, name), destination: filepath.Join(dir, name)}
		writeExecutable(t, file.source, "new "+name)
		writeExecutable(t, file.destination, "old "+name)
		files = append(files, file)
	}
	return dir, files
}

func assertOriginals(t *testing.T, files []replacement) {
	t.Helper()
	for _, file := range files {
		assertContents(t, file.destination, "old "+filepath.Base(file.destination))
	}
}

func assertNoTransactionFiles(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, ".quota-update-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary transaction files: %v, %v", files, err)
	}
}

func TestReplaceFiles(t *testing.T) {
	dir, files := transactionFiles(t)
	old, err := os.Open(files[0].destination)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := replaceFiles(context.Background(), dir, files); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		assertContents(t, file.destination, "new "+filepath.Base(file.destination))
		info, err := os.Stat(file.destination)
		if err != nil || info.Mode().Perm() != 0o751 {
			t.Fatalf("executable mode not preserved: %v, %v", info, err)
		}
	}
	data := make([]byte, 64)
	n, err := old.Read(data)
	if err != nil || string(data[:n]) != "old quota-cli" {
		t.Fatalf("open old executable was modified: %q, %v", data[:n], err)
	}
	assertNoTransactionFiles(t, dir)
}

func TestPreparationFailurePreservesBothOriginals(t *testing.T) {
	for _, failure := range []string{"missing second build", "nonexecutable second build", "symlink destination"} {
		t.Run(failure, func(t *testing.T) {
			dir, files := transactionFiles(t)
			switch failure {
			case "missing second build":
				files[1].source += ".missing"
			case "nonexecutable second build":
				if err := os.Chmod(files[1].source, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink destination":
				original := files[1].destination + ".original"
				if err := os.Rename(files[1].destination, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, files[1].destination); err != nil {
					t.Fatal(err)
				}
			}
			if err := replaceFiles(context.Background(), dir, files); err == nil {
				t.Fatal("invalid preparation succeeded")
			}
			assertOriginals(t, files)
			assertNoTransactionFiles(t, dir)
		})
	}
}

func TestCommitFailureRollsBackFirstReplacement(t *testing.T) {
	for _, missingCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing caller", true: "missing caller"}[missingCaller], func(t *testing.T) {
			_, files := transactionFiles(t)
			if missingCaller {
				if err := os.Remove(files[0].destination); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := prepareReplacements(t.TempDir(), files)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(prepared[1].staged); err != nil {
				t.Fatal(err)
			}
			failed, err := commitReplacements(context.Background(), prepared)
			if failed || err == nil || !strings.Contains(err.Error(), files[1].destination) {
				t.Fatalf("rollback failed=%v, err=%v", failed, err)
			}
			if missingCaller {
				if _, err := os.Lstat(files[0].destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("new caller survived rollback: %v", err)
				}
			} else {
				assertOriginals(t, files)
			}
			assertContents(t, files[1].destination, "old quota-bar")
		})
	}
}

func TestCommitFailureReportsRollbackFailure(t *testing.T) {
	_, files := transactionFiles(t)
	prepared, err := prepareReplacements(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{prepared[0].backup, prepared[1].staged} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := commitReplacements(context.Background(), prepared)
	if !failed || err == nil || !strings.Contains(err.Error(), "rollback failed") || !strings.Contains(err.Error(), prepared[0].backup) || !strings.Contains(err.Error(), files[1].destination) {
		t.Fatalf("rollback failure not explicit: failed=%v, err=%v", failed, err)
	}
	assertContents(t, files[0].destination, "new quota-cli")
	assertContents(t, files[1].destination, "old quota-bar")
}

func TestRollbackContinuesAfterFailure(t *testing.T) {
	_, files := transactionFiles(t)
	prepared, err := prepareReplacements(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitReplacements(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(prepared[1].destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(prepared[1].destination, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(prepared[1].destination, "blocker"), "keep")
	if err := rollbackReplacements(prepared); err == nil || !strings.Contains(err.Error(), prepared[1].backup) {
		t.Fatalf("rollback failure missing: %v", err)
	}
	assertContents(t, files[0].destination, "old quota-cli")
	assertContents(t, prepared[1].backup, "old quota-bar")
}

func TestCancelledCommitPreservesOriginals(t *testing.T) {
	dir, files := transactionFiles(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := replaceFiles(ctx, dir, files); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	assertOriginals(t, files)
	assertNoTransactionFiles(t, dir)
}

type cancelWhenReplacedContext struct {
	context.Context
	cancel      context.CancelFunc
	destination string
	original    os.FileInfo
	replaced    bool
}

func (c *cancelWhenReplacedContext) Err() error {
	info, err := os.Stat(c.destination)
	if err == nil && !os.SameFile(info, c.original) {
		c.replaced = true
		c.cancel()
	}
	return c.Context.Err()
}

func TestCancellationBetweenReplacementsRollsBack(t *testing.T) {
	dir, files := transactionFiles(t)
	original, err := os.Stat(files[0].destination)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelCtx := &cancelWhenReplacedContext{
		Context: ctx, cancel: cancel, destination: files[0].destination, original: original,
	}
	err = replaceFiles(cancelCtx, dir, files)
	if !cancelCtx.replaced || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), files[1].destination) {
		t.Fatalf("cancellation after first replacement: replaced=%v, err=%v", cancelCtx.replaced, err)
	}
	assertOriginals(t, files)
	restored, err := os.Stat(files[0].destination)
	if err != nil || !os.SameFile(original, restored) || original.Mode() != restored.Mode() {
		t.Fatalf("first replacement not restored: %v, %v", restored, err)
	}
	assertNoTransactionFiles(t, dir)
}

func TestDestinationChangedDuringPreparation(t *testing.T) {
	_, files := transactionFiles(t)
	prepared, err := prepareReplacements(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeExecutable(t, other, "external replacement")
	if err := os.Rename(other, files[1].destination); err != nil {
		t.Fatal(err)
	}
	failed, err := commitReplacements(context.Background(), prepared)
	if failed || err == nil || !strings.Contains(err.Error(), "destination changed") {
		t.Fatalf("external replacement not detected: %v, %v", failed, err)
	}
	assertContents(t, files[0].destination, "old quota-cli")
	assertContents(t, files[1].destination, "external replacement")
}
