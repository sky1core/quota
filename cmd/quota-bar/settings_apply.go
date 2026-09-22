package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/keepalive"
	"github.com/sky1core/quota/internal/update"
)

type liveSettingsItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type liveSettingsAccount struct {
	Provider   string   `json:"provider"`
	Key        string   `json:"key"`
	ConfigDir  string   `json:"configDir"`
	MinLeftPct *float64 `json:"minLeftPct"`
}

type liveSettingsUpdate struct {
	Mode string `json:"mode"`
	Ref  string `json:"ref"`
}

type liveSettingsDraft struct {
	RefreshActiveMinutes int                   `json:"refreshActiveMinutes"`
	RefreshIdleMinutes   int                   `json:"refreshIdleMinutes"`
	ShowResetTime        bool                  `json:"showResetTime"`
	StartAtLogin         bool                  `json:"startAtLogin"`
	Selected             []string              `json:"selected"`
	AvailableItems       []liveSettingsItem    `json:"availableItems"`
	Accounts             []liveSettingsAccount `json:"accounts"`
	Keepalive            keepalive.Config      `json:"keepalive"`
	Update               liveSettingsUpdate    `json:"update"`
}

type settingsFileSnapshot struct {
	path     string
	target   string
	content  []byte
	exists   bool
	mode     os.FileMode
	modified time.Time
}

type liveSettingsSnapshot struct {
	bar        settingsFileSnapshot
	shared     settingsFileSnapshot
	login      settingsFileSnapshot
	draft      liveSettingsDraft
	generation uint64
}

func readSettingsFile(path string) (settingsFileSnapshot, error) {
	s := settingsFileSnapshot{path: path, mode: 0600}
	target, err := filepath.Abs(path)
	if err != nil {
		return s, err
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else if !os.IsNotExist(err) {
		return s, err
	} else {
		parent, parentErr := settingsAccountDirectory(filepath.Dir(target))
		if parentErr != nil {
			return s, parentErr
		}
		target = filepath.Join(parent, filepath.Base(target))
	}
	s.target = target
	s.content, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return s, err
	}
	s.exists, s.mode, s.modified = true, info.Mode().Perm(), info.ModTime()
	return s, nil
}

func (s settingsFileSnapshot) check() error {
	now, err := readSettingsFile(s.path)
	if err != nil {
		return err
	}
	if s.target != now.target || s.exists != now.exists || !s.modified.Equal(now.modified) || s.mode != now.mode || !bytes.Equal(s.content, now.content) {
		return fmt.Errorf("%s changed outside this window; close and reopen Settings", filepath.Base(s.path))
	}
	return nil
}

func decodeSettingsObject(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var root map[string]any
	if err := d.Decode(&root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("settings must be a JSON object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	return root, nil
}

func snapshotLiveSettings(generation uint64, data quotaData, keepaliveStoppedWithoutSave bool) (liveSettingsSnapshot, error) {
	var s liveSettingsSnapshot
	var err error
	if s.bar, err = readSettingsFile(settingsPath()); err != nil {
		return s, err
	}
	if s.shared, err = readSettingsFile(config.Path()); err != nil {
		return s, err
	}
	if s.login, err = readSettingsFile(launchAgentPath()); err != nil {
		return s, err
	}
	if _, err = decodeSettingsObject(s.bar.content); err != nil {
		return s, err
	}
	if _, err = decodeSettingsObject(s.shared.content); err != nil {
		return s, err
	}
	cfg := settings{}
	if s.bar.exists {
		if err = json.Unmarshal(s.bar.content, &cfg); err != nil {
			return s, err
		}
	}
	var shared config.Config
	if s.shared.exists {
		if err = json.Unmarshal(s.shared.content, &shared); err != nil {
			return s, err
		}
	}
	d := liveSettingsDraft{
		RefreshActiveMinutes: int(cfg.activeInterval().Minutes()), RefreshIdleMinutes: int(cfg.idleInterval().Minutes()),
		ShowResetTime: cfg.ShowResetTime, StartAtLogin: s.login.exists,
		Selected: append([]string{}, cfg.Selected...), Keepalive: keepalive.DefaultConfig(),
		Update: liveSettingsUpdate{Mode: "latest"},
	}
	if shared.Update != nil {
		d.Update = liveSettingsUpdateFromRef(shared.Update.Ref)
	}
	if cfg.Keepalive != nil {
		d.Keepalive = *cfg.Keepalive
		d.Keepalive.Weekdays = append([]string(nil), cfg.Keepalive.Weekdays...)
	}
	add := func(provider, key, dir string) {
		var floor *float64
		if shared.ExecPrompt != nil {
			if value := shared.ExecPrompt.AccountSettings[key].MinLeftPct; value != nil {
				v := *value
				floor = &v
			}
		}
		d.Accounts = append(d.Accounts, liveSettingsAccount{provider, key, dir, floor})
		keys := claude.WindowKeys()
		if provider == "codex" {
			keys = codex.WindowKeys()
		}
		for _, wk := range keys {
			key := itemKey(key, wk)
			label := data.labels[key]
			if label == "" {
				continue
			}
			d.AvailableItems = append(d.AvailableItems, liveSettingsItem{key, providerOf(key) + " · " + label})
		}
	}
	add("claude", "claude", "")
	for _, a := range shared.ClaudeAccounts {
		add("claude", a.Key, a.ConfigDir)
	}
	add("codex", "codex", "")
	for _, a := range shared.CodexAccounts {
		add("codex", a.Key, a.Home)
	}
	for _, key := range d.Selected {
		found := false
		for _, i := range d.AvailableItems {
			if i.Key == key {
				found = true
			}
		}
		if !found {
			d.AvailableItems = append(d.AvailableItems, liveSettingsItem{key, key + " (unavailable)"})
		}
	}
	if d.AvailableItems == nil {
		d.AvailableItems = []liveSettingsItem{}
	}
	if keepaliveStoppedWithoutSave {
		d.Keepalive.Enabled = false
	}
	s.draft, s.generation = d, generation
	return s, nil
}

func decodeLiveSettings(raw string) (liveSettingsDraft, error) {
	var d liveSettingsDraft
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return d, errors.New("trailing settings content")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return d, err
	}
	for _, key := range []string{"refreshActiveMinutes", "refreshIdleMinutes", "showResetTime", "startAtLogin", "selected", "accounts", "keepalive", "update"} {
		if v, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return d, fmt.Errorf("%s is required", key)
		}
	}
	var keepaliveFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["keepalive"], &keepaliveFields); err != nil {
		return d, err
	}
	allowed := map[string]bool{}
	for _, key := range []string{"enabled", "weekdays", "time", "idleMinutes", "activityMinutes", "message"} {
		allowed[key] = true
		if value, ok := keepaliveFields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return d, fmt.Errorf("keepalive.%s is required", key)
		}
	}
	for key := range keepaliveFields {
		if !allowed[key] {
			return d, fmt.Errorf("unknown keepalive field %q", key)
		}
	}
	var updateFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["update"], &updateFields); err != nil {
		return d, err
	}
	allowed = map[string]bool{}
	for _, key := range []string{"mode", "ref"} {
		allowed[key] = true
		if value, ok := updateFields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return d, fmt.Errorf("update.%s is required", key)
		}
	}
	for key := range updateFields {
		if !allowed[key] {
			return d, fmt.Errorf("unknown update field %q", key)
		}
	}
	return d, nil
}

func liveSettingsUpdateFromRef(ref string) liveSettingsUpdate {
	switch ref {
	case "", "latest":
		return liveSettingsUpdate{Mode: "latest"}
	case "main":
		return liveSettingsUpdate{Mode: "main", Ref: "main"}
	default:
		return liveSettingsUpdate{Mode: "ref", Ref: ref}
	}
}

func liveSettingsUpdateRef(u liveSettingsUpdate) (string, error) {
	switch u.Mode {
	case "latest":
		if u.Ref != "" && u.Ref != "latest" {
			return "", errors.New(`update.ref must be empty when update.mode is "latest"`)
		}
		return "", nil
	case "main":
		if u.Ref != "" && u.Ref != "main" {
			return "", errors.New(`update.ref must be "main" when update.mode is "main"`)
		}
		return "main", nil
	case "ref":
		if u.Ref == "" {
			return "", errors.New("update ref is required")
		}
		return update.NormalizeRef(u.Ref)
	default:
		return "", fmt.Errorf("invalid update mode %q", u.Mode)
	}
}

func validateLiveSettings(d liveSettingsDraft) (settings, config.Config, error) {
	var shared config.Config
	bar := settings{Selected: append([]string(nil), d.Selected...), ShowResetTime: d.ShowResetTime,
		RefreshActiveMinutes: d.RefreshActiveMinutes, RefreshIdleMinutes: d.RefreshIdleMinutes, Keepalive: &d.Keepalive}
	for _, n := range []int{d.RefreshActiveMinutes, d.RefreshIdleMinutes} {
		if n < 1 || n > int((math.MaxInt64-int64(staleMargin))/int64(time.Minute)) {
			return bar, shared, errors.New("refresh intervals must be positive whole minutes within the supported duration range")
		}
	}
	if err := d.Keepalive.Validate(); err != nil {
		return bar, shared, err
	}
	ref, err := liveSettingsUpdateRef(d.Update)
	if err != nil {
		return bar, shared, err
	}
	if ref != "" {
		shared.Update = &config.UpdateConfig{Ref: ref}
	}
	seen := map[string]bool{}
	dirs := map[string]map[string]bool{"claude": {}, "codex": {}}
	for _, provider := range []string{"claude", "codex"} {
		canonical, err := config.DefaultAccountDirectory(provider)
		if err != nil {
			return bar, shared, err
		}
		dirs[provider][canonical] = true
	}
	shared.ExecPrompt = &config.ExecPromptConfig{AccountSettings: map[string]config.ExecPromptAccountSettings{}}
	allowedItems := map[string]bool{}
	for _, a := range d.Accounts {
		if seen[a.Key] {
			return bar, shared, fmt.Errorf("duplicate account %q", a.Key)
		}
		seen[a.Key] = true
		if a.Provider != "claude" && a.Provider != "codex" {
			return bar, shared, fmt.Errorf("invalid provider %q", a.Provider)
		}
		if a.Key == a.Provider {
			if a.ConfigDir != "" {
				return bar, shared, fmt.Errorf("default account %s directory is immutable", a.Key)
			}
		} else {
			re := config.ClaudeExtraKeyRe
			if a.Provider == "codex" {
				re = config.CodexExtraKeyRe
			}
			if !re.MatchString(a.Key) {
				return bar, shared, fmt.Errorf("invalid %s account key %q", a.Provider, a.Key)
			}
			dir, err := settingsAccountDirectory(a.ConfigDir)
			if err != nil {
				return bar, shared, fmt.Errorf("%s: %w", a.Key, err)
			}
			if dirs[a.Provider][dir] {
				return bar, shared, fmt.Errorf("%s duplicates a registered or default account directory", a.Key)
			}
			dirs[a.Provider][dir] = true
			if a.Provider == "claude" {
				shared.ClaudeAccounts = append(shared.ClaudeAccounts, config.ClaudeAccount{Key: a.Key, ConfigDir: dir})
			} else {
				shared.CodexAccounts = append(shared.CodexAccounts, config.CodexAccount{Key: a.Key, Home: dir})
			}
		}
		if a.MinLeftPct != nil {
			v := *a.MinLeftPct
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
				return bar, shared, fmt.Errorf("%s minimum remaining quota must be between 0 and 100", a.Key)
			}
			shared.ExecPrompt.AccountSettings[a.Key] = config.ExecPromptAccountSettings{MinLeftPct: &v}
		}
		keys := claude.WindowKeys()
		if a.Provider == "codex" {
			keys = codex.WindowKeys()
		}
		for _, k := range keys {
			allowedItems[itemKey(a.Key, k)] = true
		}
	}
	if !seen["claude"] || !seen["codex"] {
		return bar, shared, errors.New("default Claude and Codex accounts cannot be removed")
	}
	selected := map[string]bool{}
	for _, k := range d.Selected {
		if !allowedItems[k] || selected[k] {
			return bar, shared, fmt.Errorf("invalid or duplicate selected item %q; deselect items for removed accounts", k)
		}
		selected[k] = true
	}
	return bar, shared, nil
}

func settingsAccountDirectory(dir string) (string, error) {
	return config.CanonicalAccountDirectory(dir)
}

func settingsObject(root map[string]any, key string) (map[string]any, error) {
	value, exists := root[key]
	if !exists {
		value = map[string]any{}
		root[key] = value
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
}

func patchLiveSettings(barRoot, sharedRoot map[string]any, bar settings, d liveSettingsDraft, shared config.Config) error {
	barRoot["selected"] = d.Selected
	barRoot["showResetTime"] = d.ShowResetTime
	barRoot["refreshActiveMinutes"] = d.RefreshActiveMinutes
	barRoot["refreshIdleMinutes"] = d.RefreshIdleMinutes
	k, err := settingsObject(barRoot, "keepalive")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(bar.Keepalive)
	if err != nil {
		return err
	}
	fields, err := decodeSettingsObject(raw)
	if err != nil {
		return err
	}
	for key, value := range fields {
		k[key] = value
	}
	directories := map[string]string{}
	for _, a := range shared.ClaudeAccounts {
		directories[a.Key] = a.ConfigDir
	}
	for _, a := range shared.CodexAccounts {
		directories[a.Key] = a.Home
	}
	removed := map[string]bool{}
	for _, provider := range []string{"claude", "codex"} {
		arrayKey, dirKey := "claudeAccounts", "configDir"
		if provider == "codex" {
			arrayKey, dirKey = "codexAccounts", "home"
		}
		old := map[string]map[string]any{}
		if value, exists := sharedRoot[arrayKey]; exists && value != nil {
			rows, ok := value.([]any)
			if !ok {
				return fmt.Errorf("%s must be an array", arrayKey)
			}
			for _, v := range rows {
				row, ok := v.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid %s entry", arrayKey)
				}
				key, _ := row["key"].(string)
				old[key] = row
				removed[key] = true
			}
		}
		rows := []any{}
		for _, a := range d.Accounts {
			if a.Provider != provider || a.Key == provider {
				continue
			}
			row := old[a.Key]
			if row == nil {
				row = map[string]any{}
			}
			row["key"], row[dirKey] = a.Key, directories[a.Key]
			rows = append(rows, row)
		}
		sharedRoot[arrayKey] = rows
	}
	exec, err := settingsObject(sharedRoot, "execPrompt")
	if err != nil {
		return err
	}
	floors, err := settingsObject(exec, "accountSettings")
	if err != nil {
		return err
	}
	for _, a := range d.Accounts {
		delete(removed, a.Key)
		entry, err := settingsObject(floors, a.Key)
		if err != nil {
			return err
		}
		if a.MinLeftPct == nil {
			delete(entry, "minLeftPct")
		} else {
			entry["minLeftPct"] = *a.MinLeftPct
		}
		if len(entry) == 0 {
			delete(floors, a.Key)
		}
	}
	for key := range floors {
		if removed[key] {
			delete(floors, key)
		}
	}
	if shared.Update == nil {
		if value, exists := sharedRoot["update"]; exists && value != nil {
			u, ok := value.(map[string]any)
			if !ok {
				return errors.New("update must be an object")
			}
			delete(u, "ref")
			if len(u) == 0 {
				delete(sharedRoot, "update")
			}
		}
	} else {
		u, err := settingsObject(sharedRoot, "update")
		if err != nil {
			return err
		}
		u["ref"] = shared.Update.Ref
	}
	return nil
}

func settingsObjectHash(root map[string]any) [32]byte {
	b, _ := json.Marshal(root)
	return sha256.Sum256(b)
}

func replaceSettingsObject(root, next map[string]any) {
	clear(root)
	for k, v := range next {
		root[k] = v
	}
}

type settingsRollbackError struct{ err error }

func (e *settingsRollbackError) Error() string {
	return "Settings save failed and rollback is incomplete; keepalive has been stopped. Inspect configuration files and their backups before reopening Settings: " + e.err.Error()
}
func (e *settingsRollbackError) Unwrap() error { return e.err }

func persistLiveSettings(s liveSettingsSnapshot, d liveSettingsDraft, generation uint64) (settings, config.Config, error) {
	if d.Selected == nil {
		d.Selected = []string{}
	}
	bar, shared, err := validateLiveSettings(d)
	if err != nil {
		return bar, shared, err
	}
	if generation != s.generation {
		return bar, shared, errors.New("settings changed since this window opened; close and reopen Settings")
	}
	for _, f := range []settingsFileSnapshot{s.bar, s.shared, s.login} {
		if err = f.check(); err != nil {
			return bar, shared, err
		}
	}
	barRoot, err := decodeSettingsObject(s.bar.content)
	if err != nil {
		return bar, shared, err
	}
	sharedRoot, err := decodeSettingsObject(s.shared.content)
	if err != nil {
		return bar, shared, err
	}
	if err = patchLiveSettings(barRoot, sharedRoot, bar, d, shared); err != nil {
		return bar, shared, err
	}
	targets := map[string]bool{}
	for _, file := range []settingsFileSnapshot{s.bar, s.shared, s.login} {
		if targets[file.target] {
			return bar, shared, errors.New("settings paths must refer to separate files")
		}
		targets[file.target] = true
	}
	var loginContent []byte
	if d.StartAtLogin && !s.login.exists {
		loginContent, err = newLoginRegistration()
		if err != nil {
			return bar, shared, err
		}
	}
	loginChanged := d.StartAtLogin != s.login.exists
	loginWritten := false
	barAttempted := false
	sharedAttempted := false
	_, err = agenthooks.TryUpdateJSONObjectWithBackup(s.shared.path, func(root map[string]any) error {
		if err := s.shared.check(); err != nil {
			return err
		}
		_, err := agenthooks.TryUpdateJSONObjectWithBackup(s.bar.path, func(root map[string]any) error {
			if err := s.bar.check(); err != nil {
				return err
			}
			if err := s.login.check(); err != nil {
				return err
			}
			if loginChanged {
				if err := writeLoginRegistration(s.login, loginContent, d.StartAtLogin); err != nil {
					return err
				}
				loginWritten = true
			}
			replaceSettingsObject(root, barRoot)
			barAttempted = true
			return nil
		})
		if err != nil {
			return err
		}
		if err := s.shared.check(); err != nil {
			return err
		}
		replaceSettingsObject(root, sharedRoot)
		sharedAttempted = true
		return nil
	})
	if err == nil {
		for _, file := range []struct {
			before   settingsFileSnapshot
			expected map[string]any
		}{{s.bar, barRoot}, {s.shared, sharedRoot}} {
			current, readErr := readSettingsFile(file.before.path)
			if readErr != nil {
				err = readErr
				break
			}
			root, decodeErr := decodeSettingsObject(current.content)
			if decodeErr != nil {
				err = decodeErr
				break
			}
			if current.target != file.before.target || settingsObjectHash(root) != settingsObjectHash(file.expected) {
				err = fmt.Errorf("%s changed during save", filepath.Base(file.before.path))
				break
			}
		}
	}
	if err == nil {
		if !loginChanged {
			err = s.login.check()
		} else {
			current, readErr := readSettingsFile(s.login.path)
			err = readErr
			if err == nil && (current.exists != d.StartAtLogin || !bytes.Equal(current.content, loginContent)) {
				err = errors.New("login registration changed during save")
			}
		}
	}
	if err == nil {
		return bar, shared, nil
	}
	var rollbackErr error

	if sharedAttempted {
		rollbackErr = errors.Join(rollbackErr, rollbackSettingsObject(s.shared, sharedRoot))
	}
	if barAttempted {
		rollbackErr = errors.Join(rollbackErr, rollbackSettingsObject(s.bar, barRoot))
	}
	if loginWritten {
		expected, readErr := readSettingsFile(s.login.path)
		if readErr == nil && (expected.exists != d.StartAtLogin || !bytes.Equal(expected.content, loginContent)) {
			readErr = errors.New("login registration changed again; refusing rollback overwrite")
		}
		if readErr == nil {
			readErr = writeLoginRegistration(expected, s.login.content, s.login.exists)
		}
		rollbackErr = errors.Join(rollbackErr, readErr)
	}
	if rollbackErr != nil {
		return bar, shared, &settingsRollbackError{errors.Join(err, rollbackErr)}
	}
	return bar, shared, fmt.Errorf("settings were not applied; saved changes rolled back; close and reopen Settings: %w", err)
}

func rollbackSettingsObject(f settingsFileSnapshot, expected map[string]any) error {
	original, err := decodeSettingsObject(f.content)
	if err != nil {
		return err
	}
	_, err = agenthooks.TryUpdateJSONObjectWithBackup(f.path, func(root map[string]any) error {
		current, err := readSettingsFile(f.path)
		if err != nil {
			return err
		}
		if current.target != f.target {
			return fmt.Errorf("%s target changed; refusing rollback overwrite", filepath.Base(f.path))
		}
		hash := settingsObjectHash(root)
		if hash == settingsObjectHash(original) {
			return nil
		}
		if hash != settingsObjectHash(expected) {
			return fmt.Errorf("%s changed again; refusing rollback overwrite", filepath.Base(f.path))
		}
		if !f.exists {
			return os.Remove(current.target)
		}
		replaceSettingsObject(root, original)
		return nil
	})
	return err
}

func newLoginRegistration() ([]byte, error) {
	real, err := realExecutable()
	if err != nil {
		return nil, err
	}
	if err := ensureAppBundle(real); err != nil {
		return nil, err
	}
	exe := appBundleExecutable()
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	escape := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	var b bytes.Buffer
	err = plistTmpl.Execute(&b, struct{ Label, ExePath, Path, Home string }{launchLabel, escape(exe), escape(pathEnv), escape(home)})
	return b.Bytes(), err
}

func writeLoginRegistration(before settingsFileSnapshot, content []byte, exists bool) error {
	if err := before.check(); err != nil {
		return err
	}
	if !exists {
		if !before.exists {
			return nil
		}
		return os.Remove(before.path)
	}
	if err := os.MkdirAll(filepath.Dir(before.target), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(before.target), ".quota-login-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(before.mode); err != nil {
		return err
	}
	if _, err = f.Write(content); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = before.check(); err != nil {
		return err
	}
	return os.Rename(f.Name(), before.target)
}

const (
	barOperationRefresh uint32 = 1 << iota
	barOperationSettings
	barOperationHandover
)

type barOperationGate struct{ active atomic.Uint32 }

func (g *barOperationGate) begin(operation uint32) bool {
	for {
		current := g.active.Load()
		if current&operation != 0 || current&barOperationHandover != 0 || (operation == barOperationHandover && current != 0) {
			return false
		}
		if g.active.CompareAndSwap(current, current|operation) {
			return true
		}
	}
}

func (g *barOperationGate) end(operation uint32) { g.active.And(^operation) }
