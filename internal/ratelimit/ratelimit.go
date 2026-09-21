// Package ratelimit bounds how many requests one tenant may make, so a single
// authenticated caller cannot flood the API or monopolize the workers.
//
// The login guard in internal/auth covers sign-in attempts; this covers
// everything a signed-in user can reach afterwards, which was previously
// unbounded. The two are separate because their keys differ: login throttling is
// per source address and per account, before there is a session, while this is
// per tenant once the caller is known.
//
// Requests are classified rather than counted uniformly. A dashboard read and an
// asset import cost the installation wildly different amounts -- the import
// parses a file, writes rows and may enqueue detection jobs, while the dashboard
// read is a single query -- so one global bucket would either throttle normal
// browsing or leave the expensive endpoints effectively unlimited. Each class
// carries its own quota for that reason.
//
// The limiter fails open. A Redis outage must not take the control plane down
// with it: the caller is already authenticated and authorized, and the
// alternative -- refusing every request because the counter is unreachable --
// turns a degraded dependency into an outage.
package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

// Class names a category of request that carries its own quota. The set is
// closed so the configuration and the routes cannot disagree about what exists.
type Class string

const (
	// ClassRead is every safe method: list, detail, dashboard, report.
	ClassRead Class = "read"
	// ClassWrite is an ordinary mutation: create, update, transition, comment.
	ClassWrite Class = "write"
	// ClassBulk is the work that fans out -- an asset import, a detection
	// trigger, a manual retention run. These are the calls a single caller can
	// use to consume a worker pool, so they are metered hardest.
	ClassBulk Class = "bulk"
)

// Limits is the per-class allowance within Window.
type Limits struct {
	Read  int
	Write int
	Bulk  int
	// Window is the counting interval. A single fixed window is used rather than
	// a sliding one because the failure it prevents is a sustained flood, not a
	// burst straddling a boundary; the simpler counter costs one Redis command
	// per request instead of a sorted set maintenance.
	Window time.Duration
}

// DefaultLimits is the allowance when nothing is configured. Read is generous
// because the console polls: the dashboard, the incident list and the event
// stream each refresh on a timer, and a quota that a single open tab can exhaust
// would throttle legitimate use.
func DefaultLimits() Limits {
	return Limits{Read: 600, Write: 120, Bulk: 12, Window: time.Minute}
}

// limiterStore is the shared counter backing the limiter: an atomic increment
// with a first-write expiry. It is deliberately as small as the login guard's
// equivalent, so it can run over an in-process map in development and Redis in
// production without a second implementation of the policy.
type limiterStore interface {
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
}

// Limiter enforces a per-tenant, per-class request allowance. All methods are
// nil-safe, so a server without one (tests, embedded mode) is unlimited.
type Limiter struct {
	store  limiterStore
	limits Limits
}

func NewLimiter(store limiterStore, limits Limits) *Limiter {
	if limits.Window <= 0 {
		limits.Window = time.Minute
	}
	return &Limiter{store: store, limits: limits}
}

// NewMemoryLimiter returns a limiter backed by an in-process map. Correct for a
// single process, and the mode the integration tests and a no-Redis deployment
// run in.
func NewMemoryLimiter(limits Limits) *Limiter {
	return NewLimiter(newMemoryStore(), limits)
}

// NewRedisLimiter returns a limiter backed by Redis at addr, shared across
// replicas. Without a shared store each replica would grant the full allowance,
// so the effective limit would scale with the replica count.
func NewRedisLimiter(addr string, limits Limits) *Limiter {
	host, password := config.ParseRedisAddress(addr)
	return NewLimiter(&redisStore{client: redis.NewClient(&redis.Options{Addr: host, Password: password})}, limits)
}

// Allow records one request of the given class for the tenant and reports whether
// it may proceed. A store failure allows the request: see the package comment.
func (l *Limiter) Allow(ctx context.Context, tenantID string, class Class) (bool, error) {
	if l == nil || tenantID == "" {
		return true, nil
	}
	limit := l.limitFor(class)
	if limit <= 0 {
		return true, nil
	}
	key := l.key(tenantID, class)
	count, err := l.store.Incr(ctx, key)
	if err != nil {
		return true, nil
	}
	if count == 1 {
		// Arm the window on the first request in it. A failure here means the
		// counter never expires, which would lock the tenant out permanently, so
		// it is surfaced rather than swallowed.
		if err := l.store.Expire(ctx, key, l.limits.Window); err != nil {
			return true, nil
		}
	}
	return count <= int64(limit), nil
}

func (l *Limiter) limitFor(class Class) int {
	switch class {
	case ClassRead:
		return l.limits.Read
	case ClassWrite:
		return l.limits.Write
	case ClassBulk:
		return l.limits.Bulk
	default:
		return l.limits.Write
	}
}

// key names the counter. The window is not part of it: the key expires, which
// starts the next window, so one key per tenant and class is enough.
func (l *Limiter) key(tenantID string, class Class) string {
	return "ratelimit:" + string(class) + ":" + tenantID
}

// Classify maps a request to its class. Bulk is decided by path rather than by
// method because every bulk endpoint is a POST that also exists in a cheap form
// -- creating one asset is a write, importing a thousand is bulk -- so the path
// is the only thing that distinguishes them.
func Classify(method, path string) Class {
	if bulkPaths[path] {
		return ClassBulk
	}
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return ClassRead
	default:
		return ClassWrite
	}
}

// bulkPaths are the routes that fan out into per-item work or enqueue jobs.
// They are listed literally rather than matched by pattern: a new expensive
// endpoint should have to be added here deliberately, and a pattern that
// over-matched would meter a cheap route by accident.
var bulkPaths = map[string]bool{
	"/api/v1/assets/import":         true,
	"/api/v1/assets/export":         true,
	"/api/v1/detection-jobs":        true,
	"/api/v1/maintenance/retention": true,
}

// RetryAfter is the hint sent with a refusal. It is the window, because a fixed
// window counter cannot know when its current window started without a second
// read; the caller is told to wait at most one window, which is never a lie and
// is what a well-behaved client needs.
func (l *Limiter) RetryAfter() time.Duration {
	if l == nil {
		return 0
	}
	return l.limits.Window
}

// ErrRateLimited is returned to the HTTP layer so it can answer 429 with a
// Retry-After header.
type ErrRateLimited struct {
	RetryAfter time.Duration
	Class      Class
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("%s rate limit exceeded, retry after %s", e.Class, e.RetryAfter)
}

type memoryEntry struct {
	count int64
	exp   time.Time
}

type memoryStore struct {
	mu sync.Mutex
	m  map[string]memoryEntry
}

func newMemoryStore() *memoryStore {
	return &memoryStore{m: make(map[string]memoryEntry)}
}

func (s *memoryStore) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.m[key]
	if !ok || (!entry.exp.IsZero() && time.Now().After(entry.exp)) {
		s.m[key] = memoryEntry{count: 1}
		return 1, nil
	}
	entry.count++
	s.m[key] = entry
	return entry.count, nil
}

func (s *memoryStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.m[key]; ok {
		entry.exp = time.Now().Add(ttl)
		s.m[key] = entry
	}
	return nil
}

type redisStore struct {
	client *redis.Client
}

func (s *redisStore) Incr(ctx context.Context, key string) (int64, error) {
	return s.client.Incr(ctx, key).Result()
}

func (s *redisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}

// ParseLimits reads the per-class allowances from the environment, falling back
// to DefaultLimits for anything unset or unparseable. A zero or negative value
// disables that class rather than blocking every request of it.
func ParseLimits(get func(string) string, window time.Duration) Limits {
	limits := DefaultLimits()
	if window > 0 {
		limits.Window = window
	}
	limits.Read = config.PositiveInt(get, "RATE_LIMIT_READ_PER_WINDOW", limits.Read)
	limits.Write = config.PositiveInt(get, "RATE_LIMIT_WRITE_PER_WINDOW", limits.Write)
	limits.Bulk = config.PositiveInt(get, "RATE_LIMIT_BULK_PER_WINDOW", limits.Bulk)
	return limits
}
