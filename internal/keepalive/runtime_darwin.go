package keepalive

import (
	"errors"

	"golang.org/x/sys/unix"
)

func runtimePeer(fd, pid, uid int) error {
	peerPID, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil || peerPID != pid {
		return errors.New("socket peer PID mismatch")
	}
	peer, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil || peer.Uid != uint32(uid) {
		return errors.New("socket peer owner mismatch")
	}
	return nil
}
