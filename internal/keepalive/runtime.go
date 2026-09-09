package keepalive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Account struct {
	Provider, Key, Home string
}

type Candidate struct {
	Provider, Account, Home, SessionID string
	PID                                int
	LastActivity                       time.Time
	ActivityID, Transcript, Socket     string
}

func (c Candidate) Key() string {
	return fmt.Sprintf("%d:%s%d:%s%d:%s", len(c.Provider), c.Provider, len(c.Account), c.Account, len(c.SessionID), c.SessionID)
}

type Receipt struct {
	ActivityID string
	Confirmed  bool
	Cache      *CacheUsage
}

type CacheUsage struct {
	InputTokens  int64
	CachedTokens int64
}

func runtimeAddTokens(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

type Runtime struct{ Accounts []Account }

var runtimeUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var runtimeDeliveries sync.Map

func (r *Runtime) Scan(ctx context.Context, now time.Time, maxAge time.Duration, ignored map[string]string) ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() || maxAge <= 0 {
		return nil, errors.New("keepalive requires an explicit time and positive activity window")
	}
	accounts, err := r.runtimeAccounts()
	if err != nil {
		return nil, err
	}
	var candidates []Candidate
	var failures []error
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var found []Candidate
		var err error
		if account.Provider == "codex" {
			found, err = scanCodexRuntime(ctx, account, now, maxAge)
		} else {
			found, err = scanClaudeRuntime(ctx, account, now, maxAge)
		}
		if err != nil {
			failures = append(failures, err)
		}
		for _, candidate := range found {
			if candidate.ActivityID != ignored[candidate.Key()] {
				candidates = append(candidates, candidate)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Key() < candidates[j].Key() })
	return candidates, errors.Join(failures...)
}

func (r *Runtime) Deliver(ctx context.Context, c Candidate, message string, maxAge time.Duration, messageID string, ready func() bool) (Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if maxAge <= 0 || strings.TrimSpace(message) == "" || len(message) > 8192 || strings.IndexByte(message, 0) >= 0 || !runtimeUUID.MatchString(messageID) || ready == nil {
		return Receipt{}, errors.New("invalid keepalive delivery input")
	}
	accounts, err := r.runtimeAccounts()
	if err != nil {
		return Receipt{}, err
	}
	var account Account
	for _, a := range accounts {
		if a.Provider == c.Provider && a.Key == c.Account && a.Home == c.Home {
			account = a
		}
	}
	if account.Home == "" || !runtimeUUID.MatchString(c.SessionID) || c.PID <= 1 || c.ActivityID == "" {
		return Receipt{}, errors.New("keepalive candidate does not match a configured account and live session")
	}
	key := c.Key()
	if _, loaded := runtimeDeliveries.LoadOrStore(key, struct{}{}); loaded {
		return Receipt{}, errors.New("keepalive delivery already in progress")
	}
	defer runtimeDeliveries.Delete(key)
	if c.Provider == "codex" {
		return deliverCodexRuntime(ctx, account, c, message, maxAge, messageID, ready)
	}
	return deliverClaudeRuntime(ctx, account, c, message, maxAge, messageID, ready)
}

func (r *Runtime) runtimeAccounts() ([]Account, error) {
	if r == nil {
		return nil, errors.New("keepalive runtime is missing")
	}
	accounts := make([]Account, 0, len(r.Accounts))
	keys, homes := map[string]bool{}, map[string]bool{}
	for _, a := range r.Accounts {
		if (a.Provider != "claude" && a.Provider != "codex") || a.Key == "" || !filepath.IsAbs(a.Home) {
			return nil, errors.New("keepalive account requires a supported provider, key, and explicit absolute home")
		}
		home, err := filepath.EvalSymlinks(a.Home)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("keepalive account home is unavailable")
		}
		a.Home = filepath.Clean(home)
		key := (Candidate{Provider: a.Provider, Account: a.Key}).Key()
		homeKey := a.Provider + "\x00" + a.Home
		if keys[key] || homes[homeKey] {
			return nil, errors.New("keepalive account ownership is ambiguous")
		}
		keys[key], homes[homeKey] = true, true
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func runtimeRecent(activity, now time.Time, maxAge time.Duration) bool {
	return !activity.IsZero() && !activity.After(now) && now.Sub(activity) <= maxAge
}

func runtimeMessageID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", errors.New("keepalive correlation ID unavailable")
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	s := hex.EncodeToString(bytes[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
