package keepalive

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxProcessStartHandlesCommandNames(t *testing.T) {
	for _, name := range []string{"example", "example worker", "example) (worker)"} {
		stat := "123 (" + name + ") S " + strings.Repeat("0 ", 18) + "12345 0 0\n"
		if start, err := parseLinuxProcessStart([]byte(stat), 123); err != nil || start != "12345" {
			t.Fatalf("name %q: start=%q error=%v", name, start, err)
		}
	}
	for _, stat := range []string{
		"321 (example) S " + strings.Repeat("0 ", 18) + "12345",
		"123 example S " + strings.Repeat("0 ", 18) + "12345",
		"123 (example) S 0",
		"123 (example) S " + strings.Repeat("0 ", 18) + "unknown",
		"123 (example) S " + strings.Repeat("0 ", 18) + "-1",
	} {
		if _, err := parseLinuxProcessStart([]byte(stat), 123); err == nil {
			t.Fatalf("invalid process identity accepted: %q", stat)
		}
	}
}

func TestLinuxRuntimeProcessStartUsesNativeTicks(t *testing.T) {
	process, err := readRuntimeProcess(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.ParseUint(process.Start, 10, 64); err != nil {
		t.Fatalf("Claude Linux registry requires process start ticks, got %q", process.Start)
	}
}
