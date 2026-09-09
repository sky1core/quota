package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	commandTimeout     = time.Minute
	maxMessageBytes    = 2 * 1024 * 1024
	maxDiagnosticBytes = 4096
)

type boundedOutput struct {
	data      []byte
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := min(len(p), maxDiagnosticBytes-len(b.data))
	b.data = append(b.data, p[:n]...)
	b.truncated = b.truncated || n < len(p)
	return len(p), nil
}

func command(ctx context.Context, target Target, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(target.Binary) {
		return nil, errors.New("model discovery requires an absolute binary path")
	}
	if target.Env == nil {
		return nil, errors.New("model discovery requires explicit child environment")
	}
	cmd := exec.CommandContext(ctx, target.Binary, args...)
	cmd.Env = append([]string{}, target.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd, nil
}

func processError(ctx context.Context, stage string, err error, stderr *boundedOutput) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", stage, ctx.Err())
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("%s: %w (stderr captured: %d bytes, truncated: %t)", stage, err, len(stderr.data), stderr.truncated)
}

func CLIVersion(ctx context.Context, target Target) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd, err := command(ctx, target, "--version")
	if err != nil {
		return "", err
	}
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", processError(ctx, "CLI version", err, &stderr)
	}
	if ctx.Err() != nil {
		return "", processError(ctx, "CLI version", ctx.Err(), &stderr)
	}
	version := strings.TrimSpace(string(stdout.data))
	if stdout.truncated || version == "" || strings.ContainsAny(version, "\r\n\x00\x1b") {
		return "", errors.New("invalid CLI version output")
	}
	return version, nil
}

func Discover(ctx context.Context, target Target) (models []Model, err error) {
	var args []string
	switch target.Provider {
	case "codex":
		args = []string{"app-server"}
	case "claude":
		args = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence"}
	default:
		return nil, errors.New("unsupported model discovery provider")
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd, err := command(ctx, target, args...)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "quota-model-discovery-")
	if err != nil {
		return nil, errors.New("cannot create model discovery working directory")
	}
	defer func() {
		if removeErr := os.Remove(dir); removeErr != nil {
			models = nil
			err = errors.Join(err, errors.New("cannot remove discovery working directory; nonempty directories are preserved"))
		}
	}()
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot open discovery stdin")
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("cannot open discovery stdout")
	}
	defer stdout.Close()
	var stderr boundedOutput
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, processError(ctx, "start discovery", err, &stderr)
	}
	stopClose := context.AfterFunc(ctx, func() {
		stdin.Close()
		stdout.Close()
	})
	defer stopClose()
	stream := newProtocolStream(ctx, stdout, stdin)
	if target.Provider == "codex" {
		models, err = discoverCodex(stream)
	} else {
		models, err = discoverClaude(stream)
	}
	stdin.Close()
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, errors.Join(err, processError(ctx, "discovery or shutdown", ctx.Err(), &stderr))
	}
	if err != nil {
		return nil, processError(ctx, "discovery protocol", err, &stderr)
	}
	if waitErr != nil {
		return nil, processError(ctx, "discovery shutdown", waitErr, &stderr)
	}
	if drainErr != nil {
		return nil, errors.New("failed to drain discovery stdout")
	}
	return models, nil
}

type protocolStream struct {
	ctx     context.Context
	scanner *bufio.Scanner
	encoder *json.Encoder
}

func newProtocolStream(ctx context.Context, r io.Reader, w io.Writer) *protocolStream {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	return &protocolStream{ctx: ctx, scanner: scanner, encoder: json.NewEncoder(w)}
}

func (s *protocolStream) send(message any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.encoder.Encode(message); err != nil {
		return errors.New("cannot write protocol request")
	}
	return nil
}

func (s *protocolStream) read() (map[string]json.RawMessage, error) {
	for {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		if !s.scanner.Scan() {
			if s.scanner.Err() != nil {
				return nil, errors.New("cannot read protocol message (I/O failure or message exceeds 2 MiB)")
			}
			return nil, io.ErrUnexpectedEOF
		}
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(line, &message) != nil || message == nil {
			return nil, errors.New("invalid protocol JSON object")
		}
		return message, nil
	}
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (s *protocolStream) rpcResult(id int) (json.RawMessage, error) {
	for {
		message, err := s.read()
		if err != nil {
			return nil, err
		}
		if string(message["id"]) != strconv.Itoa(id) {
			continue
		}
		if !isNull(message["error"]) {
			var rpcError struct {
				Code *int `json:"code"`
			}
			if json.Unmarshal(message["error"], &rpcError) != nil || rpcError.Code == nil {
				return nil, errors.New("invalid RPC error response")
			}
			return nil, fmt.Errorf("RPC request %d failed with code %d", id, *rpcError.Code)
		}
		result := message["result"]
		if isNull(result) || result[0] != '{' {
			return nil, errors.New("missing or invalid RPC result object")
		}
		return result, nil
	}
}

func discoverCodex(s *protocolStream) ([]Model, error) {
	if err := s.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]string{"name": "quota-cli", "version": "0.1.0"}, "capabilities": map[string]any{},
	}}); err != nil {
		return nil, err
	}
	if _, err := s.rpcResult(1); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := s.send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	models := []Model{}
	var cursor *string
	seen := map[string]bool{}
	for id := 2; ; id++ {
		if err := s.send(map[string]any{"id": id, "method": "model/list", "params": map[string]any{
			"limit": 100, "includeHidden": true, "cursor": cursor,
		}}); err != nil {
			return nil, err
		}
		raw, err := s.rpcResult(id)
		if err != nil {
			return nil, fmt.Errorf("model/list: %w", err)
		}
		page, next, err := decodeCodexPage(raw)
		if err != nil {
			return nil, err
		}
		models = append(models, page...)
		if next == nil {
			return models, nil
		}
		if seen[*next] {
			return nil, errors.New("model/list repeated pagination cursor")
		}
		seen[*next] = true
		cursor = next
	}
}

func decodeCodexPage(raw json.RawMessage) ([]Model, *string, error) {
	var page struct {
		Data *[]struct {
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			SupportedReasoningEfforts *[]struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
			DefaultReasoningEffort string `json:"defaultReasoningEffort"`
			Hidden                 bool   `json:"hidden"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Data == nil {
		return nil, nil, errors.New("invalid model/list data")
	}
	models := make([]Model, 0, len(*page.Data))
	for _, entry := range *page.Data {
		if strings.TrimSpace(entry.Model) == "" {
			return nil, nil, errors.New("model/list entry missing model")
		}
		model := Model{ID: entry.Model, DisplayName: entry.DisplayName, DefaultEffort: entry.DefaultReasoningEffort, Hidden: entry.Hidden}
		if entry.SupportedReasoningEfforts != nil {
			supported := len(*entry.SupportedReasoningEfforts) > 0
			model.SupportsEffort = &supported
			model.SupportedEfforts = []string{}
			for _, effort := range *entry.SupportedReasoningEfforts {
				if strings.TrimSpace(effort.ReasoningEffort) == "" {
					return nil, nil, errors.New("model/list entry missing reasoning effort")
				}
				model.SupportedEfforts = append(model.SupportedEfforts, effort.ReasoningEffort)
			}
		}
		models = append(models, model)
	}
	return models, page.NextCursor, nil
}

func discoverClaude(s *protocolStream) ([]Model, error) {
	const requestID = "quota-model-discovery"
	if err := s.send(map[string]any{"type": "control_request", "request_id": requestID, "request": map[string]string{"subtype": "initialize"}}); err != nil {
		return nil, err
	}
	for {
		message, err := s.read()
		if err != nil {
			return nil, fmt.Errorf("initialize: %w", err)
		}
		var messageType string
		if json.Unmarshal(message["type"], &messageType) != nil || messageType != "control_response" {
			continue
		}
		var response struct {
			RequestID string          `json:"request_id"`
			Subtype   string          `json:"subtype"`
			Response  json.RawMessage `json:"response"`
		}
		if isNull(message["response"]) || json.Unmarshal(message["response"], &response) != nil {
			return nil, errors.New("invalid control response")
		}
		if response.RequestID != requestID {
			continue
		}
		if response.Subtype != "success" {
			return nil, errors.New("initialize control response did not succeed")
		}
		return decodeClaudeModels(response.Response)
	}
}

func decodeClaudeModels(raw json.RawMessage) ([]Model, error) {
	var result struct {
		Models *[]struct {
			Value                 string   `json:"value"`
			ResolvedModel         string   `json:"resolvedModel"`
			DisplayName           string   `json:"displayName"`
			SupportsEffort        *bool    `json:"supportsEffort"`
			SupportedEffortLevels []string `json:"supportedEffortLevels"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Models == nil {
		return nil, errors.New("invalid initialize models")
	}
	models := make([]Model, 0, len(*result.Models))
	for _, entry := range *result.Models {
		if strings.TrimSpace(entry.Value) == "" {
			return nil, errors.New("initialize model entry missing value")
		}
		for _, effort := range entry.SupportedEffortLevels {
			if strings.TrimSpace(effort) == "" {
				return nil, errors.New("initialize model entry has empty effort")
			}
		}
		models = append(models, Model{ID: entry.Value, ResolvedModel: entry.ResolvedModel, DisplayName: entry.DisplayName,
			SupportsEffort: entry.SupportsEffort, SupportedEfforts: entry.SupportedEffortLevels})
	}
	return models, nil
}
