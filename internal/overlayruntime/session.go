package overlayruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sky1core/quota/internal/atomicfile"
)

type instructionSession struct {
	path string
	last string
	lock *os.File
}

func (r repoContext) instructionSession(agent, id, transcript string) (*instructionSession, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".config", "quota", "instruction-sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(agent + "\x00" + r.Common + "\x00" + id + "\x00" + transcript))
	s := &instructionSession{path: filepath.Join(dir, fmt.Sprintf("%x", key))}
	s.lock, err = os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		s.lock.Close()
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		s.close()
		return nil, err
	}
	if err == nil {
		s.last = strings.TrimSuffix(string(data), "\n")
		decoded, e := hex.DecodeString(s.last)
		if e != nil || len(decoded) != sha256.Size {
			s.close()
			return nil, fmt.Errorf("invalid instruction delivery record: %s", s.path)
		}
	}
	return s, nil
}

func (s *instructionSession) close() {
	syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	s.lock.Close()
}

func localSnapshot(body string, present bool) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%t\x00%s", present, body))))
}

func (s *instructionSession) remember(snapshot string) error {
	if s == nil || s.last == snapshot {
		return nil
	}
	return atomicfile.Save(s.path, []byte(snapshot+"\n"), 0o600, true)
}
