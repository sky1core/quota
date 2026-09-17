package overlayruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

type RepositoryState struct {
	snapshot       []byte
	Generated      map[string]string `json:"generated,omitempty"`
	GeneratedModes map[string]uint32 `json:"generated_modes,omitempty"`
	LocalFiles     []string          `json:"local_files,omitempty"`
}

func instructionsStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "quota", "instructions"), nil
}

func statePath(r repoContext) (string, error) {
	dir, err := instructionsStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(r.Common)))), nil
}

func legacyStatePath(r repoContext) string { return filepath.Join(r.Common, "quota-instructions.json") }

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func recordGeneratedFile(state *RepositoryState, path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular generated file", path)
	}
	if state.Generated == nil {
		state.Generated = map[string]string{}
	}
	if state.GeneratedModes == nil {
		state.GeneratedModes = map[string]uint32{}
	}
	state.Generated[path] = digest(data)
	state.GeneratedModes[path] = uint32(info.Mode())
	return nil
}

func forgetGeneratedFile(state *RepositoryState, path string) {
	delete(state.Generated, path)
	delete(state.GeneratedModes, path)
}

func checkRecordedGeneratedMode(path string, state RepositoryState) error {
	mode, recorded := state.GeneratedModes[path]
	if !recorded {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; file preserved", path)
	}
	if info.Mode() != os.FileMode(mode) {
		return fmt.Errorf("%s permissions changed after generation; file preserved", path)
	}
	return nil
}

func readOwnerOnlyFile(p string) ([]byte, error) {
	info, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s requires owner-only permissions", p)
	}
	return readRegular(p)
}

func decodeState(b []byte, path string, strict bool) (RepositoryState, error) {
	if trimmed := bytes.TrimSpace(b); len(trimmed) == 0 || trimmed[0] != '{' {
		return RepositoryState{}, fmt.Errorf("parse instructions state %s: top-level value is not a JSON object", path)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	var parsed RepositoryState
	if e := dec.Decode(&parsed); e != nil {
		return parsed, fmt.Errorf("parse instructions state %s: %w", path, e)
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return parsed, fmt.Errorf("invalid trailing content in instructions state %s", path)
	}
	if parsed.Generated == nil {
		parsed.Generated = map[string]string{}
	}
	if parsed.GeneratedModes == nil {
		parsed.GeneratedModes = map[string]uint32{}
	}
	if e := validateLocalFiles(parsed.LocalFiles); e != nil {
		return parsed, fmt.Errorf("invalid local_files in instructions state %s: %w", path, e)
	}
	return parsed, nil
}

// readState loads the account-level state for the repository. When no state
// exists yet, ownership and local-file registrations are carried over from
// the legacy in-repository file, which is never updated or removed.
func readState(r repoContext) (RepositoryState, error) {
	p, e := statePath(r)
	if e != nil {
		return RepositoryState{}, e
	}
	if exists(p) {
		b, e := readOwnerOnlyFile(p)
		if e != nil {
			return RepositoryState{}, e
		}
		s, e := decodeState(b, p, true)
		if e != nil {
			return s, e
		}
		s.snapshot = b
		return s, nil
	}
	legacy := legacyStatePath(r)
	if !exists(legacy) {
		return RepositoryState{Generated: map[string]string{}, GeneratedModes: map[string]uint32{}}, nil
	}
	b, e := readOwnerOnlyFile(legacy)
	if e != nil {
		return RepositoryState{}, e
	}
	s, e := decodeState(b, legacy, false)
	if e != nil {
		return s, e
	}
	return RepositoryState{Generated: s.Generated, GeneratedModes: s.GeneratedModes, LocalFiles: s.LocalFiles}, nil
}

func writeState(r repoContext, s RepositoryState) error {
	p, e := statePath(r)
	if e != nil {
		return e
	}
	sort.Strings(s.LocalFiles)
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	data := append(b, '\n')
	if bytes.Equal(s.snapshot, data) {
		return nil
	}
	if e = safeDirectory(filepath.Dir(p)); e != nil {
		return e
	}
	return atomicWriteFile(p, data, true, false, s.snapshot)
}

func RemoveRepositoryState(ctx context.Context, dir string) error {
	if err := ValidateGitEnvironment(ctx); err != nil {
		return err
	}
	r, err := resolveContext(ctx, dir)
	if err != nil {
		return err
	}
	p, err := statePath(r)
	if err != nil {
		return err
	}
	for _, path := range []string{p, p + ".lock"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func withStateLock(r repoContext, fn func() error) error {
	p, e := statePath(r)
	if e != nil {
		return e
	}
	if e = safeDirectory(filepath.Dir(p)); e != nil {
		return e
	}
	fd, e := unix.Open(p+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil {
		return e
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0o077 != 0 {
		return fmt.Errorf("unsafe instructions state lock %s.lock", p)
	}
	for {
		e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if e == nil {
			break
		}
		if e != unix.EAGAIN && e != unix.EWOULDBLOCK {
			return e
		}
		select {
		case <-r.Context.Done():
			return r.Context.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn()
}
