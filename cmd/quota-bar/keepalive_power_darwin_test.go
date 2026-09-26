package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSystemSleepGuardCreatesAndReleasesACAssertion(t *testing.T) {
	if externalPower() != 1 {
		t.Skip("AC power unavailable")
	}
	g := &systemSleepGuard{}
	defer g.close()
	if status, err := g.sync(true); err != nil || status != "active" {
		t.Fatalf("AC assertion: %s %v", status, err)
	}
	assertion := fmt.Sprintf("pid %d(", os.Getpid())
	check := func(want bool) {
		t.Helper()
		out, err := exec.Command("pmset", "-g", "assertions").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, line := range strings.Split(string(out), "\n") {
			seen = seen || strings.Contains(line, assertion) && strings.Contains(line, "quota-bar keepalive")
		}
		if seen != want {
			t.Fatalf("own sleep assertion visible=%v, want %v", seen, want)
		}
	}
	check(true)
	if status, err := g.sync(false); err != nil || status != "off" {
		t.Fatalf("assertion release: %s %v", status, err)
	}
	check(false)
}

func TestSystemSleepGuardReportsReleaseFailure(t *testing.T) {
	g := &systemSleepGuard{active: true}
	status, err := g.sync(false)
	if err == nil || status != "unavailable" || !g.active {
		t.Fatalf("release failure hidden: status=%s active=%v err=%v", status, g.active, err)
	}
}
