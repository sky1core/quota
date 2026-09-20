package codex

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnvForHomeSeparatesRuntimeSettingsFromGeneralEnvironment(t *testing.T) {
	general := []string{"HOME=/account-home", "PATH=/bin", "TMPDIR=/tmp", "LANG=C.UTF-8",
		"HTTPS_PROXY=http://proxy.invalid:8080", "NO_PROXY=localhost", "SSL_CERT_FILE=/cert.pem",
		"CODEX_CA_CERTIFICATE=/codex-cert.pem", "RUST_LOG=warn", "UNRELATED=value"}
	base := append(append([]string{}, general...), "CODEX_HOME=/caller", "CODEX_SESSIONS_DIR=/caller-sessions",
		"CODEX_FUTURE_SETTING=caller", "OPENAI_FUTURE_SETTING=caller", "OPENAI_API_KEY=caller-key")
	before := append([]string{}, base...)
	for _, home := range []string{"", "/selected"} {
		want := append([]string{}, general...)
		if home != "" {
			want = append(want, "CODEX_HOME="+home)
		}
		if got := EnvForHome(base, home); !reflect.DeepEqual(got, want) {
			t.Fatalf("environment for %q = %v, want %v", home, got, want)
		}
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatal("caller environment was modified")
	}
}

func TestEnvForHomeSelectsAccountAndScrubsOverrides(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"CODEX_HOME=/old-one",
		"CODEX_HOME=/old-two",
		"CODEX_ACCESS_TOKEN=secret",
		"CODEX_API_KEY=secret",
		"CODEX_AUTH=secret",
		"CODEX_AUTHAPI_BASE_URL=https://example.invalid",
		"CODEX_URL=https://example.invalid",
		"OPENAI_API_KEY=secret",
		"OPENAI_BASE_URL=https://example.invalid",
		"OPENAI_ORGANIZATION=org",
		"OPENAI_PROJECT=project",
		"CODEX_SQLITE_HOME=/caller-state",
	}
	env := EnvForHome(base, "/selected")

	if got := countEnv(env, "CODEX_HOME", "/selected"); got != 1 {
		t.Fatalf("selected CODEX_HOME assignments = %d, want 1", got)
	}
	for _, key := range []string{
		"CODEX_ACCESS_TOKEN", "CODEX_API_KEY", "CODEX_AUTH", "CODEX_AUTHAPI_BASE_URL", "CODEX_URL",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORGANIZATION", "OPENAI_PROJECT", "CODEX_SQLITE_HOME",
	} {
		if countEnv(env, key, "") != 0 {
			t.Fatalf("%s must be removed", key)
		}
	}
	if countEnv(env, "PATH", "/bin") != 1 {
		t.Fatal("unrelated environment variables must be preserved")
	}
}

func TestEnvForHomeDropsInheritedDefault(t *testing.T) {
	env := EnvForHome([]string{"CODEX_HOME=/inherited", "OPENAI_API_KEY=secret"}, "")
	if countEnv(env, "CODEX_HOME", "") != 0 {
		t.Fatal("empty home must not preserve inherited CODEX_HOME")
	}
	if countEnv(env, "OPENAI_API_KEY", "") != 0 {
		t.Fatal("auth overrides must still be removed for the default account")
	}
}

func countEnv(env []string, key, value string) int {
	count := 0
	for _, item := range env {
		itemKey, itemValue, ok := strings.Cut(item, "=")
		if ok && itemKey == key && (value == "" || itemValue == value) {
			count++
		}
	}
	return count
}
