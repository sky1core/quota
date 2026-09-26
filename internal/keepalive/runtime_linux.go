package keepalive

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readRuntimeProcessStart(pid int, _ string) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", errors.New("runtime process start time unavailable")
	}
	return parseLinuxProcessStart(data, pid)
}

func parseLinuxProcessStart(data []byte, pid int) (string, error) {
	text := string(data)
	end := strings.LastIndex(text, ") ")
	if !strings.HasPrefix(text, strconv.Itoa(pid)+" (") || end < 0 {
		return "", errors.New("runtime process stat identity is unsupported")
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 20 {
		return "", errors.New("runtime process stat is incomplete")
	}
	start := fields[19]
	if _, err := strconv.ParseUint(start, 10, 64); err != nil {
		return "", errors.New("runtime process start time is unsupported")
	}
	return start, nil
}

func runtimePeer(fd, pid, uid int) error {
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(peer.Pid) != pid || peer.Uid != uint32(uid) {
		return errors.New("socket peer identity mismatch")
	}
	return nil
}
