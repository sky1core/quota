package keepalive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Enabled         bool     `json:"enabled"`
	Weekdays        []string `json:"weekdays"`
	Time            string   `json:"time"`
	IdleMinutes     int      `json:"idleMinutes"`
	ActivityMinutes int      `json:"activityMinutes"`
	Message         string   `json:"message"`
}

func DefaultConfig() Config {
	return Config{Weekdays: []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, Time: "12:30", IdleMinutes: 5, ActivityMinutes: 50, Message: "Do not use tools, change files, or start any work. Reply with OK only."}
}

func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	p := plain(DefaultConfig())
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Config(p)
	return nil
}

func (c Config) Validate() error {
	if len(c.Weekdays) == 0 {
		return errors.New("keepalive.weekdays must not be empty")
	}
	seen := map[string]bool{}
	for _, d := range c.Weekdays {
		if !map[string]bool{"Sun": true, "Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true}[d] || seen[d] {
			return fmt.Errorf("invalid or duplicate keepalive weekday %q", d)
		}
		seen[d] = true
	}
	if _, err := time.Parse("15:04", c.Time); err != nil || len(c.Time) != 5 {
		return errors.New("keepalive.time must be HH:MM")
	}
	if c.IdleMinutes < 1 || c.IdleMinutes > 1440 {
		return errors.New("keepalive.idleMinutes must be between 1 and 1440")
	}
	if c.ActivityMinutes < 1 || c.ActivityMinutes > 1440 {
		return errors.New("keepalive.activityMinutes must be between 1 and 1440")
	}
	if strings.TrimSpace(c.Message) == "" || len(c.Message) > 8192 || strings.ContainsRune(c.Message, 0) {
		return errors.New("keepalive.message must contain 1–8192 bytes")
	}
	return nil
}

func due(c Config, previous, now time.Time) bool {
	if !c.Enabled || previous.IsZero() || !now.After(previous) || now.Sub(previous) > 30*time.Second {
		return false
	}
	allowed := false
	for _, day := range c.Weekdays {
		if day == now.Weekday().String()[:3] {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	at, err := time.Parse("15:04", c.Time)
	if err != nil {
		return false
	}
	scheduled := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
	return previous.Before(scheduled) && !now.Before(scheduled) && now.Sub(scheduled) <= 30*time.Second
}

type ignoredActivity struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

type ledger struct {
	Day     string                     `json:"day"`
	Ignored map[string]ignoredActivity `json:"ignored"`
}

func updateLedger(path string, fn func(*ledger) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	l := ledger{Ignored: map[string]ignoredActivity{}}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal(b, &l); err != nil {
			return fmt.Errorf("keepalive state: %w", err)
		}
		if l.Day == "" {
			return errors.New("keepalive state date is missing")
		}
		if l.Day != "" {
			if _, err = time.Parse("2006-01-02", l.Day); err != nil {
				return fmt.Errorf("keepalive state date: %w", err)
			}
		}
	}
	if l.Ignored == nil {
		l.Ignored = map[string]ignoredActivity{}
	}
	if err = fn(&l); err != nil {
		return err
	}
	b, err = json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".keepalive-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var alreadyAttempted = errors.New("already attempted today")

func claimDay(path string, now time.Time) (map[string]string, error) {
	ignored := map[string]string{}
	err := updateLedger(path, func(l *ledger) error {
		today := now.Format("2006-01-02")
		if l.Day >= today {
			return alreadyAttempted
		}
		l.Day = today
		for key, v := range l.Ignored {
			if now.Sub(v.At) > 48*time.Hour {
				delete(l.Ignored, key)
			} else {
				ignored[key] = v.ID
			}
		}
		return nil
	})
	return ignored, err
}

func prepareActivity(path, key string, now time.Time) (string, error) {
	id, err := runtimeMessageID()
	if err != nil {
		return "", err
	}
	err = updateLedger(path, func(l *ledger) error {
		if l.Day == "" {
			return errors.New("keepalive schedule claim is missing")
		}
		l.Ignored[key] = ignoredActivity{ID: id, At: now}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

type Result struct {
	CacheHits, CacheMisses, CacheUnknown int
	CacheDetails                         []string
	Status                               string
	Selected, Confirmed, Unconfirmed     int
	Error                                error
}

type Service struct {
	mu          sync.Mutex
	config      Config
	previous    time.Time
	cancel      context.CancelFunc
	runtime     *Runtime
	statePath   string
	idleSeconds func() float64
}

func NewService(runtime *Runtime, statePath string, idleSeconds func() float64) *Service {
	return &Service{runtime: runtime, statePath: statePath, idleSeconds: idleSeconds}
}

func (s *Service) Configure(c Config, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.config = Config{}
	s.previous = now
	if err := c.Validate(); err != nil {
		return err
	}
	c.Weekdays = append([]string(nil), c.Weekdays...)
	s.config = c
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.config.Enabled = false
}

func (s *Service) Tick(parent context.Context, now time.Time) Result {
	s.mu.Lock()
	c := s.config
	run := due(c, s.previous, now)
	s.previous = now
	if !run {
		s.mu.Unlock()
		return Result{}
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	ignored, err := claimDay(s.statePath, now)
	if errors.Is(err, alreadyAttempted) {
		return Result{Status: "Already checked today"}
	}
	if err != nil {
		return Result{Status: "State unavailable", Error: err}
	}
	if !s.isInactive(c) {
		return Result{Status: "Skipped: PC active or idle time unavailable"}
	}
	maxAge := time.Duration(c.ActivityMinutes) * time.Minute
	candidates, scanErr := s.runtime.Scan(ctx, time.Now(), maxAge, ignored)
	result := Result{Status: "Checked", Selected: len(candidates), Error: scanErr}
	for _, candidate := range candidates {
		if ctx.Err() != nil || !s.isInactive(c) {
			result.Status = "Stopped: disabled, interrupted, or PC active"
			break
		}
		id, err := prepareActivity(s.statePath, candidate.Key(), time.Now())
		if err != nil {
			result.Error = errors.Join(result.Error, err)
			break
		}
		receipt, sendErr := s.runtime.Deliver(ctx, candidate, c.Message, maxAge, id, func() bool { return ctx.Err() == nil && s.isInactive(c) })
		result.addReceipt(candidate, receipt)
		if sendErr != nil {
			result.Error = errors.Join(result.Error, sendErr)
		}
	}
	return result
}

func (s *Service) isInactive(c Config) bool {
	if s.idleSeconds == nil {
		return false
	}
	seconds := s.idleSeconds()
	return !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds >= float64(c.IdleMinutes)*60
}

func (r *Result) addReceipt(c Candidate, receipt Receipt) {
	if !receipt.Confirmed {
		r.Unconfirmed++
		return
	}
	r.Confirmed++
	usage := receipt.Cache
	if usage == nil || usage.InputTokens <= 0 || usage.CachedTokens < 0 || usage.CachedTokens > usage.InputTokens {
		r.CacheUnknown++
		r.CacheDetails = append(r.CacheDetails, fmt.Sprintf("%s: cache usage unavailable", c.Account))
		return
	}
	if usage.CachedTokens > 0 {
		r.CacheHits++
	} else {
		r.CacheMisses++
	}
	r.CacheDetails = append(r.CacheDetails, fmt.Sprintf("%s: %d/%d input tokens cached", c.Account, usage.CachedTokens, usage.InputTokens))
}
