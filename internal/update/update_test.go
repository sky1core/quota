package update

import (
	"context"
	"os/exec"
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

// TestBinPath_RealGoEnv checks the production path against the real go tool
// by deriving the expected location independently (separate single-value
// `go env` calls, so the empty-GOBIN line can't be misparsed). This is the
// path the bar re-execs after an update — a wrong path bricks the restart.
// REGRESSION GUARD: with GOBIN unset, BinPath once returned GOPATH/<name>
// (missing /bin) because trimming the combined output swallowed GOBIN's empty
// line.
func TestBinPath_RealGoEnv(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	goEnv := func(key string) string {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", key, err)
		}
		return strings.TrimSpace(string(out))
	}
	var want string
	if gobin := goEnv("GOBIN"); gobin != "" {
		want = gobin + "/quota-bar"
	} else if gopath := goEnv("GOPATH"); gopath != "" {
		want = strings.Split(gopath, ":")[0] + "/bin/quota-bar"
	} else {
		t.Skip("neither GOBIN nor GOPATH set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := BinPath(ctx, "quota-bar")
	if err != nil {
		t.Fatalf("BinPath: %v", err)
	}
	if p != want {
		t.Errorf("BinPath = %q, want %q", p, want)
	}
}
