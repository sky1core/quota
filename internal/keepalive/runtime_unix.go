//go:build darwin || linux

package keepalive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type runtimeProcess struct {
	Start, TTY, Executable string
}

func readRuntimeProcess(ctx context.Context, pid int) (runtimeProcess, error) {
	if pid <= 1 {
		return runtimeProcess{}, errors.New("invalid runtime process")
	}
	data, err := runtimeCommand(ctx, "ps", []string{"-p", strconv.Itoa(pid), "-o", "uid=", "-o", "stat=", "-o", "tty=", "-o", "lstart="})
	if err != nil {
		return runtimeProcess{}, errors.New("runtime process identity unavailable")
	}
	p, err := parseRuntimeProcess(data, os.Getuid())
	if err != nil {
		return runtimeProcess{}, err
	}
	p.Start, err = readRuntimeProcessStart(pid, p.Start)
	if err != nil {
		return runtimeProcess{}, err
	}
	data, err = runtimeCommand(ctx, "lsof", []string{"-n", "-P", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fpn"})
	if err != nil {
		return runtimeProcess{}, errors.New("runtime executable unavailable")
	}
	p.Executable = runtimeExecutablePath(data, pid)
	if !filepath.IsAbs(p.Executable) {
		return runtimeProcess{}, errors.New("runtime executable identity is ambiguous")
	}
	return p, nil
}

func runtimeExecutablePath(data []byte, pid int) string {
	owner := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "p") {
			owner = line == "p"+strconv.Itoa(pid)
		}
		if owner && strings.HasPrefix(line, "n/") {
			return line[1:]
		}
	}
	return ""
}

func parseRuntimeProcess(data []byte, uid int) (runtimeProcess, error) {
	text := strings.TrimSpace(string(data))
	fields := strings.Fields(text)
	if strings.ContainsAny(text, "\n\r") || len(fields) != 8 || fields[0] != strconv.Itoa(uid) || strings.ContainsAny(fields[1], "ZTtX") {
		return runtimeProcess{}, errors.New("runtime process is not alive, owned, and runnable")
	}
	start := strings.Join(fields[3:], " ")
	if _, err := time.Parse("Mon Jan 2 15:04:05 2006", start); err != nil {
		return runtimeProcess{}, errors.New("runtime process start time is unsupported")
	}
	return runtimeProcess{Start: start, TTY: fields[2]}, nil
}

func runtimeCommand(ctx context.Context, name string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TZ=UTC"}
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	var output runtimeOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("runtime metadata command failed")
	}
	return output.Bytes(), nil
}

type runtimeOutput struct{ bytes.Buffer }

func (b *runtimeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, errors.New("runtime metadata output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func sameRuntimeExecutable(actual, expected string) bool {
	a, err := os.Stat(actual)
	if err != nil || !a.Mode().IsRegular() {
		return false
	}
	b, err := os.Stat(expected)
	return err == nil && os.SameFile(a, b)
}

func openRuntimeFile(root *os.Root, name string, limit int64) (*os.File, os.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, errors.New("runtime metadata file unavailable")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || !runtimeOwned(info) {
		f.Close()
		return nil, nil, errors.New("runtime metadata file has unsupported type, owner, or size")
	}
	return f, info, nil
}

func openRuntimeDirectory(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() || !runtimeOwned(info) {
		f.Close()
		return nil, errors.New("runtime metadata directory type or ownership is unsupported")
	}
	return f, nil
}

func runtimeOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func readRuntimeFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, error) {
	f, _, err := openRuntimeFile(root, name, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var data bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buf)
		if int64(data.Len()+n) > limit {
			return nil, errors.New("runtime metadata file exceeds limit")
		}
		data.Write(buf[:n])
		if err == io.EOF {
			return data.Bytes(), nil
		}
		if err != nil {
			return nil, errors.New("runtime metadata read failed")
		}
	}
}

func runtimeSocketOwned(ctx context.Context, pid int, socket string) bool {
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || !runtimeOwned(info) {
		return false
	}
	data, err := runtimeCommand(ctx, "lsof", []string{"-n", "-P", "-a", "-p", strconv.Itoa(pid), "-U", "-Fpn"})
	if err != nil {
		return false
	}
	owner := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "p") {
			owner = line == "p"+strconv.Itoa(pid)
		}
		if owner && (line == "n"+socket || line == "n"+socket+" type=STREAM") {
			return true
		}
	}
	return false
}

func connectRuntimeSocket(ctx context.Context, socket string, pid int) (*net.UnixConn, error) {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, errors.New("keepalive socket connection failed")
	}
	u, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, errors.New("keepalive transport is not a Unix socket")
	}
	raw, err := u.SyscallConn()
	if err != nil {
		u.Close()
		return nil, errors.New("keepalive socket credentials unavailable")
	}
	var credentialErr error
	err = raw.Control(func(fd uintptr) { credentialErr = runtimePeer(int(fd), pid, os.Getuid()) })
	if err != nil || credentialErr != nil {
		u.Close()
		return nil, errors.New("keepalive socket peer does not match the live process")
	}
	return u, nil
}

func runtimeAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
