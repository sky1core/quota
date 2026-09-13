package overlayruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNativeSettingsUnicodeExclusions(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		matched       bool
	}{
		{"/예제/프로젝트*/CLAUDE.md", "/예제/프로젝트하나/CLAUDE.md", true},
		{"/예제/**/개인?/CLAUDE.local.md", "/예제/하위/개인용/CLAUDE.local.md", true},
		{"/예제/프로젝트*/CLAUDE.md", "/예제/프로젝트/하위/CLAUDE.md", false},
	} {
		expression, err := regexp.Compile(claudeExclusionPattern(tc.pattern))
		if err != nil {
			t.Fatal(err)
		}
		if got := expression.MatchString(tc.path); got != tc.matched {
			t.Fatalf("%q matches %q = %t", tc.pattern, tc.path, got)
		}
	}
	repo := filepath.Join(newRepo(t), "한글저장소")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, "AGENTS.md"), "shared instruction\n")
	if code, out, errb := run(t, "", "setup", "--runtime=claude", repo); code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, errb)
	}
	settings, err := json.Marshal(map[string]any{"claudeMdExcludes": []string{filepath.ToSlash(filepath.Join(filepath.Dir(repo), "한글*", "CLAUDE.md"))}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), string(settings))
	if code, out, errb := run(t, "", "check", "--runtime=claude", repo); code != 1 || !strings.Contains(out+errb, "claudeMdExcludes matches") {
		t.Fatalf("check: %d %s %s", code, out, errb)
	}
}

func TestNativeSettingsMalformedClaudeValues(t *testing.T) {
	for _, tc := range []struct{ name, content, reason string }{
		{"null root", `null`, "JSON object"},
		{"array root", `[]`, "could not read"},
		{"trailing JSON", `{} {}`, "trailing content"},
		{"null disable", `{"disableAllHooks":null}`, "must be a boolean"},
		{"array disable", `{"disableAllHooks":[]}`, "must be a boolean"},
		{"null exclusions", `{"claudeMdExcludes":null}`, "must be an array"},
		{"number exclusion", `{"claudeMdExcludes":[12]}`, "non-empty strings"},
		{"empty exclusion", `{"claudeMdExcludes":[""]}`, "non-empty strings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.md"), "shared instruction\n")
			if code, out, errb := run(t, "", "setup", "--runtime=claude", repo); code != 0 {
				t.Fatalf("setup: %d %s %s", code, out, errb)
			}
			path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
			write(t, path, tc.content)
			if code, out, errb := run(t, "", "check", "--runtime=claude", repo); code != 1 || !strings.Contains(out+errb, tc.reason) {
				t.Fatalf("check: %d %s %s", code, out, errb)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.content {
				t.Fatalf("check changed source settings: %q %v", after, err)
			}
		})
	}
}

func TestNativeSettingsCodexDocumentBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, config, reason string }{
		{"exact byte budget", "project_doc_max_bytes = 6\n", ""},
		{"one byte too small", "project_doc_max_bytes = 5\n", "over Codex project_doc_max_bytes"},
		{"signed maximum", "project_doc_max_bytes = 9223372036854775807\n", ""},
		{"unsigned overflow", "project_doc_max_bytes = 18446744073709551615\n", "could not parse"},
		{"signed overflow", "project_doc_max_bytes = 9223372036854775808\n", "could not parse"},
		{"negative", "project_doc_max_bytes = -1\n", "positive integer"},
		{"float", "project_doc_max_bytes = 6.0\n", "positive integer"},
		{"boolean", "project_doc_max_bytes = true\n", "positive integer"},
		{"empty filenames", "project_doc_fallback_filenames = []\n", ""},
		{"empty filename entry", "project_doc_fallback_filenames = ['']\n", "must be [] or absent"},
		{"wrong filenames type", "project_doc_fallback_filenames = ''\n", "must be [] or absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.md"), "가나")
			if code, out, errb := run(t, "", "setup", "--runtime=codex", repo); code != 0 {
				t.Fatalf("setup: %d %s %s", code, out, errb)
			}
			write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), tc.config)
			want := 0
			if tc.reason != "" {
				want = 1
			}
			if code, out, errb := run(t, "", "check", "--runtime=codex", repo); code != want || !strings.Contains(out+errb, tc.reason) {
				t.Fatalf("check: %d want %d %s %s", code, want, out, errb)
			}
		})
	}
}
