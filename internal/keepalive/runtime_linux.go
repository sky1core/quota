package keepalive

import (
	"errors"

	"golang.org/x/sys/unix"
)

func runtimePeer(fd, pid, uid int) error {
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(peer.Pid) != pid || peer.Uid != uint32(uid) {
		return errors.New("socket peer identity mismatch")
	}
	return nil
}
