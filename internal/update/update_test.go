package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestResolveVersion(t *testing.T) {
	biVersioned := &debug.BuildInfo{Main: debug.Module{Version: "v0.7.0"}}
	biLocal := &debug.BuildInfo{
		Main:     debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "438784f550e2ffb48f703fa668ec5df3d94b1018"}},
	}
	biBare := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	cases := []struct {
		name   string
		ldflag string
		bi     *debug.BuildInfo
		ok     bool
		want   string
	}{
		{"ldflags override wins", "v9.9.9", biVersioned, true, "v9.9.9"},
		{"module version from @install", "", biVersioned, true, "v0.7.0"},
		{"local build -> short vcs revision", "", biLocal, true, "438784f"},
		{"devel without vcs -> dev", "", biBare, true, "dev"},
		{"no build info -> dev", "", nil, false, "dev"},
	}
	for _, c := range cases {
		if got := ResolveVersion(c.ldflag, c.bi, c.ok); got != c.want {
			t.Errorf("%s: ResolveVersion(%q, …) = %q, want %q", c.name, c.ldflag, got, c.want)
		}
	}
}

func TestBinPathMatchesGoInstall(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	t.Setenv("GOENV", "off")
	module := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.com/install-path-probe\n\ngo 1.25.13\n",
		"main.go": "package main\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name, gobin, wantDir string
		gopath               []string
	}{
		{"gobin", "bin", "bin", []string{"workspace"}},
		{"gobin internal spaces", "bin with spaces", "bin with spaces", []string{"workspace"}},
		{"gobin trailing space", "bin ", "bin ", []string{"workspace"}},
		{"gobin trailing tab", "bin\t", "bin\t", []string{"workspace"}},
		{"gobin newline", "bin\nmore", "bin\nmore", []string{"workspace"}},
		{"gobin trailing newline", "bin\n", "bin\n", []string{"workspace"}},
		{"gopath", "", "workspace/bin", []string{"workspace"}},
		{"gopath internal spaces", "", "work space/bin", []string{"work space"}},
		{"gopath trailing space", "", "workspace /bin", []string{"workspace "}},
		{"gopath trailing newline", "", "workspace\n/bin", []string{"workspace\n"}},
		{"gopath list", "", "first /bin", []string{"first ", "second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gobin := ""
			if tc.gobin != "" {
				gobin = filepath.Join(root, tc.gobin)
			}
			var gopath []string
			for _, path := range tc.gopath {
				gopath = append(gopath, filepath.Join(root, path))
			}
			t.Setenv("GOBIN", gobin)
			t.Setenv("GOPATH", strings.Join(gopath, string(os.PathListSeparator)))
			t.Setenv("GOWORK", "off")
			cmd := exec.CommandContext(ctx, "go", "install", ".")
			cmd.Dir = module
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go install: %v\n%s", err, out)
			}
			want := filepath.Join(root, tc.wantDir, "install-path-probe")
			info, err := os.Stat(want)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("go install did not create executable at %q: %v", want, err)
			}
			got, err := BinPath(ctx, "install-path-probe")
			if err != nil || got != want {
				t.Fatalf("BinPath = %q, err=%v; actual go install path = %q", got, err, want)
			}
		})
	}
}

func TestBinPathPreservesGoEnvBytes(t *testing.T) {
	t.Setenv("GOENV", "off")
	for _, key := range []string{"GOBIN", "GOPATH"} {
		t.Run(key, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "directory-\xff")
			t.Setenv("GOBIN", "")
			t.Setenv(key, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "go", "env", key).Output()
			if err != nil || string(out) != dir+"\n" {
				t.Fatalf("go env changed %s bytes: %q, %v", key, out, err)
			}
			want := filepath.Join(dir, "quota-cli")
			if key == "GOPATH" {
				want = filepath.Join(dir, "bin", "quota-cli")
			}
			got, err := BinPath(ctx, "quota-cli")
			if err != nil || got != want {
				t.Fatalf("BinPath = %q, err=%v; want unchanged path bytes %q", got, err, want)
			}
		})
	}
}
