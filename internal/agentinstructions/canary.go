package agentinstructions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sky1core/quota/internal/childprocess"
	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/overlayruntime"
)

func VerifyCanary(ctx context.Context, runtime string) error {
	if err := overlayruntime.ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	switch runtime {
	case "claude":
		return verifyClaudeCanary(ctx)
	case "codex":
		return verifyCodexCanary(ctx)
	default:
		return fmt.Errorf("instructions canary: unsupported runtime %q (want claude or codex)", runtime)
	}
}

const (
	canaryLabelShared = "QUOTA-INSTRUCTIONS-CANARY-SHARED"
	canaryLabelLocal  = "QUOTA-INSTRUCTIONS-CANARY-LOCAL"
)

const canaryPrompt = "Two label lines may appear in the instructions loaded into your context: " +
	`"` + canaryLabelShared + `:" and "` + canaryLabelLocal + `:". ` +
	"For every occurrence of a label line in your loaded instructions, take the token that follows the colon. " +
	"Repeat the token if that label line occurs more than once; do not deduplicate. " +
	"Reply with all shared-label tokens first, then all local-label tokens, separated by single spaces. " +
	"If neither label line is present, output exactly: NONE. " +
	"Do not use any tools, do not read or open any files, and output nothing except the token(s) or NONE."

func verifyClaudeCanary(ctx context.Context) error {
	shared, local, err := newMarkers()
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}

	posDir, cleanup, err := newCanaryRepo("claude-pos")
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	defer cleanup()
	if err := writeMarkerFile(posDir, sharedRuleFile, markerLine(canaryLabelShared, shared)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := writeMarkerFile(posDir, localRuleFile, markerLine(canaryLabelLocal, local)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := gitInit(ctx, posDir); err != nil {
		return err
	}
	if err := runSetup(ctx, posDir, "claude"); err != nil {
		return err
	}
	out, err := runClaudeCanary(ctx, posDir)
	if err != nil {
		return fmt.Errorf("claude instructions canary positive: %w", err)
	}
	if err := checkClaudeCanary(out, joinMarkers(shared, local), "positive"); err != nil {
		return err
	}

	negDir, cleanupNeg, err := newCanaryRepo("claude-neg")
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	defer cleanupNeg()
	if err := writeMarkerFile(negDir, sharedRuleFile, markerLine(canaryLabelShared, shared)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := writeMarkerFile(negDir, localRuleFile, markerLine(canaryLabelLocal, local)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := gitInit(ctx, negDir); err != nil {
		return err
	}
	outNeg, err := runClaudeCanary(ctx, negDir)
	if err != nil {
		return fmt.Errorf("claude instructions canary negative control: %w", err)
	}
	return checkClaudeCanary(outNeg, canaryNone, "negative control", shared, local)
}

func verifyCodexCanary(ctx context.Context) error {
	shared, local, err := newMarkers()
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}

	posDir, cleanup, err := newCanaryRepo("codex-pos")
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	defer cleanup()
	if err := writeMarkerFile(posDir, sharedRuleFile, markerLine(canaryLabelShared, shared)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := writeMarkerFile(posDir, localRuleFile, markerLine(canaryLabelLocal, local)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := gitInit(ctx, posDir); err != nil {
		return err
	}
	if err := runSetup(ctx, posDir, "codex"); err != nil {
		return err
	}
	out, err := runCodexCanary(ctx, posDir)
	if err != nil {
		return fmt.Errorf("codex instructions canary positive: %w", err)
	}
	if err := checkCodexCanary(out, joinMarkers(shared, local), "positive"); err != nil {
		return err
	}

	negDir, cleanupNeg, err := newCanaryRepo("codex-neg")
	if err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	defer cleanupNeg()
	if err := writeMarkerFile(negDir, sharedRuleFile, markerLine(canaryLabelShared, shared)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := writeMarkerFile(negDir, codexStrayLocalFile, markerLine(canaryLabelLocal, local)); err != nil {
		return fmt.Errorf("instructions canary: %w", err)
	}
	if err := gitInit(ctx, negDir); err != nil {
		return err
	}
	outNeg, err := runCodexCanary(ctx, negDir)
	if err != nil {
		return fmt.Errorf("codex instructions canary negative control: %w", err)
	}
	return checkCodexCanary(outNeg, shared, "negative control", local)
}

const (
	sharedRuleFile      = "AGENTS.md"
	localRuleFile       = "AGENTS.local.md"
	codexStrayLocalFile = "NOTES.md"
	canaryNone          = "NONE"
)

func newMarkers() (shared, local string, err error) {
	if shared, err = randToken(); err != nil {
		return "", "", err
	}
	if local, err = randToken(); err != nil {
		return "", "", err
	}
	return shared, local, nil
}

func randToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate canary token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func markerLine(label, value string) string {
	return fmt.Sprintf("# instructions canary\n\n%s: %s\n", label, value)
}

func joinMarkers(shared, local string) string { return shared + " " + local }

func markersEqual(got, want string) bool {
	return strings.Join(strings.Fields(got), " ") == want
}

func canaryMarkerDiagnostics(got, want string, forbidden []string) string {
	expected := strings.Fields(want)
	if want == canaryNone {
		expected = nil
	}
	observed := strings.Fields(got)
	counts := map[string]int{}
	for _, token := range observed {
		counts[token]++
	}
	expectedCounts := make([]int, len(expected))
	allowed := map[string]bool{}
	for index, token := range expected {
		expectedCounts[index] = counts[token]
		allowed[token] = true
	}
	if want == canaryNone {
		allowed[canaryNone] = true
	}
	unexpected := 0
	for _, token := range observed {
		if !allowed[token] {
			unexpected++
		}
	}
	noneOnly := len(observed) == 1 && observed[0] == canaryNone
	diagnostics := fmt.Sprintf("expected_tokens=%d observed_expected=%v unexpected_tokens=%d none_only=%t", len(expected), expectedCounts, unexpected, noneOnly)
	if len(forbidden) > 0 {
		forbiddenCounts := make([]int, len(forbidden))
		for index, token := range forbidden {
			forbiddenCounts[index] = counts[token]
		}
		diagnostics += fmt.Sprintf(" observed_forbidden=%v", forbiddenCounts)
	}
	return diagnostics
}

func canaryBaseDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	base := filepath.Join(cache, "quota", "instructions-verify")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("create canary base dir: %w", err)
	}
	return base, nil
}

func newCanaryRepo(label string) (string, func(), error) {
	base, err := canaryBaseDir()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(base, label+"-")
	if err != nil {
		return "", nil, fmt.Errorf("create canary repo: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func writeMarkerFile(dir, name, content string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func gitInit(ctx context.Context, dir string) error {
	if err := overlayruntime.ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	cmd := childprocess.CommandContext(ctx, "git", "init")
	cmd.Dir = dir
	if err := childprocess.Run(cmd); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("instructions canary: %w", ctx.Err())
		}
		return fmt.Errorf("instructions canary: git init failed: %w", err)
	}
	return nil
}

func runSetup(ctx context.Context, dir, runtime string) error {
	if runtime == "codex" {
		plan, _, err := overlayruntime.PlanNativeCodexRepository(ctx, dir, runtime, "", nil, nil)
		if err != nil {
			return fmt.Errorf("instructions canary: %w", err)
		}
		validated, err := PreflightNativeCodex(ctx, plan)
		if err != nil {
			return fmt.Errorf("instructions canary: %w", err)
		}
		if err := validated.Apply(ctx, func() error { return nil }); err != nil {
			return fmt.Errorf("instructions canary: %w", err)
		}
		return nil
	}
	if code := overlayruntime.Run(ctx, []string{"setup", "--runtime=" + runtime, dir}, nil, io.Discard, io.Discard); code != 0 {
		if ctx.Err() != nil {
			return fmt.Errorf("instructions canary: %w", ctx.Err())
		}
		return fmt.Errorf("instructions canary: instruction setup failed (exit %d)", code)
	}
	return nil
}

func runClaudeCanary(ctx context.Context, dir string) ([]byte, error) {
	bin, err := claude.FindBinary()
	if err != nil {
		return nil, fmt.Errorf("instructions canary: %w", err)
	}
	args := []string{
		"-p", canaryPrompt,
		"--output-format", "json",
	}
	cmd := childprocess.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = claudeCanaryEnv(os.Environ())
	return runCanaryCommand(ctx, cmd)
}

func claudeCanaryEnv(base []string) []string {
	env := make([]string, 0, len(base))
	for _, kv := range base {
		if !strings.HasPrefix(kv, "CLAUDECODE=") {
			env = append(env, kv)
		}
	}
	return env
}

func runCanaryCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := childprocess.Run(cmd)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("instructions canary: native CLI: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("instructions canary: native CLI failed: %w", err)
	}
	return stdout.Bytes(), nil
}

type claudeEnvelope struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	IsError  *bool  `json:"is_error"`
	NumTurns int    `json:"num_turns"`
	Result   string `json:"result"`
}

func checkClaudeCanary(raw []byte, want, phase string, forbidden ...string) error {
	var env claudeEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		return fmt.Errorf("claude instructions canary %s: unreadable result envelope", phase)
	}
	if env.Type != "result" || env.Subtype != "success" || env.IsError == nil || *env.IsError {
		return fmt.Errorf("claude instructions canary %s: run did not report explicit success", phase)
	}
	if env.NumTurns != 1 {
		return fmt.Errorf("claude instructions canary %s: expected exactly one turn, got %d", phase, env.NumTurns)
	}
	if !markersEqual(env.Result, want) {
		return fmt.Errorf("claude instructions canary %s: delivered instruction markers did not match expectation (%s)", phase, canaryMarkerDiagnostics(env.Result, want, forbidden))
	}
	return nil
}

func runCodexCanary(ctx context.Context, dir string) ([]byte, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, errors.New("instructions canary: codex CLI not found in PATH")
	}
	args := []string{"exec", "--json", "--ephemeral", "-s", "read-only", "-C", dir, canaryPrompt}
	cmd := childprocess.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	return runCanaryCommand(ctx, cmd)
}

type codexEvent struct {
	Type string          `json:"type"`
	Item *codexItem      `json:"item"`
	Err  json.RawMessage `json:"error"`
}

type codexItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseCodexCanary(raw []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var final string
	threadStarted, turnStarted, turnCompleted, sawMessage := false, false, false, false
	pending := make(map[string]string)
	completedItems := make(map[string]bool)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
			return "", errors.New("malformed codex event stream")
		}
		if turnCompleted || (len(ev.Err) != 0 && string(ev.Err) != "null") {
			return "", errors.New("codex emitted an error or an event after turn completion")
		}
		switch ev.Type {
		case "thread.started":
			if threadStarted || turnStarted {
				return "", errors.New("codex emitted an unexpected thread start")
			}
			threadStarted = true
		case "turn.started":
			if turnStarted {
				return "", errors.New("codex emitted multiple turns")
			}
			turnStarted = true
		case "item.started", "item.updated", "item.completed":
			if !turnStarted || ev.Item == nil || ev.Item.ID == "" {
				return "", errors.New("codex emitted an item without an active turn or identity")
			}
			item := ev.Item
			if item.Type != "agent_message" && item.Type != "reasoning" {
				return "", errors.New("codex emitted a tool or unsupported item")
			}
			kind, active := pending[item.ID]
			if completedItems[item.ID] || (active && kind != item.Type) {
				return "", errors.New("codex emitted an inconsistent item")
			}
			switch ev.Type {
			case "item.started":
				if active {
					return "", errors.New("codex started the same item twice")
				}
				pending[item.ID] = item.Type
			case "item.updated":
				if !active {
					return "", errors.New("codex updated an item that was not started")
				}
			case "item.completed":
				delete(pending, item.ID)
				completedItems[item.ID] = true
				if item.Type == "agent_message" {
					sawMessage = true
					final = item.Text
				}
			}
		case "turn.completed":
			if !turnStarted || len(pending) != 0 || !sawMessage || strings.TrimSpace(final) == "" {
				return "", errors.New("codex completed without a finished final message")
			}
			turnCompleted = true
		default:
			return "", errors.New("codex emitted a failure or unsupported event")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", errors.New("could not read codex event stream")
	}
	if !turnCompleted {
		return "", errors.New("codex turn did not complete")
	}
	return final, nil
}

func checkCodexCanary(raw []byte, want, phase string, forbidden ...string) error {
	final, err := parseCodexCanary(raw)
	if err != nil {
		return fmt.Errorf("codex instructions canary %s: %w", phase, err)
	}
	if !markersEqual(final, want) {
		return fmt.Errorf("codex instructions canary %s: delivered instruction markers did not match expectation (%s)", phase, canaryMarkerDiagnostics(final, want, forbidden))
	}
	return nil
}
