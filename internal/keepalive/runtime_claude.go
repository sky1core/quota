//go:build darwin || linux

package keepalive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sky1core/quota/internal/claude"
)

const claudeTranscriptLimit = 64 * 1024 * 1024

var errClaudeRegistryVersion = errors.New("claude runtime registry version is unsupported")

type claudeRegistry struct {
	PID                 int    `json:"pid"`
	SessionID           string `json:"sessionId"`
	CWD                 string `json:"cwd"`
	StartedAt           int64  `json:"startedAt"`
	ProcStart           string `json:"procStart"`
	Version             string `json:"version"`
	Kind                string `json:"kind"`
	Status              string `json:"status"`
	WaitingFor          string `json:"waitingFor"`
	MessagingSocketPath string `json:"messagingSocketPath"`
	Tempo               string `json:"tempo"`
	Needs               string `json:"needs"`
	Agent               string `json:"agent"`
	JobID               string `json:"jobId"`
	ParkedJobID         string `json:"parkedJobId"`
	Spare               bool   `json:"spare"`
}

func parseClaudeRegistry(data []byte, pid int) (claudeRegistry, error) {
	var r claudeRegistry
	if json.Unmarshal(data, &r) != nil || r.PID != pid || pid <= 1 || !runtimeUUID.MatchString(r.SessionID) || r.StartedAt <= 0 || r.ProcStart == "" || !filepath.IsAbs(r.CWD) {
		return r, errors.New("claude runtime registry shape is unsupported")
	}
	if !versionAtLeast(r.Version, claudeMinVersion) {
		return r, errClaudeRegistryVersion
	}
	if r.Status != "idle" && r.Status != "busy" && r.Status != "waiting" && r.Status != "shell" {
		return r, errors.New("claude runtime status is unsupported")
	}
	return r, nil
}

func (r claudeRegistry) idle() bool {
	return r.Kind == "interactive" && r.Status == "idle" && r.WaitingFor == "" && (r.Tempo == "" || r.Tempo == "idle") && r.Needs == "" && r.Agent == "" && r.JobID == "" && r.ParkedJobID == "" && !r.Spare
}

func readClaudeRegistries(ctx context.Context, root *os.Root) ([]claudeRegistry, error) {
	dir, err := openRuntimeDirectory(root, "sessions")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("claude runtime registry unavailable")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(2049)
	if err != nil && err != io.EOF || len(entries) > 2048 {
		return nil, errors.New("claude runtime registry directory is unreadable or exceeds limit")
	}
	var records []claudeRegistry
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil || name != strconv.Itoa(pid)+".json" || pid <= 1 {
			continue
		}
		if !runtimeAlive(pid) {
			continue
		}
		data, err := readRuntimeFile(ctx, root, filepath.Join("sessions", name), 64*1024)
		if err != nil {
			return nil, err
		}
		r, err := parseClaudeRegistry(data, pid)
		if errors.Is(err, errClaudeRegistryVersion) {
			r.Status = ""
			records = append(records, r)
			failures = append(failures, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, errors.Join(failures...)
}

func scanClaudeRuntime(ctx context.Context, account Account, now time.Time, maxAge time.Duration) ([]Candidate, error) {
	root, err := os.OpenRoot(account.Home)
	if err != nil {
		return nil, errors.New("claude runtime home unavailable")
	}
	defer root.Close()
	records, scanErr := readClaudeRegistries(ctx, root)
	if len(records) == 0 {
		return nil, scanErr
	}
	executable, err := claude.FindBinary()
	if err != nil {
		return nil, errors.New("claude executable identity unavailable")
	}
	counts := map[string]int{}
	for _, record := range records {
		counts[record.SessionID]++
	}
	var candidates []Candidate
	var failures []error
	if scanErr != nil {
		failures = append(failures, scanErr)
	}
	for _, record := range records {
		if !record.idle() {
			continue
		}
		if counts[record.SessionID] != 1 {
			failures = append(failures, errors.New("claude session ownership is ambiguous"))
			continue
		}
		candidate, _, err := inspectClaudeRuntime(ctx, root, account, record, executable, now, maxAge)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if candidate.ActivityID != "" {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, errors.Join(failures...)
}

func inspectClaudeRuntime(ctx context.Context, root *os.Root, account Account, record claudeRegistry, executable string, now time.Time, maxAge time.Duration) (Candidate, []byte, error) {
	if !record.idle() {
		return Candidate{}, nil, nil
	}
	process, err := readRuntimeProcess(ctx, record.PID)
	if err != nil {
		return Candidate{}, nil, err
	}
	if process.Start != strings.Join(strings.Fields(record.ProcStart), " ") || process.TTY == "?" || process.TTY == "??" || !sameRuntimeExecutable(process.Executable, executable) {
		return Candidate{}, nil, errors.New("claude runtime process ownership or interactive terminal could not be verified")
	}
	socket := record.MessagingSocketPath
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || !runtimeSocketOwned(ctx, record.PID, socket) {
		return Candidate{}, nil, errors.New("claude exported session socket is missing or its ownership is unverified")
	}
	transcript, err := findClaudeTranscript(ctx, root, record.SessionID)
	if err != nil {
		return Candidate{}, nil, err
	}
	data, err := readRuntimeFile(ctx, root, transcript, claudeTranscriptLimit)
	if err != nil {
		return Candidate{}, nil, err
	}
	state, err := parseClaudeTranscript(ctx, data, record.SessionID)
	if err != nil {
		return Candidate{}, nil, err
	}
	if !state.idle() || !runtimeRecent(state.lastActivity, now, maxAge) || state.completedAt.After(now) {
		return Candidate{}, nil, nil
	}
	return Candidate{Provider: account.Provider, Account: account.Key, Home: account.Home, SessionID: record.SessionID, PID: record.PID, LastActivity: state.lastActivity, ActivityID: state.activity, Transcript: filepath.Join(account.Home, transcript), Socket: socket}, data, nil
}

func findClaudeTranscript(ctx context.Context, root *os.Root, session string) (string, error) {
	if !runtimeUUID.MatchString(session) {
		return "", errors.New("invalid claude session identity")
	}
	dir, err := openRuntimeDirectory(root, "projects")
	if err != nil {
		return "", errors.New("claude transcript directory unavailable")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(4097)
	if err != nil && err != io.EOF || len(entries) > 4096 {
		return "", errors.New("claude transcript directory is unreadable or exceeds limit")
	}
	var found string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := filepath.Join("projects", entry.Name(), session+".jsonl")
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || !runtimeOwned(info) {
			return "", errors.New("claude transcript type or ownership is unsupported")
		}
		if found != "" {
			return "", errors.New("claude transcript ownership is ambiguous")
		}
		found = name
	}
	if found == "" {
		return "", errors.New("claude session transcript is missing")
	}
	return found, nil
}

func deliverClaudeRuntime(ctx context.Context, account Account, c Candidate, message string, maxAge time.Duration, id string, ready func() bool) (Receipt, error) {
	root, err := os.OpenRoot(account.Home)
	if err != nil {
		return Receipt{}, errors.New("claude runtime home unavailable")
	}
	defer root.Close()
	records, err := readClaudeRegistries(ctx, root)
	if err != nil && len(records) == 0 {
		return Receipt{}, err
	}
	var record claudeRegistry
	matches := 0
	for _, r := range records {
		if r.SessionID == c.SessionID {
			matches++
			if r.PID == c.PID {
				record = r
			}
		}
	}
	if matches != 1 || record.PID == 0 {
		return Receipt{}, errors.New("claude delivery session is gone or ownership changed")
	}
	executable, err := claude.FindBinary()
	if err != nil {
		return Receipt{}, errors.New("claude executable identity unavailable")
	}
	fresh, baseline, err := inspectClaudeRuntime(ctx, root, account, record, executable, time.Now(), maxAge)
	if err != nil {
		return Receipt{}, err
	}
	if !fresh.LastActivity.Equal(c.LastActivity) {
		return Receipt{}, errors.New("claude delivery activity changed")
	}
	fresh.LastActivity = c.LastActivity
	if fresh != c {
		return Receipt{}, errors.New("claude delivery candidate changed or is no longer idle and recent")
	}
	conn, err := connectRuntimeSocket(ctx, c.Socket, c.PID)
	if err != nil {
		return Receipt{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	latestData, err := readRuntimeFile(ctx, root, filepath.Join("sessions", strconv.Itoa(c.PID)+".json"), 64*1024)
	if err != nil {
		return Receipt{}, err
	}
	latest, err := parseClaudeRegistry(latestData, c.PID)
	if err != nil || latest != record {
		return Receipt{}, errors.New("claude runtime changed before delivery")
	}
	name, err := filepath.Rel(account.Home, c.Transcript)
	if err != nil || !filepath.IsLocal(name) {
		return Receipt{}, errors.New("claude delivery transcript is outside the account")
	}
	f, info, err := openRuntimeFile(root, name, claudeTranscriptLimit)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	current, err := io.ReadAll(io.LimitReader(f, claudeTranscriptLimit+1))
	if err != nil || !bytes.Equal(current, baseline) {
		return Receipt{}, errors.New("claude transcript changed before delivery")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if !runtimeRecent(c.LastActivity, time.Now(), maxAge) {
		return Receipt{}, errors.New("claude activity expired before delivery")
	}
	receipt := Receipt{ActivityID: id}
	if err := sendClaudeRuntimeFrame(ctx, conn, c.SessionID, id, message, ready); err != nil {
		return receipt, err
	}
	state, err := parseClaudeTranscript(ctx, baseline, c.SessionID)
	if err != nil {
		return receipt, errors.New("claude dispatch is unconfirmed: transcript changed")
	}
	usage, err := awaitClaudeCompletion(ctx, root, name, f, info, baseline, state, record, id)
	if err != nil {
		return receipt, err
	}
	receipt.Confirmed = true
	receipt.Cache = usage
	return receipt, nil
}

func sendClaudeRuntimeFrame(ctx context.Context, conn net.Conn, session, id, message string, ready func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame := struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		UUID      string `json:"uuid"`
		From      string `json:"from"`
		Message   struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{Type: "user", SessionID: session, UUID: id, From: "quota-keepalive"}
	frame.Message.Role, frame.Message.Content = "user", message
	data, err := json.Marshal(frame)
	if err != nil {
		return errors.New("keepalive frame encoding failed")
	}
	data = append(data, '\n')
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if conn.SetWriteDeadline(deadline) != nil {
		return errors.New("keepalive socket deadline unavailable")
	}
	if ready == nil || !ready() {
		return errors.New("keepalive PC inactivity condition no longer holds")
	}
	n, err := conn.Write(data)
	if err != nil || n != len(data) {
		return errors.New("claude dispatch is unconfirmed: socket write failed; no retry")
	}
	return nil
}

func awaitClaudeCompletion(ctx context.Context, root *os.Root, name string, f *os.File, info os.FileInfo, baseline []byte, state claudeTranscript, record claudeRegistry, id string) (*CacheUsage, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	offset := int64(len(baseline))
	digest := sha256.Sum256(baseline)
	var pending []byte
	for {
		select {
		case <-ctx.Done():
			return nil, errors.Join(errors.New("claude dispatch is unconfirmed or pending; no retry and no log removal"), ctx.Err())
		case <-ticker.C:
		}
		current, err := root.Lstat(name)
		if err != nil || !os.SameFile(info, current) || current.Size() < offset || current.Size() > claudeTranscriptLimit {
			return nil, errors.New("claude dispatch is unconfirmed: transcript replaced, truncated, or oversized")
		}
		tail, err := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
		if err != nil || len(tail) > 8*1024*1024 {
			return nil, errors.New("claude dispatch is unconfirmed: transcript tail unavailable or oversized")
		}
		offset += int64(len(tail))
		pending = append(pending, tail...)
		for {
			end := bytes.IndexByte(pending, '\n')
			if end < 0 {
				break
			}
			if err := state.consume(pending[:end]); err != nil {
				return nil, errors.New("claude dispatch is unconfirmed: unsupported transcript record")
			}
			pending = pending[end+1:]
		}
		if len(pending) > 4*1024*1024 {
			return nil, errors.New("claude dispatch is unconfirmed: incomplete transcript record exceeds limit")
		}
		data, err := readRuntimeFile(ctx, root, filepath.Join("sessions", strconv.Itoa(record.PID)+".json"), 64*1024)
		if err != nil {
			return nil, errors.New("claude dispatch is unconfirmed: runtime registry unavailable")
		}
		latest, err := parseClaudeRegistry(data, record.PID)
		if err != nil || latest.SessionID != record.SessionID || latest.ProcStart != record.ProcStart || latest.MessagingSocketPath != record.MessagingSocketPath || !runtimeAlive(record.PID) {
			return nil, errors.New("claude dispatch is unconfirmed: live session changed")
		}
		if latest.Status == "waiting" || latest.WaitingFor != "" {
			return nil, errors.New("claude dispatch is pending approval or input; no retry")
		}
		if state.activity != id || !state.responseComplete() || !latest.idle() || len(pending) != 0 {
			continue
		}
		prefix := make([]byte, len(baseline))
		if _, err := f.ReadAt(prefix, 0); err != nil || sha256.Sum256(prefix) != digest {
			return nil, errors.New("claude dispatch is unconfirmed: transcript prefix changed")
		}
		return state.cacheUsage(), nil
	}
}
