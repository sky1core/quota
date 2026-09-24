package overlayruntime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

type legacyRecord struct {
	Generated map[string]string `json:"generated"`
}

func statePath(r repoContext) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "quota", "instructions", fmt.Sprintf("%x.json", sha256.Sum256([]byte(r.Common)))), nil
}

func readLegacyRecord(path string) (legacyRecord, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return legacyRecord{}, false, nil
	}
	if err != nil {
		return legacyRecord{}, true, err
	}
	var record legacyRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return legacyRecord{}, true, fmt.Errorf("parse %s: %w", path, err)
	}
	return record, true, nil
}

func sortedRecordPaths(record legacyRecord) []string {
	paths := make([]string, 0, len(record.Generated))
	for path := range record.Generated {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (r repoContext) legacyRemnants() (string, []string, error) {
	path, err := statePath(r)
	if err != nil {
		return "", nil, err
	}
	record, present, err := readLegacyRecord(path)
	if err != nil {
		return path, nil, err
	}
	if !present {
		return "", nil, nil
	}
	var remaining []string
	for _, generated := range sortedRecordPaths(record) {
		if exists(generated) {
			remaining = append(remaining, generated)
		}
	}
	return path, remaining, nil
}

func (r repoContext) cleanupLegacy() ([]string, error) {
	path, err := statePath(r)
	if err != nil {
		return nil, err
	}
	record, present, err := readLegacyRecord(path)
	if err != nil {
		return []string{err.Error() + "; previous generated files were left in place"}, nil
	}
	if !present {
		return nil, nil
	}
	var notices []string
	for _, generated := range sortedRecordPaths(record) {
		if notice := r.removeLegacyFile(generated, record.Generated[generated]); notice != "" {
			notices = append(notices, notice)
		}
	}
	for _, stale := range []string{path, path + ".lock"} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			notices = append(notices, err.Error())
		}
	}
	return notices, nil
}

func (r repoContext) removeLegacyFile(path, recorded string) string {
	if (filepath.Base(path) == "AGENTS.md" && filepath.Base(filepath.Dir(path)) != ".claude") || path == r.localSource() {
		return path + " is an instruction source and was left in place"
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return err.Error()
	}
	if !info.Mode().IsRegular() {
		return path + " is not a regular file; left in place"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != recorded {
		return path + " differs from the quota-generated content; left in place"
	}
	tracked, err := r.tracked(path)
	if err != nil {
		return path + " could not be checked for Git tracking; left in place: " + err.Error()
	}
	if tracked {
		return path + " is tracked by Git; left in place"
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err.Error()
	}
	if dir := filepath.Dir(path); filepath.Base(dir) == "rules" && filepath.Base(filepath.Dir(dir)) == ".claude" {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !isNotEmpty(err) {
			return err.Error()
		}
	}
	return ""
}

func isNotEmpty(err error) bool {
	var errno syscall.Errno
	if pathErr, ok := err.(*os.PathError); ok {
		errno, _ = pathErr.Err.(syscall.Errno)
	}
	return errno == syscall.ENOTEMPTY || errno == syscall.EEXIST
}
