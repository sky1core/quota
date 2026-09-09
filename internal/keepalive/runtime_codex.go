//go:build darwin || linux

package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type codexThread struct {
	ID         string `json:"id"`
	Transcript string `json:"rollout_path"`
	Source     string `json:"source"`
	Version    string `json:"cli_version"`
	CWD        string `json:"cwd"`
	Linked     *int   `json:"linked"`
}

func codexNativeExecutable() (string, error) {
	launcher, err := exec.LookPath("codex")
	if err != nil {
		return "", errors.New("codex executable unavailable")
	}
	launcher, err = filepath.EvalSymlinks(launcher)
	if err != nil {
		return "", errors.New("codex executable path unavailable")
	}
	if filepath.Base(launcher) == "codex" {
		return launcher, nil
	}
	if filepath.Base(launcher) != "codex.js" || filepath.Base(filepath.Dir(launcher)) != "bin" {
		return "", errors.New("codex launcher layout is unsupported")
	}
	arch, target := "", ""
	switch runtime.GOARCH {
	case "arm64":
		arch, target = "arm64", "aarch64"
	case "amd64":
		arch, target = "x64", "x86_64"
	default:
		return "", errors.New("codex native architecture is unsupported")
	}
	if runtime.GOOS == "darwin" {
		target += "-apple-darwin"
	} else {
		target += "-unknown-linux-musl"
	}
	base := filepath.Dir(filepath.Dir(launcher))
	pkg := "codex-" + runtime.GOOS + "-" + arch
	paths := []string{
		filepath.Join(base, "node_modules", "@openai", pkg, "vendor", target, "bin", "codex"),
		filepath.Join(filepath.Dir(base), pkg, "vendor", target, "bin", "codex"),
		filepath.Join(base, "vendor", target, "bin", "codex"),
	}
	found := ""
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", errors.New("codex native executable is unsupported")
		}
		if found != "" && !sameRuntimeExecutable(found, path) {
			return "", errors.New("codex native installation identity is ambiguous")
		}
		found = path
	}
	if found == "" {
		return "", errors.New("codex native executable was not found in a supported installation")
	}
	return found, nil
}

func codexLsof(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", append([]string{"-n", "-P"}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TZ=UTC"}
	cmd.Dir, cmd.WaitDelay = "/", time.Second
	var output runtimeOutput
	cmd.Stdout = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return output.Bytes(), nil
	}
	if err != nil {
		return nil, errors.New("codex process observation failed")
	}
	return output.Bytes(), nil
}

func parseCodexLocks(data []byte, home string) (map[string][]int, error) {
	owners := map[string][]int{}
	var failures []error
	dir := filepath.Join(home, "thread-writer-locks")
	pid, fd := 0, false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			var err error
			pid, err = strconv.Atoi(line[1:])
			fd = false
			if err != nil || pid <= 1 {
				return nil, errors.New("codex writer PID is unsupported")
			}
		case 'f':
			fd = len(line) > 1 && line[1] >= '0' && line[1] <= '9'
		case 'n':
			path := line[1:]
			if !fd || filepath.Dir(path) != dir {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(path), ".lock")
			if !runtimeUUID.MatchString(id) || filepath.Base(path) != id+".lock" || pid <= 1 {
				failures = append(failures, errors.New("codex writer lock identity is unsupported"))
				continue
			}
			duplicate := false
			for _, owner := range owners[id] {
				duplicate = duplicate || owner == pid
			}
			if !duplicate {
				owners[id] = append(owners[id], pid)
			}
		default:
			return nil, errors.New("codex writer observation shape is unsupported")
		}
	}
	return owners, errors.Join(failures...)
}

func readCodexLocks(ctx context.Context, root *os.Root, home string) (map[string][]int, error) {
	dir, err := openRuntimeDirectory(root, "thread-writer-locks")
	if os.IsNotExist(err) {
		return map[string][]int{}, nil
	}
	if err != nil {
		return nil, errors.New("codex writer directory unavailable")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(2049)
	if err != nil && err != io.EOF || len(entries) > 2048 {
		return nil, errors.New("codex writer directory exceeds inspection limit")
	}
	if len(entries) == 0 {
		return map[string][]int{}, nil
	}
	data, err := codexLsof(ctx, "-Fpfn", "+D", filepath.Join(home, "thread-writer-locks"))
	if err != nil {
		return nil, err
	}
	return parseCodexLocks(data, home)
}

func codexSoleWriter(owners map[string][]int, session string, pid int) bool {
	if len(owners[session]) != 1 || owners[session][0] != pid {
		return false
	}
	for id, pids := range owners {
		if id == session {
			continue
		}
		for _, owner := range pids {
			if owner == pid {
				return false
			}
		}
	}
	return true
}

func readCodexProcess(ctx context.Context, pid int, executable string) (runtimeProcess, error) {
	data, err := runtimeCommand(ctx, "ps", []string{"-p", strconv.Itoa(pid), "-o", "uid=", "-o", "stat=", "-o", "tty=", "-o", "lstart="})
	if err != nil {
		return runtimeProcess{}, errors.New("codex process identity unavailable")
	}
	p, err := parseRuntimeProcess(data, os.Getuid())
	if err != nil || p.TTY == "?" || p.TTY == "??" || p.TTY == "-" {
		return runtimeProcess{}, errors.New("codex process is not a verified live interactive terminal")
	}
	data, err = codexLsof(ctx, "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fpn")
	if err != nil {
		return runtimeProcess{}, err
	}
	p.Executable = runtimeExecutablePath(data, pid)
	if !sameRuntimeExecutable(p.Executable, executable) {
		return runtimeProcess{}, errors.New("codex native executable ownership mismatch")
	}
	return p, nil
}

func readCodexThread(ctx context.Context, root *os.Root, account Account, id string) (codexThread, error) {
	var thread codexThread
	if !runtimeUUID.MatchString(id) {
		return thread, errors.New("invalid codex thread identity")
	}
	f, _, err := openRuntimeFile(root, "state_5.sqlite", 512*1024*1024)
	if err != nil {
		return thread, err
	}
	f.Close()
	query := "SELECT id,rollout_path,source,cli_version,cwd,EXISTS(SELECT 1 FROM thread_spawn_edges WHERE parent_thread_id=threads.id OR child_thread_id=threads.id) AS linked FROM threads WHERE id='" + id + "';"
	data, err := runtimeCommand(ctx, "sqlite3", []string{"-init", "/dev/null", "-readonly", "-batch", "-bail", "-json", filepath.Join(account.Home, "state_5.sqlite"), query})
	if err != nil {
		return thread, errors.New("codex read-only thread metadata unavailable")
	}
	var rows []codexThread
	if json.Unmarshal(data, &rows) != nil || len(rows) != 1 {
		return thread, errors.New("codex thread metadata is missing or ambiguous")
	}
	thread = rows[0]
	if thread.ID != id || thread.Source != "cli" || !versionAtLeast(thread.Version, codexMinVersion) || !filepath.IsAbs(thread.CWD) || thread.Linked == nil || *thread.Linked != 0 {
		return thread, errors.New("codex thread source, version, or background-agent state is unsupported")
	}
	name, err := filepath.Rel(account.Home, thread.Transcript)
	if err != nil || !filepath.IsAbs(thread.Transcript) || !filepath.IsLocal(name) || !strings.HasPrefix(name, "sessions"+string(filepath.Separator)) {
		return thread, errors.New("codex transcript is outside the account sessions")
	}
	return thread, nil
}

type codexEvidence struct {
	thread  codexThread
	process runtimeProcess
	data    []byte
	info    os.FileInfo
	state   codexTranscript
}

func inspectCodexRuntime(ctx context.Context, root *os.Root, account Account, id string, pid int, executable string) (codexEvidence, error) {
	var e codexEvidence
	var err error
	e.process, err = readCodexProcess(ctx, pid, executable)
	if err != nil {
		return e, err
	}
	e.thread, err = readCodexThread(ctx, root, account, id)
	if err != nil {
		return e, err
	}
	name, _ := filepath.Rel(account.Home, e.thread.Transcript)
	f, info, err := openRuntimeFile(root, name, codexTranscriptLimit)
	if err != nil {
		return e, err
	}
	defer f.Close()
	e.info = info
	e.data, err = io.ReadAll(io.LimitReader(f, codexTranscriptLimit+1))
	if err != nil {
		return e, errors.New("codex transcript read failed")
	}
	e.state, err = parseCodexTranscript(ctx, e.data, id, e.thread.CWD)
	return e, err
}

func (e codexEvidence) candidate(account Account, pid int, now time.Time, maxAge time.Duration) Candidate {
	if !e.state.idle() || !runtimeRecent(e.state.lastActivity, now, maxAge) || e.state.lastRecord.After(now) {
		return Candidate{}
	}
	return Candidate{Provider: account.Provider, Account: account.Key, Home: account.Home, SessionID: e.thread.ID, PID: pid, LastActivity: e.state.lastActivity, ActivityID: e.state.activity, Transcript: e.thread.Transcript}
}

func scanCodexRuntime(ctx context.Context, account Account, now time.Time, maxAge time.Duration) (found []Candidate, resultErr error) {
	if account.Provider != "codex" || !filepath.IsAbs(account.Home) || account.Key == "" || now.IsZero() || maxAge <= 0 {
		return nil, errors.New("invalid codex scan input")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(account.Home)
	if err != nil {
		return nil, errors.New("codex runtime home unavailable")
	}
	defer root.Close()
	owners, err := readCodexLocks(ctx, root, account.Home)
	if len(owners) == 0 {
		return nil, err
	}
	scanErr := err
	executable, err := codexNativeExecutable()
	if err != nil {
		return nil, err
	}
	var rpc *codexRPC
	var closeHelper func() error
	defer func() {
		if closeHelper != nil {
			resultErr = errors.Join(resultErr, closeHelper())
		}
	}()
	var failures []error
	if scanErr != nil {
		failures = append(failures, scanErr)
	}
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err())
			break
		}
		pids := owners[id]
		if len(pids) != 1 || !codexSoleWriter(owners, id, pids[0]) {
			failures = append(failures, errors.New("codex thread writer ownership is ambiguous"))
			continue
		}
		e, err := inspectCodexRuntime(ctx, root, account, id, pids[0], executable)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		candidate := e.candidate(account, pids[0], now, maxAge)
		if candidate.ActivityID == "" {
			continue
		}
		if rpc == nil {
			rpc, closeHelper, err = startCodexRPC(ctx, account, executable)
			if err != nil {
				failures = append(failures, err)
				break
			}
		}
		queue, err := rpc.queue(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if len(queue) != 0 {
			continue
		}
		if err := recheckCodexEvidence(ctx, root, account, candidate, executable, e); err != nil {
			failures = append(failures, err)
			continue
		}
		found = append(found, candidate)
	}
	return found, errors.Join(failures...)
}

func recheckCodexEvidence(ctx context.Context, root *os.Root, account Account, c Candidate, executable string, e codexEvidence) error {
	owners, err := readCodexLocks(ctx, root, account.Home)
	if err != nil && owners == nil {
		return err
	}
	if !codexSoleWriter(owners, c.SessionID, c.PID) {
		return errors.New("codex original thread writer changed or exited")
	}
	p, err := readCodexProcess(ctx, c.PID, executable)
	if err != nil || p != e.process {
		return errors.New("codex original process identity changed")
	}
	thread, err := readCodexThread(ctx, root, account, c.SessionID)
	if err != nil || thread.Transcript != e.thread.Transcript || thread.CWD != e.thread.CWD {
		return errors.New("codex thread metadata changed")
	}
	name, _ := filepath.Rel(account.Home, c.Transcript)
	info, err := root.Lstat(name)
	if err != nil || !os.SameFile(e.info, info) {
		return errors.New("codex transcript was replaced")
	}
	data, err := readRuntimeFile(ctx, root, name, codexTranscriptLimit)
	if err != nil || !bytes.Equal(data, e.data) {
		return errors.New("codex transcript changed before queue submission")
	}
	return nil
}

func deliverCodexRuntime(ctx context.Context, account Account, c Candidate, message string, maxAge time.Duration, id string, ready func() bool) (receipt Receipt, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !runtimeUUID.MatchString(id) || id == c.ActivityID || account.Provider != "codex" || account.Key == "" || !filepath.IsAbs(account.Home) || c.Provider != "codex" || c.Account != account.Key || c.Home != account.Home || !runtimeUUID.MatchString(c.SessionID) || c.PID <= 1 || ready == nil || strings.TrimSpace(message) == "" || len(message) > 8192 || strings.IndexByte(message, 0) >= 0 || maxAge <= 0 {
		return receipt, errors.New("invalid codex delivery input")
	}
	root, err := os.OpenRoot(account.Home)
	if err != nil {
		return receipt, errors.New("codex runtime home unavailable")
	}
	defer root.Close()
	executable, err := codexNativeExecutable()
	if err != nil {
		return receipt, err
	}
	e, err := inspectCodexRuntime(ctx, root, account, c.SessionID, c.PID, executable)
	if err != nil {
		return receipt, err
	}
	fresh := e.candidate(account, c.PID, time.Now(), maxAge)
	if !fresh.LastActivity.Equal(c.LastActivity) {
		return receipt, errors.New("codex delivery activity changed")
	}
	fresh.LastActivity = c.LastActivity
	if fresh != c {
		return receipt, errors.New("codex delivery candidate changed or is no longer idle")
	}
	rpc, closeHelper, err := startCodexRPC(ctx, account, executable)
	if err != nil {
		return receipt, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeHelper()) }()
	queue, err := rpc.queue(ctx, c.SessionID)
	if err != nil {
		return receipt, err
	}
	if len(queue) != 0 {
		return receipt, errors.New("codex has pending queued input")
	}
	if err := recheckCodexEvidence(ctx, root, account, c, executable, e); err != nil {
		return receipt, err
	}
	queue, err = rpc.queue(ctx, c.SessionID)
	if err != nil {
		return receipt, err
	}
	if len(queue) != 0 || !runtimeRecent(c.LastActivity, time.Now(), maxAge) {
		return receipt, errors.New("codex queue or activity changed before submission")
	}
	name, _ := filepath.Rel(account.Home, c.Transcript)
	latest, err := readRuntimeFile(ctx, root, name, codexTranscriptLimit)
	if err != nil || !bytes.Equal(latest, e.data) || !runtimeAlive(c.PID) {
		return receipt, errors.New("codex runtime changed immediately before submission")
	}
	receipt.ActivityID = id
	var usage *CacheUsage
	err = rpc.add(ctx, c.SessionID, id, message, ready)
	if err == nil {
		usage, err = awaitCodexCompletion(ctx, root, account, c, executable, e, id)
	}
	if err == nil {
		receipt.Confirmed = true
		receipt.Cache = usage
		return receipt, nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cleanupCancel()
	cleanupRPC := rpc
	if rpc.stopped {
		var cleanupClose func() error
		cleanupRPC, cleanupClose, resultErr = startCodexRPC(cleanupCtx, account, executable)
		if resultErr != nil {
			return receipt, errors.Join(errors.New("codex dispatch and own-queue cleanup are unconfirmed; no retry"), err, resultErr)
		}
		defer func() { resultErr = errors.Join(resultErr, cleanupClose()) }()
	}
	cleanupErr := cleanupRPC.removeOwn(cleanupCtx, c.SessionID, id, message)
	return receipt, errors.Join(errors.New("codex response completion is unconfirmed; no retry"), err, cleanupErr)
}

func awaitCodexCompletion(ctx context.Context, root *os.Root, account Account, c Candidate, executable string, e codexEvidence, id string) (*CacheUsage, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	name, _ := filepath.Rel(account.Home, c.Transcript)
	f, info, err := openRuntimeFile(root, name, codexTranscriptLimit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !os.SameFile(info, e.info) {
		return nil, errors.New("codex completion transcript was replaced")
	}
	offset := int64(len(e.data))
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, errors.New("codex completion transcript seek failed")
	}
	s := e.state
	var pending []byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		if !runtimeAlive(c.PID) {
			return nil, errors.New("codex original CLI exited before completion was verified")
		}
		info, err := root.Lstat(name)
		if err != nil || !os.SameFile(info, e.info) || info.Size() < offset || info.Size() > codexTranscriptLimit {
			return nil, errors.New("codex completion transcript was replaced")
		}
		tail, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
		if err != nil || len(tail) > 4*1024*1024 || offset+int64(len(tail)) > codexTranscriptLimit {
			return nil, errors.New("codex completion tail is unreadable or exceeds limit")
		}
		offset += int64(len(tail))
		pending = append(pending, tail...)
		for {
			end := bytes.IndexByte(pending, '\n')
			if end < 0 {
				break
			}
			if err := s.consume(pending[:end]); err != nil {
				return nil, err
			}
			pending = pending[end+1:]
		}
		if len(pending) > 4*1024*1024 {
			return nil, errors.New("codex incomplete completion record exceeds limit")
		}
		if s.activity != id || !s.idle() || len(pending) != 0 {
			continue
		}
		data, err := readRuntimeFile(ctx, root, name, codexTranscriptLimit)
		if err != nil || !bytes.HasPrefix(data, e.data) {
			return nil, errors.New("codex completion transcript was truncated or rewritten")
		}
		verified, err := parseCodexTranscript(ctx, data, c.SessionID, e.thread.CWD)
		if err != nil || !verified.idle() || verified.activity != id || verified.lastRecord.After(time.Now()) {
			return nil, errors.New("codex completion changed during verification")
		}
		confirmed := e
		confirmed.data = data
		if err := recheckCodexEvidence(ctx, root, account, c, executable, confirmed); err != nil {
			return nil, err
		}
		return verified.cacheUsage(), nil
	}
}
