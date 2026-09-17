package agenthooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func ReadJSONObject(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeJSONObject(b)
}

func decodeJSONObject(b []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("JSON root must be an object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing content after JSON object")
	}
	return root, nil
}

func UpdateJSONObjectWithBackup(path string, update func(map[string]any) error) (map[string]any, error) {
	return updateJSONObjectWithBackup(path, update, syscall.LOCK_EX)
}

func TryUpdateJSONObjectWithBackup(path string, update func(map[string]any) error) (map[string]any, error) {
	return updateJSONObjectWithBackup(path, update, syscall.LOCK_EX|syscall.LOCK_NB)
}

func updateJSONObjectWithBackup(path string, update func(map[string]any) error, lockFlags int) (map[string]any, error) {
	target, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	target = filepath.Join(parent, filepath.Base(target))
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(target)
		if err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lock, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), lockFlags)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("configuration is being edited; try saving again: %w", err)
		}
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	original, err := os.ReadFile(target)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	mode := os.FileMode(0o644)
	if existed {
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		mode = info.Mode().Perm()
	}
	root, err := decodeJSONObject(original)
	if err != nil {
		return nil, err
	}
	before, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	if err := update(root); err != nil {
		return nil, err
	}
	after, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(before, after) {
		return root, nil
	}
	content, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	if err := ensureJSONTargetUnchanged(target, existed, original); err != nil {
		return nil, err
	}
	if existed {
		if err := writeUniqueBackup(target, original); err != nil {
			return nil, err
		}
	}
	tmp, err := createExclusiveTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-", mode)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := ensureJSONTargetUnchanged(target, existed, original); err != nil {
		return nil, err
	}
	if existed {
		if err := os.Chmod(tmp.Name(), mode); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return nil, err
	}
	saved, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("saved JSON could not be read back: %w", err)
	}
	if !bytes.Equal(saved, content) {
		return nil, fmt.Errorf("saved JSON differs from the update")
	}
	return decodeJSONObject(saved)
}

func ensureJSONTargetUnchanged(target string, existed bool, original []byte) error {
	latest, err := os.ReadFile(target)
	if existed {
		if err != nil {
			return err
		}
		if !bytes.Equal(latest, original) {
			return fmt.Errorf("%s changed during update; preserving external edit", target)
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("%s appeared during update; preserving external edit", target)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
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
