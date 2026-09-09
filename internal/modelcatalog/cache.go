package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const MaxAge = 3 * time.Hour

type Snapshot struct {
	SchemaVersion int       `json:"schemaVersion"`
	Provider      string    `json:"provider"`
	Binary        string    `json:"binary"`
	ConfigDir     string    `json:"configDir"`
	CLIVersion    string    `json:"cliVersion"`
	FetchedAt     time.Time `json:"fetchedAt"`
	Models        []Model   `json:"models"`
}

type Cache struct {
	Dir string
}

func (c Cache) Get(ctx context.Context, target Target, force bool) (Snapshot, error) {
	if c.Dir == "" {
		return Snapshot{}, errors.New("model cache directory is required")
	}
	if target.Provider != "claude" && target.Provider != "codex" {
		return Snapshot{}, fmt.Errorf("unknown model provider %q", target.Provider)
	}
	if !filepath.IsAbs(target.Binary) || !filepath.IsAbs(target.ConfigDir) {
		return Snapshot{}, errors.New("model discovery requires absolute binary and account directory paths")
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return Snapshot{}, err
	}
	path := filepath.Join(c.Dir, cacheKey(target)+".json")
	unlock, err := lockCache(ctx, path+".lock")
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	version, err := CLIVersion(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}
	if !force {
		cached, err := readSnapshot(path)
		if err != nil && !os.IsNotExist(err) {
			return Snapshot{}, err
		}
		if err == nil && cached.fresh(target, version, time.Now()) {
			return cached, nil
		}
	}
	models, err := Discover(ctx, target)
	if err != nil {
		return Snapshot{}, fmt.Errorf("model catalog refresh failed: %w", err)
	}
	if len(models) == 0 {
		return Snapshot{}, errors.New("model catalog refresh returned no models")
	}
	currentVersion, err := CLIVersion(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}
	if currentVersion != version {
		return Snapshot{}, errors.New("CLI version changed during model discovery; retry refresh")
	}
	snapshot := Snapshot{SchemaVersion: 1, Provider: target.Provider, Binary: target.Binary,
		ConfigDir: target.ConfigDir, CLIVersion: version, FetchedAt: time.Now(), Models: models}
	if err := writeSnapshot(path, snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func cacheKey(target Target) string {
	identity, _ := json.Marshal([]string{target.Provider, target.Binary, target.ConfigDir})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])
}

func (s Snapshot) fresh(target Target, version string, now time.Time) bool {
	return s.SchemaVersion == 1 && s.Provider == target.Provider && s.Binary == target.Binary &&
		s.ConfigDir == target.ConfigDir && version != "" && s.CLIVersion == version &&
		!s.FetchedAt.IsZero() && !now.Before(s.FetchedAt) && now.Sub(s.FetchedAt) < MaxAge && len(s.Models) > 0
}

func readSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("invalid model cache; use models refresh: %w", err)
	}
	if len(snapshot.Models) == 0 {
		return Snapshot{}, errors.New("invalid model cache: empty models; use models refresh")
	}
	for _, model := range snapshot.Models {
		if strings.TrimSpace(model.ID) == "" {
			return Snapshot{}, errors.New("invalid model cache: missing model ID; use models refresh")
		}
		for _, effort := range model.SupportedEfforts {
			if strings.TrimSpace(effort) == "" {
				return Snapshot{}, errors.New("invalid model cache: empty effort value; use models refresh")
			}
		}
	}
	return snapshot, nil
}

func writeSnapshot(path string, snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".models-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func lockCache(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
