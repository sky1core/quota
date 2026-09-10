package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestParseAutoPromptArgs(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		for _, prompt := range []string{"", "hello\nworld", "--agent=codex", "--", `$(echo literal) "quoted"`} {
			args := []string{"--model", "fable:" + effort, "--model=code-model:high", "--", prompt}
			got, err := parseAutoPromptArgs(args)
			want := autoPromptOptions{models: [2]promptModel{{"fable", effort}, {"code-model", "high"}}, prompt: prompt}
			if err != nil || got != want {
				t.Fatalf("parse %q = %+v, %v; want %+v", args, got, err, want)
			}
		}
	}
}

func TestAutoPromptReadOnly(t *testing.T) {
	models := []string{"--model=fable:high", "--model=code-model:high"}
	for position := 0; position <= len(models); position++ {
		args := append([]string{}, models[:position]...)
		args = append(args, "--read-only")
		args = append(args, models[position:]...)
		args = append(args, "--", "--read-only stays literal in the prompt")
		opts, err := parseAutoPromptArgs(args)
		if err != nil || !opts.readOnly {
			t.Fatalf("parse: %+v %v", opts, err)
		}
		for _, provider := range []string{"claude", "codex"} {
			account := autoPromptAccount{provider: provider, model: promptModel{"example-model", "high"}}
			got := autoPromptArgs(account, opts)
			var want []string
			if provider == "claude" {
				want = []string{"-p", "--model", "example-model", "--effort", "high", "--tools", "Read,Glob,Grep", "--disallowedTools", "mcp__*", "--", opts.prompt}
			} else {
				want = []string{"exec", "--model", "example-model", "-c", `model_reasoning_effort="high"`, "--sandbox", "read-only", "--", opts.prompt}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: %q, want %q", provider, got, want)
			}
		}
	}
	opts, err := parseAutoPromptArgs(append(models, "--", "--read-only"))
	if err != nil || opts.readOnly || opts.prompt != "--read-only" {
		t.Fatalf("prompt interpreted as option: %+v %v", opts, err)
	}
}

func TestAutoPromptRejectsInvalidArgsBeforeIO(t *testing.T) {
	home := autoPromptTestHome(t)
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("invalid config"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--model=fable:high", "--model=code-model:high", "--read-only", "--read-only", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:high", "--read-only=false", "--", "prompt"},
		nil,
		{"--model"},
		{"--model=fable:high", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:low", "--model=other:high", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:low", "prompt"},
		{"--model=fable:high", "--model=code-model:low", "--"},
		{"--model=fable:high", "--model=code-model:low", "--", "one", "two"},
		{"--model=fable:high", "--model=fable:low", "--", "prompt"},
		{"--model=:high", "--model=code-model:low", "--", "prompt"},
		{"--model=  :high", "--model=code-model:low", "--", "prompt"},
		{"--model=fable", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:ultra", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:ultra", "--", "prompt"},
		{"--model=fable:HIGH", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:high:low", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:high", "--model", "--", "prompt"},
		{"--model=fable:high", "--effort=low", "--model=code-model:low", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:low", "--unknown", "--", "prompt"},
		{"--model=fable:high", "--model=code-model:low", "--agent=claude", "--", "prompt"},
		{"--agent=other", "prompt"},
		{"--agent", "codex", "prompt"},
		{"-m", "fable:high", "--model=code-model:low", "--", "prompt"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseAutoPromptArgs(args); err == nil {
				t.Fatal("parser accepted invalid arguments")
			}
			if code := runExecPrompt(args); code != 2 {
				t.Fatalf("exit = %d, want parse failure 2 before invalid config is read", code)
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(home, ".config", "quota"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("unexpected I/O artifacts: %v, %v", entries, err)
	}
}

func TestClassifyAutoPromptModelsExactUnion(t *testing.T) {
	catalogs := []accountModels{
		autoPromptTestCatalog("codex", modelcatalog.Model{ID: "code-model", ResolvedModel: "resolved-model", DisplayName: "display-model"}),
		autoPromptTestCatalog("codex-2", modelcatalog.Model{ID: "hidden-model", Hidden: true}),
	}
	for _, model := range []string{"code-model", "hidden-model"} {
		for _, other := range []string{"fable", "code-model-suffix", "code", "CODE-MODEL", "resolved-model", "display-model"} {
			opts := autoPromptOptions{models: [2]promptModel{{model, "low"}, {other, "max"}}}
			for range 2 {
				got, err := classifyAutoPromptModels(opts, catalogs)
				if err != nil || got["codex"] != (promptModel{model, "low"}) || got["claude"] != (promptModel{other, "max"}) {
					t.Fatalf("classification = %+v, %v", got, err)
				}
				opts.models[0], opts.models[1] = opts.models[1], opts.models[0]
			}
		}
	}
	for _, models := range [][2]promptModel{
		{{"code-model", "high"}, {"hidden-model", "low"}},
		{{"fable", "high"}, {"other", "low"}},
	} {
		if _, err := classifyAutoPromptModels(autoPromptOptions{models: models}, catalogs); err == nil {
			t.Fatalf("accepted same-provider models %v", models)
		}
	}
}

func TestAutoPromptCatalogFailuresAbort(t *testing.T) {
	autoPromptTestHome(t)
	valid := autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))
	for _, catalogs := range [][]accountModels{
		nil,
		{{Account: "codex", Provider: "codex"}},
		{autoPromptTestCatalog("codex")},
		{{Account: "codex", Provider: "claude", Catalog: valid.Catalog}},
		{valid, {Account: "codex-2", Provider: "codex", Error: "catalog unavailable"}},
		{{Account: "codex", Provider: "codex", Catalog: valid.Catalog, Error: "refresh failed"}},
	} {
		if _, err := selectAutoPromptAccount(config.Config{}, autoPromptTestOptions(), catalogs, time.Now()); err == nil || !strings.Contains(err.Error(), "catalog") {
			t.Fatalf("catalog failure did not abort: %v", err)
		}
	}
	cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/extra-codex"}}}
	if _, err := selectAutoPromptAccount(cfg, autoPromptTestOptions(), []accountModels{valid}, time.Now()); err == nil || !strings.Contains(err.Error(), "codex-2: missing") {
		t.Fatalf("missing configured catalog error = %v", err)
	}
	catalogs, err := loadAutoPromptCatalogs(cfg)
	if err != nil || len(catalogs) != 2 {
		t.Fatalf("catalog results = %+v, %v", catalogs, err)
	}
	for _, catalog := range catalogs {
		if catalog.Provider != "codex" || catalog.Error == "" || catalog.Catalog != nil {
			t.Fatalf("unavailable CLI did not fail for each configured account: %+v", catalog)
		}
	}
}

func TestCatalogSupportsPromptModel(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name  string
		model modelcatalog.Model
		want  bool
	}{
		{"exact", autoPromptTestModel("code-model", "high"), true},
		{"unknown", modelcatalog.Model{ID: "code-model"}, false},
		{"unknown capability", modelcatalog.Model{ID: "code-model", SupportedEfforts: []string{"high"}}, false},
		{"unsupported", modelcatalog.Model{ID: "code-model", SupportsEffort: &no, SupportedEfforts: []string{"high"}}, false},
		{"missing effort", autoPromptTestModel("code-model"), false},
		{"different effort", autoPromptTestModel("code-model", "low"), false},
		{"effort case", autoPromptTestModel("code-model", "HIGH"), false},
		{"different model", autoPromptTestModel("code-model-extra", "high"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := catalogSupportsPromptModel(modelcatalog.Snapshot{Models: []modelcatalog.Model{tc.model}}, promptModel{"code-model", "high"})
			if got != tc.want {
				t.Fatalf("supported = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAutoPromptAccountMembershipAndEligibility(t *testing.T) {
	home := autoPromptTestHome(t)
	cfg := config.Config{
		ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "~/extra-claude"}},
		CodexAccounts: []config.CodexAccount{
			{Key: "codex-2", Home: "~/extra-codex"},
			{Key: "codex-3", Home: "~/unknown-effort"},
			{Key: "codex-4", Home: "~/other-effort"},
		},
	}
	catalogs := []accountModels{
		autoPromptTestCatalog("codex", autoPromptTestModel("other-code", "high")),
		autoPromptTestCatalog("codex-2", autoPromptTestModel("code-model", "high")),
		autoPromptTestCatalog("codex-3", modelcatalog.Model{ID: "code-model"}),
		autoPromptTestCatalog("codex-4", autoPromptTestModel("code-model", "low")),
	}
	accounts, err := autoPromptAccounts(cfg, autoPromptTestOptions(), catalogs)
	if err != nil || len(accounts) != 3 || accounts[0].key != "claude" || accounts[1].key != "claude-2" || accounts[2].key != "codex-2" {
		t.Fatalf("eligible accounts = %+v, %v", accounts, err)
	}
	if accounts[1].dir != filepath.Join(home, "extra-claude") || accounts[2].dir != filepath.Join(home, "extra-codex") {
		t.Fatalf("account directories = %+v", accounts)
	}
	putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 70% used")
	putAutoPromptQuota(t, "claude", accounts[1].dir, "Current week (all models): 50% used")
	putAutoPromptQuota(t, "codex", accounts[2].dir, autoPromptCodexQuota(80, -1))
	selected, err := selectAutoPromptAccount(cfg, autoPromptTestOptions(), catalogs, time.Now())
	if err != nil || selected != accounts[2] {
		t.Fatalf("selection = %+v, %v; want %+v", selected, err, accounts[2])
	}
}

func TestAutoPromptQuotaScoresAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claude   string
		codex    string
		floor    float64
		selected string
	}{
		{"aggregate beats model row", "Current session: 10% used\nCurrent week (all models): 60% used\nCurrent week (Fable): 0% used", autoPromptCodexQuota(70, 90), 5, "codex"},
		{"both session only", "Current session: 10% used", `{"rateLimits":{"primary":{"windowDurationMins":300,"usedPercent":75}}}`, 5, "claude"},
		{"both session only codex higher", "Current session: 75% used", `{"rateLimits":{"primary":{"windowDurationMins":300,"usedPercent":10}}}`, 5, "codex"},
		{"model quota admission", "Current week (all models): 0% used\nCurrent week (Fable): 96% used", autoPromptCodexQuota(20, -1), 5, "codex"},
		{"model quota boundary", "Current week (all models): 0% used\nCurrent week (Fable): 95% used", autoPromptCodexQuota(20, -1), 5, "claude"},
		{"no model row", "Current week (all models): 20% used", autoPromptCodexQuota(70, -1), 5, "claude"},
		{"claude session below 25", "Current session: 76% used\nCurrent week (all models): 0% used", autoPromptCodexQuota(20, -1), 5, "codex"},
		{"claude session at 25", "Current session: 75% used\nCurrent week (all models): 0% used", autoPromptCodexQuota(20, -1), 5, "claude"},
		{"codex session below 25", "Current week (all models): 60% used", autoPromptCodexQuota(90, 24), 5, "claude"},
		{"codex session at 25", "Current week (all models): 60% used", autoPromptCodexQuota(90, 25), 5, "codex"},
		{"weekly only below floor", "Current week (all models): 96% used", autoPromptCodexQuota(4, -1), 5, ""},
		{"weekly only at floor", "Current week (all models): 95% used", autoPromptCodexQuota(5, -1), 5, "claude"},
		{"weekly only custom floor", "Current week (all models): 61% used", autoPromptCodexQuota(40, -1), 40, "codex"},
		{"custom floor rejects session", "Current session: 61% used\nCurrent week (all models): 0% used", autoPromptCodexQuota(50, -1), 40, "codex"},
		{"common session tiebreak", "Current session: 50% used\nCurrent week (all models): 50% used", autoPromptCodexQuota(50, 90), 5, "codex"},
		{"codex session absent", "Current session: 10% used\nCurrent week (all models): 50% used", autoPromptCodexQuota(50, -1), 5, "claude"},
		{"claude session absent", "Current week (all models): 50% used", autoPromptCodexQuota(50, 90), 5, "claude"},
		{"tie", "Current session: 50% used\nCurrent week (all models): 50% used", autoPromptCodexQuota(50, 50), 5, "claude"},
		{"claude incomplete session", "Current session: unavailable\nCurrent week (all models): 0% used", autoPromptCodexQuota(50, -1), 5, "codex"},
		{"codex incomplete session", "Current week (all models): 50% used", `{"rateLimits":{"primary":{"windowDurationMins":300},"secondary":{"windowDurationMins":10080,"usedPercent":0}}}`, 5, "claude"},
		{"claude probe failure", "", autoPromptCodexQuota(50, -1), 5, "codex"},
		{"codex probe failure", "Current week (all models): 50% used", "", 5, "claude"},
		{"both probes fail", "", "", 5, ""},
		{"unlike session duration", "Current session: 50% used\nCurrent week (all models): 50% used", `{"rateLimits":{"primary":{"windowDurationMins":600,"usedPercent":0},"secondary":{"windowDurationMins":10080,"usedPercent":50}}}`, 5, "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := autoPromptTestHome(t)
			if tc.claude != "" {
				putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), tc.claude)
			}
			if tc.codex != "" {
				putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), tc.codex)
			}
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
				"claude": {MinLeftPct: &tc.floor}, "codex": {MinLeftPct: &tc.floor},
			}}}
			catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
			opts := autoPromptTestOptions()
			for range 2 {
				selected, err := selectAutoPromptAccount(cfg, opts, catalogs, time.Now())
				if tc.selected == "" {
					if err == nil || !strings.Contains(err.Error(), "no account has usable quota") {
						t.Fatalf("expected no usable quota, got %+v, %v", selected, err)
					}
				} else if err != nil || selected.key != tc.selected {
					t.Fatalf("selection = %+v, %v; want %s", selected, err, tc.selected)
				}
				opts.models[0], opts.models[1] = opts.models[1], opts.models[0]
			}
		})
	}
}

func TestAutoPromptRejectsIncomparablePeriods(t *testing.T) {
	home := autoPromptTestHome(t)
	putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 10% used")
	putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), `{"rateLimits":{"primary":{"windowDurationMins":300,"usedPercent":10}}}`)
	catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
	_, err := selectAutoPromptAccount(config.Config{}, autoPromptTestOptions(), catalogs, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no common quota window") {
		t.Fatalf("incomparable periods accepted: %v", err)
	}
}

func TestAutoPromptArgs(t *testing.T) {
	prompt := "--agent=other\nquotes ' \" and $() stay literal"
	for _, tc := range []struct {
		provider string
		model    promptModel
		want     []string
	}{
		{"claude", promptModel{"fable", "max"}, []string{"-p", "--model", "fable", "--effort", "max", "--", prompt}},
		{"codex", promptModel{"code-model", "high"}, []string{"exec", "--model", "code-model", "-c", `model_reasoning_effort="high"`, "--", prompt}},
	} {
		args := autoPromptArgs(autoPromptAccount{provider: tc.provider, model: tc.model}, autoPromptOptions{prompt: prompt})
		if !reflect.DeepEqual(args, tc.want) {
			t.Fatalf("%s argv = %q; want %q", tc.provider, args, tc.want)
		}
		if got := delegatedArgv(tc.provider, nil, args); !reflect.DeepEqual(got, append([]string{tc.provider}, tc.want...)) {
			t.Fatalf("delegated argv = %q", got)
		}
	}
}

func TestAutoPromptQuotaResetRatesAndReserves(t *testing.T) {
	for _, tc := range []struct {
		name        string
		claude      string
		codexLeft   int
		codexReset  time.Duration
		claudeFloor float64
		codexFloor  float64
		selected    string
	}{
		{"common reset rates", "Current week (all models): 60% used - resets in 1h", 90, 24 * time.Hour, 5, 5, "claude"},
		{"unknown Claude reset", "Current week (all models): 60% used", 90, 24 * time.Hour, 5, 5, "codex"},
		{"unknown Codex reset", "Current week (all models): 60% used - resets in 1h", 90, 0, 5, 5, "codex"},
		{"Claude reserve", "Current week (all models): 10% used", 50, 0, 80, 5, "codex"},
		{"Codex reserve", "Current week (all models): 50% used", 90, 0, 5, 80, "claude"},
		{"ignore model reset score", "Current week (all models): 60% used - resets in 2d\nCurrent week (Fable): 0% used - resets in 1h", 80, 24 * time.Hour, 5, 5, "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := autoPromptTestHome(t)
			now := time.Now()
			reset := ""
			if tc.codexReset > 0 {
				reset = fmt.Sprintf(`,"resetsAt":%d`, now.Add(tc.codexReset).Unix())
			}
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), tc.claude)
			putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), fmt.Sprintf(`{"rateLimits":{"primary":{"windowDurationMins":10080,"usedPercent":%d%s}}}`, 100-tc.codexLeft, reset))
			cfg := config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{
				"claude": {MinLeftPct: &tc.claudeFloor}, "codex": {MinLeftPct: &tc.codexFloor},
			}}}
			catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
			opts := autoPromptTestOptions()
			for range 2 {
				selected, err := selectAutoPromptAccount(cfg, opts, catalogs, now)
				if err != nil || selected.key != tc.selected {
					t.Fatalf("selection = %+v, %v; want %s", selected, err, tc.selected)
				}
				opts.models[0], opts.models[1] = opts.models[1], opts.models[0]
			}
		})
	}
}

func TestAutoPromptCommonSessionAcrossAllEligibleAccounts(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		t.Run(fmt.Sprint(eligible), func(t *testing.T) {
			home := autoPromptTestHome(t)
			cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/weekly-only"}}}
			model := autoPromptTestModel("other-code", "high")
			if eligible {
				model = autoPromptTestModel("code-model", "high")
			}
			catalogs := []accountModels{
				autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high")),
				autoPromptTestCatalog("codex-2", model),
			}
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current session: 70% used\nCurrent week (all models): 50% used")
			putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(50, 90))
			putAutoPromptQuota(t, "codex", filepath.Join(home, "weekly-only"), autoPromptCodexQuota(20, -1))
			want := "codex"
			if eligible {
				want = "claude"
			}
			selected, err := selectAutoPromptAccount(cfg, autoPromptTestOptions(), catalogs, time.Now())
			if err != nil || selected.key != want {
				t.Fatalf("selection = %+v, %v; want %s", selected, err, want)
			}
		})
	}
}

func TestAutoPromptAccountTieUsesConfigOrder(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			home := autoPromptTestHome(t)
			cfg := config.Config{}
			catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 90% used")
			putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(10, -1))
			for _, suffix := range []string{"9", "2"} {
				key := provider + "-" + suffix
				dir := filepath.Join(home, key)
				if provider == "claude" {
					cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{Key: key, ConfigDir: dir})
					putAutoPromptQuota(t, provider, dir, "Current week (all models): 10% used")
				} else {
					cfg.CodexAccounts = append(cfg.CodexAccounts, config.CodexAccount{Key: key, Home: dir})
					catalogs = append(catalogs, autoPromptTestCatalog(key, autoPromptTestModel("code-model", "high")))
					putAutoPromptQuota(t, provider, dir, autoPromptCodexQuota(90, -1))
				}
			}
			opts := autoPromptTestOptions()
			for range 2 {
				selected, err := selectAutoPromptAccount(cfg, opts, catalogs, time.Now())
				if err != nil || selected.key != provider+"-9" {
					t.Fatalf("selection = %+v, %v; want first configured account", selected, err)
				}
				opts.models[0], opts.models[1] = opts.models[1], opts.models[0]
			}
		})
	}
}

func TestAutoPromptInvalidAccountConfiguration(t *testing.T) {
	autoPromptTestHome(t)
	for _, cfg := range []config.Config{
		{ClaudeAccounts: []config.ClaudeAccount{{Key: "invalid", ConfigDir: "~/extra"}}},
		{CodexAccounts: []config.CodexAccount{{Key: "codex-2"}}},
		{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{"invalid": {}}}},
		{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{"claude-2": {}}}},
		{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{"codex-2": {}}}},
		{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{"claude": {MinLeftPct: floatPtr(-1)}}}},
		{ExecPrompt: &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{"codex": {MinLeftPct: floatPtr(101)}}}},
	} {
		catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
		if _, err := autoPromptAccounts(cfg, autoPromptTestOptions(), catalogs); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func autoPromptTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

func autoPromptTestOptions() autoPromptOptions {
	return autoPromptOptions{models: [2]promptModel{{"fable", "max"}, {"code-model", "high"}}, prompt: "prompt"}
}

func autoPromptTestModel(id string, efforts ...string) modelcatalog.Model {
	yes := true
	return modelcatalog.Model{ID: id, SupportsEffort: &yes, SupportedEfforts: efforts}
}

func autoPromptTestCatalog(key string, models ...modelcatalog.Model) accountModels {
	return accountModels{Account: key, Provider: "codex", Catalog: &modelcatalog.Snapshot{Provider: "codex", Models: models}}
}

func putAutoPromptQuota(t *testing.T, provider, dir, raw string) {
	t.Helper()
	key := provider + ":" + dir
	quotacache.Put(key, raw, time.Now().Add(time.Hour))
	if _, ok := quotacache.Get(key, cliCacheMaxAge); !ok {
		t.Fatal("synthetic quota cache entry was not saved")
	}
}

func autoPromptCodexQuota(weeklyLeft, sessionLeft int) string {
	short := "null"
	if sessionLeft >= 0 {
		short = fmt.Sprintf(`{"windowDurationMins":300,"usedPercent":%d}`, 100-sessionLeft)
	}
	return fmt.Sprintf(`{"rateLimits":{"primary":%s,"secondary":{"windowDurationMins":10080,"usedPercent":%d}}}`, short, 100-weeklyLeft)
}
