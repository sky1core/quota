package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindBinarySearchesNativeInstallation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	native := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/echo", native); err != nil {
		t.Fatal(err)
	}
	got, err := FindBinary()
	if err != nil || got != native {
		t.Fatalf("native resolution: %q %v", got, err)
	}
	pathDir := t.TempDir()
	preferred := filepath.Join(pathDir, "claude")
	if err := os.Symlink("/bin/echo", preferred); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	got, err = FindBinary()
	if err != nil || got != preferred {
		t.Fatalf("PATH precedence: %q %v", got, err)
	}
}
func TestFindBinaryRejectsNonExecutableNative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	native := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := FindBinary(); err == nil {
		t.Fatalf("accepted nonexecutable %s", got)
	}
}
