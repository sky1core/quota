package agentskills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func nodeShim(version string) string {
	return "#!/bin/sh\necho v" + version + "\n"
}

func installBody(names ...string) string {
	return `d=$(pwd)
skills_dir=".agents/skills"
root="$d/$skills_dir"
mkdir -p "$root"
out="["
first=1
for n in ` + strings.Join(names, " ") + `; do
  mkdir -p "$root/$n"
  printf '%s\n' '---' "name: $n" '---' body > "$root/$n/SKILL.md"
  [ $first -eq 0 ] && out="$out,"
  out="$out{\"name\":\"$n\",\"status\":\"installed\",\"path\":\"$root/$n\",\"scope\":\"project\",\"agents\":[\"Codex\"],\"mode\":\"copy\"}"
  first=0
done
out="$out]"
printf '%s\n' "$out"
`
}

func npmShim(body string) string {
	return "#!/bin/sh\n" + body
}

func setShims(t *testing.T, nodeVer, npmBody string, keepPath bool) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	if nodeVer != "" {
		writeExec(t, filepath.Join(bin, "node"), nodeShim(nodeVer))
	}
	if npmBody != "" {
		writeExec(t, filepath.Join(bin, "npm"), npmShim(npmBody))
	}
	path := bin
	if keepPath {
		path = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	t.Setenv("PATH", path)
	return bin
}

func TestFetchSuccessSingle(t *testing.T) {
	setShims(t, "22.20.0", installBody("alpha"), true)

	skills, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil on success")
	}
	if len(skills) != 1 {
		t.Fatalf("want 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "alpha" {
		t.Errorf("name = %q, want alpha", s.Name)
	}
	if !filepath.IsAbs(s.Dir) || filepath.Clean(s.Dir) != s.Dir {
		t.Errorf("Dir %q not clean absolute", s.Dir)
	}
	if filepath.Base(s.Dir) != "alpha" || filepath.Base(filepath.Dir(s.Dir)) != "skills" {
		t.Errorf("Dir %q does not have the expected skill name and parent directory", s.Dir)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove temp project: %v", err)
	}
}

func TestFetchSuccessMultipleDefaultSelector(t *testing.T) {
	setShims(t, "22.20.0", installBody("alpha", "beta"), true)

	skills, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer cleanup()
	if len(skills) != 2 {
		t.Fatalf("want 2 skills, got %d", len(skills))
	}
	got := map[string]bool{}
	for _, s := range skills {
		got[s.Name] = true
	}
	if !got["alpha"] || !got["beta"] {
		t.Errorf("missing skills, got %v", got)
	}
}

func TestFetchLocalRelativeSourceResolved(t *testing.T) {
	argsOut := filepath.Join(t.TempDir(), "src.txt")
	t.Setenv("ARGS_OUT", argsOut)
	body := `prev=""
for a in "$@"; do
  [ "$prev" = "add" ] && printf '%s' "$a" > "$ARGS_OUT"
  prev="$a"
done
` + installBody("alpha")
	setShims(t, "22.20.0", body, true)

	_, cleanup, err := Fetch(context.Background(), "./local-skill", "alpha")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer cleanup()

	got, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("read recorded source: %v", err)
	}
	want, _ := filepath.Abs("./local-skill")
	if string(got) != want {
		t.Errorf("source passed to CLI = %q, want absolute %q", string(got), want)
	}
}

func TestFetchMissingNpm(t *testing.T) {
	setShims(t, "22.20.0", "", false)

	_, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha")
	if err == nil {
		t.Fatal("want error for missing npm")
	}
	if cleanup != nil {
		t.Error("cleanup must be nil on error")
	}
	if !strings.Contains(err.Error(), "npm") {
		t.Errorf("error = %v, want npm mention", err)
	}
}

func TestFetchMissingNode(t *testing.T) {
	setShims(t, "", installBody("alpha"), false)

	_, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha")
	if err == nil {
		t.Fatal("want error for missing node")
	}
	if cleanup != nil {
		t.Error("cleanup must be nil on error")
	}
	if !strings.Contains(err.Error(), "node") {
		t.Errorf("error = %v, want node mention", err)
	}
}

func TestFetchNodeTooOld(t *testing.T) {
	setShims(t, "22.19.9", installBody("alpha"), true)

	_, _, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha")
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("want too-old error, got %v", err)
	}
}

func TestFetchInvalidJSON(t *testing.T) {
	setShims(t, "22.20.0", "echo 'not json'\nexit 1\n", true)

	_, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha")
	if err == nil {
		t.Fatal("want error for invalid JSON")
	}
	if cleanup != nil {
		t.Error("cleanup must be nil on error")
	}
}

func TestFetchPartialFailure(t *testing.T) {
	body := `d=$(pwd)
skills_dir=".agents/skills"
root="$d/$skills_dir"
mkdir -p "$root/alpha"
printf 'x' > "$root/alpha/SKILL.md"
printf '[{"name":"alpha","status":"installed","path":"%s/alpha","scope":"project","agents":["Codex"],"mode":"copy"},{"name":"beta","status":"failed","error":"boom"}]\n' "$root"
exit 1
`
	setShims(t, "22.20.0", body, true)

	_, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "")
	if err == nil {
		t.Fatal("want error for partial failure")
	}
	if cleanup != nil {
		t.Error("cleanup must be nil on error")
	}
	if !strings.Contains(err.Error(), "beta") || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want mention of failed skill", err)
	}
}

func TestFetchNonzeroExitWithSuccessfulJSON(t *testing.T) {
	setShims(t, "22.20.0", installBody("alpha")+"exit 1\n", true)
	_, cleanup, err := Fetch(context.Background(), "example/repository", "alpha")
	if cleanup != nil {
		cleanup()
	}
	if err == nil || cleanup != nil {
		t.Fatal("accepted results from a failed process")
	}
}

func TestFetchEscapingPath(t *testing.T) {
	body := `d=$(pwd)
esc="$d/../escaped-skill"
mkdir -p "$esc"
printf 'x' > "$esc/SKILL.md"
abs=$(cd "$esc" && pwd)
printf '[{"name":"escaped","status":"installed","path":"%s","scope":"project","agents":["Codex"],"mode":"copy"}]\n' "$abs"
`
	setShims(t, "22.20.0", body, true)

	_, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "escaped")
	if err == nil {
		t.Fatal("want error for escaping path")
	}
	if cleanup != nil {
		t.Error("cleanup must be nil on error")
	}
	if !strings.Contains(err.Error(), "not directly under") {
		t.Errorf("error = %v, want escape rejection", err)
	}
}

func TestFetchUnexpectedAgentScopeMode(t *testing.T) {
	cases := []struct{ name, agents, scope, mode string }{
		{"wrong-agent", `["Claude Code"]`, "project", "copy"},
		{"extra-agent", `["Codex","Claude Code"]`, "project", "copy"},
		{"wrong-scope", `["Codex"]`, "global", "copy"},
		{"wrong-mode", `["Codex"]`, "project", "symlink"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `d=$(pwd)
skills_dir=".agents/skills"
root="$d/$skills_dir"
mkdir -p "$root/alpha"
printf 'x' > "$root/alpha/SKILL.md"
printf '[{"name":"alpha","status":"installed","path":"%s/alpha","scope":"` + c.scope + `","agents":` + c.agents + `,"mode":"` + c.mode + `"}]\n' "$root"
`
			setShims(t, "22.20.0", body, true)
			if _, cleanup, err := Fetch(context.Background(), "vercel-labs/agent-skills", "alpha"); err == nil {
				t.Fatalf("want error for %s", c.name)
			} else if cleanup != nil {
				t.Error("cleanup must be nil on error")
			}
		})
	}
}

func TestFetchArgsValidation(t *testing.T) {
	cases := []struct{ source, name, want string }{
		{"", "alpha", "empty"},
		{"-rf", "alpha", "'-'"},
		{"vercel-labs/agent-skills", "../evil", "'.'"},
		{"vercel-labs/agent-skills", "a/b", "separator"},
	}
	for _, c := range cases {
		_, cleanup, err := Fetch(context.Background(), c.source, c.name)
		if err == nil {
			t.Errorf("source=%q name=%q: want error", c.source, c.name)
			continue
		}
		if cleanup != nil {
			t.Errorf("source=%q name=%q: cleanup must be nil", c.source, c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("source=%q name=%q: error %v missing %q", c.source, c.name, err, c.want)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"alpha", "a-b-c", "skill.name", "스킬", "a1_b", "web-design"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", ".", "..", ".hidden", "-lead", "a/b", "a\\b", "a\x00b", "a\tb", "\xff\xfe"}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestParseNodeVersion(t *testing.T) {
	cases := []struct {
		in            string
		maj, min, pat int
		wantErr       bool
	}{
		{"v22.20.0\n", 22, 20, 0, false},
		{"22.20.1", 22, 20, 1, false},
		{"v24.3.0-nightly", 24, 3, 0, false},
		{"v22", 0, 0, 0, true},
		{"notaversion", 0, 0, 0, true},
		{"v22invalid.20.0", 0, 0, 0, true},
		{"v22.20.0unexpected", 0, 0, 0, true},
	}
	for _, c := range cases {
		maj, min, pat, err := parseNodeVersion(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseNodeVersion(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && (maj != c.maj || min != c.min || pat != c.pat) {
			t.Errorf("parseNodeVersion(%q) = %d.%d.%d, want %d.%d.%d", c.in, maj, min, pat, c.maj, c.min, c.pat)
		}
	}
}

func TestFetchNestedSkillsIntegration(t *testing.T) {
	if os.Getenv("QUOTA_SKILLS_INTEGRATION_TEST") != "1" {
		t.Skip("set QUOTA_SKILLS_INTEGRATION_TEST=1 to run the pinned npm CLI")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("npm_config_cache", t.TempDir())
	source := t.TempDir()
	for dir, name := range map[string]string{source: "root-skill", filepath.Join(source, "skills", "child"): "child"} {
		putFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: Synthetic nested fixture.\n---\nExample.\n", 0o644)
	}
	for _, selector := range []string{"", "child"} {
		skills, cleanup, err := Fetch(context.Background(), source, selector)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, skill := range skills {
			names[skill.Name] = true
		}
		cleanup()
		if !names["child"] || selector == "" && (!names["root-skill"] || len(names) != 2) || selector == "child" && len(names) != 1 {
			t.Fatalf("selector %q: discovered %v", selector, names)
		}
	}
}
