package main

import (
	"bytes"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippetAroundCaseFoldingDoesNotPanic(t *testing.T) {
	text := strings.Repeat("Ⱥ", 400) + " needle tail"
	got := snippetAround(text, "needle", 40)
	if !strings.Contains(got, "needle") {
		t.Fatalf("snippet %q does not contain needle", got)
	}
	if body := strings.Trim(got, "."); utf8.RuneCountInString(body) > 40 {
		t.Fatalf("snippet body exceeds maxChars: %q (%d runes)", got, utf8.RuneCountInString(body))
	}
}

func TestSnippetAroundFoldsQueryAgainstOriginalRunes(t *testing.T) {
	text := strings.Repeat("x", 300) + "ⱥTARGETⱥ" + strings.Repeat("y", 300)
	got := snippetAround(text, "ȺtargetȺ", 20)
	if !strings.Contains(got, "ⱥTARGETⱥ") {
		t.Fatalf("expected original-case segment preserved, got %q", got)
	}
}

func TestSessionLogInterspersedFlagsPreservesTerminator(t *testing.T) {
	got := sessionLogInterspersedFlags(
		[]string{"-limit", "5", "foo", "--", "--amend", "--json"},
		map[string]bool{"limit": true},
	)
	want := []string{"-limit", "5", "--", "foo", "--amend", "--json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSessionLogInterspersedFlagsKeepsLiteralsAfterTerminator(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "")
	limit := fs.Int("limit", 0, "")

	args := sessionLogInterspersedFlags(
		[]string{"-limit", "3", "--", "--json", "amend"},
		map[string]bool{"limit": true},
	)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *jsonOut {
		t.Fatal("--json after -- was consumed as the json flag")
	}
	if *limit != 3 {
		t.Fatalf("limit = %d, want 3", *limit)
	}
	if q := strings.Join(fs.Args(), " "); q != "--json amend" {
		t.Fatalf("query = %q, want %q", q, "--json amend")
	}
}

func TestSessionLogInterspersedFlagsKeepsInterspersedOptions(t *testing.T) {
	got := sessionLogInterspersedFlags(
		[]string{"foo", "-limit", "3", "bar"},
		map[string]bool{"limit": true},
	)
	want := []string{"-limit", "3", "foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSessionLogSearchAcceptsLiteralFlagQueries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	for _, query := range []string{"--amend", "--json"} {
		var out, stderr bytes.Buffer
		if code := sessionLogSearch([]string{"--", query}, &out, &stderr); code != 0 {
			t.Fatalf("query=%q code=%d stderr=%s", query, code, &stderr)
		}
	}
}
