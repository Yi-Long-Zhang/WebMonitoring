package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

// ErrAccountLocked is returned when an account has exceeded the allowed number
// of failed login attempts and is within its lockout window.
type ErrAccountLocked struct {
	RetryAfter time.Duration
}

func (e *ErrAccountLocked) Error() string {
	return fmt.Sprintf("account locked, retry after %s", e.RetryAfter)
}

// ErrRateLimited is returned when a source IP exceeds the coarse per-IP login
// throttle. The per-account lockout is the primary control; this is a blunt
// secondary that remains meaningful behind a shared reverse proxy.
type ErrRateLimited struct {
	RetryAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}

// loginStateStore is the shared backing store for login throttle state. It is
// deliberately small so it can be implemented over either an in-process map
// (dev / tests) or Redis (production, shared across replicas).
type loginStateStore interface {
	Incr(ctx context.Context, key string) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
	Reset(ctx context.Context, key string) error
	Expire(ctx context.Context, key string, ttl time.Duration) error
}

// LoginGuard enforces per-account lockout (primary) and a coarse per-IP
// throttle (secondary). All methods are nil-safe so a server without a guard
// (tests, embedded mode) behaves as if throttling is disabled.
type LoginGuard struct {
	store       loginStateStore
	maxFailures int
	lockout     time.Duration
	maxIP       int
	ipWindow    time.Duration
}

// NewLoginGuard builds a guard from an existing store. maxFailures and lockout
// are validated to safe defaults when non-positive.
func NewLoginGuard(store loginStateStore, maxFailures int, lockout time.Duration) *LoginGuard {
	if maxFailures <= 0 {
		maxFailures = 5
	}
	if lockout <= 0 {
		lockout = 15 * time.Minute
	}
	return &LoginGuard{
		store:       store,
		maxFailures: maxFailures,
		lockout:     lockout,
		maxIP:       30,
		ipWindow:    time.Minute,
	}
}

// NewMemoryLoginGuard returns a guard backed by an in-process store. Suitable
// for development, tests, and the embedded (no REDIS_ADDR) deployment mode.
func NewMemoryLoginGuard(maxFailures int, lockout time.Duration) *LoginGuard {
	return NewLoginGuard(newMemoryLoginState(), maxFailures, lockout)
}

// NewRedisLoginGuard returns a guard backed by Redis at addr, shared across
// replicas. The client connects lazily; if Redis is unreachable the throttle
// fails open (the argon2 password gate remains authoritative).
func NewRedisLoginGuard(addr string, maxFailures int, lockout time.Duration) *LoginGuard {
	return NewLoginGuard(newRedisLoginState(addr), maxFailures, lockout)
}

func (g *LoginGuard) accountKey(tenantID, username string) string {
	return "login:fail:" + tenantID + ":" + strings.ToLower(strings.TrimSpace(username))
}

// BeforeLogin checks whether the account is locked out or the source IP is
// throttled. It must be called before credential verification; it returns a
// nil error when the attempt may proceed. Throttle-state read/write failures
// fail open so an unavailable Redis never blocks legitimate logins.
func (g *LoginGuard) BeforeLogin(ctx context.Context, tenantID, username, remoteIP string) error {
	if g == nil {
		return nil
	}
	count, err := g.store.Get(ctx, g.accountKey(tenantID, username))
	if err != nil {
		return nil
	}
	if count >= int64(g.maxFailures) {
		return &ErrAccountLocked{RetryAfter: g.lockout}
	}
	if remoteIP == "" {
		return nil
	}
	key := "login:ip:" + remoteIP
	ipCount, err := g.store.Incr(ctx, key)
	if err != nil {
		return nil
	}
	if ipCount == 1 {
		_ = g.store.Expire(ctx, key, g.ipWindow)
	}
	if ipCount > int64(g.maxIP) {
		return &ErrRateLimited{RetryAfter: g.ipWindow}
	}
	return nil
}

// RecordFailure increments the per-account failure counter and, on reaching
// the threshold, arms the lockout window. Failures are recorded whether or not
// the account exists so the counter (and timing) does not leak that fact.
func (g *LoginGuard) RecordFailure(ctx context.Context, tenantID, username string) {
	if g == nil {
		return
	}
	key := g.accountKey(tenantID, username)
	count, err := g.store.Incr(ctx, key)
	if err != nil {
		return
	}
	if count == 1 || count >= int64(g.maxFailures) {
		_ = g.store.Expire(ctx, key, g.lockout)
	}
}

// RecordSuccess clears the per-account failure counter after a successful login.
func (g *LoginGuard) RecordSuccess(ctx context.Context, tenantID, username string) {
	if g == nil {
		return
	}
	_ = g.store.Reset(ctx, g.accountKey(tenantID, username))
}

type loginEntry struct {
	count int64
	exp   time.Time
}

type memoryLoginState struct {
	mu sync.Mutex
	m  map[string]loginEntry
}

func newMemoryLoginState() *memoryLoginState {
	return &memoryLoginState{m: make(map[string]loginEntry)}
}

func (s *memoryLoginState) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.m[key]
	if !ok || (!entry.exp.IsZero() && time.Now().After(entry.exp)) {
		s.m[key] = loginEntry{count: 1}
		return 1, nil
	}
	entry.count++
	s.m[key] = entry
	return entry.count, nil
}

func (s *memoryLoginState) Get(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.m[key]
	if !ok {
		return 0, nil
	}
	if !entry.exp.IsZero() && time.Now().After(entry.exp) {
		delete(s.m, key)
		return 0, nil
	}
	return entry.count, nil
}

func (s *memoryLoginState) Reset(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

func (s *memoryLoginState) Expire(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.m[key]; ok {
		entry.exp = time.Now().Add(ttl)
		s.m[key] = entry
	}
	return nil
}

type redisLoginState struct {
	client *redis.Client
}

func newRedisLoginState(addr string) *redisLoginState {
	host, password := config.ParseRedisAddress(addr)
	return &redisLoginState{client: redis.NewClient(&redis.Options{Addr: host, Password: password})}
}

func (s *redisLoginState) Incr(ctx context.Context, key string) (int64, error) {
	return s.client.Incr(ctx, key).Result()
}

func (s *redisLoginState) Get(ctx context.Context, key string) (int64, error) {
	value, err := s.client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return value, err
}

func (s *redisLoginState) Reset(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

func (s *redisLoginState) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}
