package atomicfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrExists = errors.New("file already exists")

func Save(path string, data []byte, perm os.FileMode, force bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := createExclusiveTemp(dir, ".quota.tmp-", perm)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeTemp(tmp, data); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}

	if !force {
		if err := os.Link(tmpName, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s: %w", path, ErrExists)
			}
			return err
		}
		return nil
	}

	unlock, err := lockTarget(path)
	if err != nil {
		return err
	}
	defer unlock()
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to overwrite", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeTemp(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func createExclusiveTemp(dir, prefix string, perm os.FileMode) (*os.File, error) {
	for i := 0; ; i++ {
		name := filepath.Join(dir, fmt.Sprintf("%s%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), i))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
}

func lockTarget(path string) (func(), error) {
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	lockPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".quota-%x.lock", sum))
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		lock.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}, nil
}
