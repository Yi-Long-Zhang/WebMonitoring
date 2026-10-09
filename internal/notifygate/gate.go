// Package notifygate bounds and, when necessary, suspends outbound alert
// delivery per channel.
//
// The retry loop in internal/notify already handles a channel that fails once:
// it backs off, retries, and gives up. What it cannot see is a channel that has
// been failing for an hour -- a webhook destination that was retired, a DingTalk
// robot whose secret was rotated, an SMTP relay that now rejects the sender.
// Every incident raised during that hour tries the full retry ladder against it,
// and the cost is not only wasted work: the alert worker is a shared resource,
// so one dead channel slows delivery to the channels that still work.
//
// Two separate controls, because they answer different questions:
//
//   - The limiter answers "is this channel being used at all reasonably". A
//     detection run that finds a thousand changed pages in one pass produces a
//     thousand incidents, and a channel that receives all of them is a channel
//     nobody reads.
//   - The breaker answers "is this channel working". It counts consecutive
//     failures and, past a threshold, stops trying for a cooldown -- at which
//     point exactly one probe is let through to find out whether the channel has
//     recovered.
//
// Both fail open. A gate that cannot read its state must not stop an alert: the
// whole point of the notification path is that it fires when something is wrong,
// and a Redis outage is not a reason to stop telling operators about tampering.
package notifygate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

// Policy is the per-channel allowance and breaker configuration.
type Policy struct {
	// MaxPerWindow bounds how many notifications one channel will send inside
	// Window. Zero or negative disables the limiter, leaving the breaker as the
	// only control.
	MaxPerWindow int
	Window       time.Duration
	// FailureThreshold is how many consecutive failures close the breaker.
	// Zero or negative disables the breaker.
	FailureThreshold int
	// Cooldown is how long the breaker stays open before a probe is allowed.
	Cooldown time.Duration
}

// DefaultPolicy is the allowance when nothing is configured. The limits are
// deliberately loose: this is a runaway guard, not a budget. A channel that
// receives more than sixty alerts a minute is either misconfigured or the
// installation is under an attack large enough that the console, not a webhook,
// is where it will be watched.
func DefaultPolicy() Policy {
	return Policy{
		MaxPerWindow:     60,
		Window:           time.Minute,
		FailureThreshold: 5,
		Cooldown:         5 * time.Minute,
	}
}

// Decision is the outcome of consulting the gate.
type Decision struct {
	// Allowed is false when the notification must not be attempted at all.
	Allowed bool
	// Reason names why, for the log line and the delivery record: "LIMITED" or
	// "CIRCUIT_OPEN". Empty when Allowed.
	Reason string
	// RetryAfter is how long the caller should wait before the channel is worth
	// trying again. Zero when Allowed.
	RetryAfter time.Duration
}

// gateStore is the shared state backing both controls. It is deliberately one
// interface rather than two: a limiter and a breaker that disagree about whether
// Redis is reachable would be worse than either alone, and the memory and Redis
// implementations stay small because the policy lives in Gate.
type gateStore interface {
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Reset(ctx context.Context, key string) error
	// SetWithTTL and Get write and read a value that carries its own lifetime,
	// which is what the breaker needs: it stores the moment the breaker opened,
	// and the cooldown is measured from that stored time rather than from a key
	// that has already expired.
	SetWithTTL(ctx context.Context, key, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, error)
}

// Gate decides whether a channel may be notified, and records the outcome.
// All methods are nil-safe, so a dispatcher without a gate is unlimited.
type Gate struct {
	store  gateStore
	policy Policy
	// now is injectable so the breaker's cooldown can be tested without sleeping
	// for it. Production leaves it at time.Now.
	now func() time.Time
}

// NewGate builds a gate from an existing store.
func NewGate(store gateStore, policy Policy) *Gate {
	if policy.Window <= 0 {
		policy.Window = time.Minute
	}
	if policy.Cooldown <= 0 {
		policy.Cooldown = 5 * time.Minute
	}
	return &Gate{store: store, policy: policy, now: time.Now}
}

// NewMemoryGate returns a gate backed by an in-process map. Correct for a single
// process: the embedded deployment mode, and the tests.
func NewMemoryGate(policy Policy) *Gate {
	return NewGate(newMemoryStore(), policy)
}

// setClock moves the gate and, when the store is in-process, the store's expiry
// with it. Redis expiry is the server's own and cannot be moved, which is
// correct -- a Redis-backed gate has no injectable clock to begin with.
func (g *Gate) setClock(now func() time.Time) {
	g.now = now
	if store, ok := g.store.(*memoryStore); ok {
		store.now = now
	}
}

// NewRedisGate returns a gate backed by Redis at addr, shared across replicas.
// Without a shared store each replica would grant the full allowance and keep
// its own failure count, so neither control would mean what it says.
func NewRedisGate(addr string, policy Policy) *Gate {
	host, password := config.ParseRedisAddress(addr)
	return NewGate(&redisStore{client: redis.NewClient(&redis.Options{Addr: host, Password: password})}, policy)
}

// Allow reports whether channelID may be notified, and consumes one unit of its
// allowance when it may. A store failure allows the notification: see the
// package comment.
func (g *Gate) Allow(ctx context.Context, channelID string) Decision {
	if g == nil || channelID == "" {
		return Decision{Allowed: true}
	}
	if decision, ok := g.breakerDecision(ctx, channelID); !ok {
		return decision
	}
	if g.policy.MaxPerWindow <= 0 {
		return Decision{Allowed: true}
	}
	key := g.limitKey(channelID)
	count, err := g.store.Incr(ctx, key)
	if err != nil {
		return Decision{Allowed: true}
	}
	if count == 1 {
		if err := g.store.Expire(ctx, key, g.policy.Window); err != nil {
			return Decision{Allowed: true}
		}
	}
	if count > int64(g.policy.MaxPerWindow) {
		return Decision{Reason: "LIMITED", RetryAfter: g.policy.Window}
	}
	return Decision{Allowed: true}
}

// breakerDecision reports the breaker's verdict. The bool is false when the
// notification must not proceed; it is true to mean "the breaker has no opinion",
// including when it is disabled or its state is unreadable.
//
// The cooldown is stored rather than expressed as a TTL because the key has to
// outlive the decision: a probe that arrives after the cooldown must be able to
// read when the breaker opened, and a key that had already expired would leave
// it unable to tell "just opened" from "never opened".
func (g *Gate) breakerDecision(ctx context.Context, channelID string) (Decision, bool) {
	if g.policy.FailureThreshold <= 0 {
		return Decision{}, true
	}
	openedAt, err := g.store.Get(ctx, g.breakerKey(channelID))
	if err != nil || openedAt == "" {
		return Decision{}, true
	}
	opened, err := time.Parse(time.RFC3339Nano, openedAt)
	if err != nil {
		// Unparseable state is unreadable state: allow rather than block on it.
		return Decision{}, true
	}
	elapsed := g.now().Sub(opened)
	if elapsed < g.policy.Cooldown {
		return Decision{Reason: "CIRCUIT_OPEN", RetryAfter: g.policy.Cooldown - elapsed}, false
	}
	// The cooldown has elapsed. Let this one through as a probe; RecordOutcome
	// decides from its result whether to close the breaker or re-open it.
	return Decision{}, true
}

// RecordOutcome feeds a delivery result back to the breaker. A success closes
// it; a failure past the threshold opens it. It is nil-safe and swallows store
// errors -- losing a failure count is not worth failing a delivery over.
//
// It reports the state it left behind and whether that state changed, because
// this is the only place that knows: Open answers "is this channel being refused
// right now", which is deliberately not the same question -- a breaker whose
// cooldown has lapsed is still recorded but is letting a probe through, so
// reading Open before and after a successful probe would show no change at all
// and the recovery would go unreported.
func (g *Gate) RecordOutcome(ctx context.Context, channelID string, delivered bool) (open bool, changed bool) {
	if g == nil || channelID == "" || g.policy.FailureThreshold <= 0 {
		return false, false
	}
	if delivered {
		wasOpen := g.breakerRecorded(ctx, channelID)
		_ = g.store.Reset(ctx, g.failureKey(channelID)) //nolint:errcheck // 尽力而为的簿记/解锁：失败不影响本次结果
		_ = g.store.Reset(ctx, g.breakerKey(channelID)) //nolint:errcheck // 尽力而为的簿记/解锁：失败不影响本次结果
		return false, wasOpen
	}
	count, err := g.store.Incr(ctx, g.failureKey(channelID))
	if err != nil {
		return false, false
	}
	// The counter is re-armed on every failure so it cannot lapse between two of
	// them. The lifetime is not the cooldown: a channel failing once an hour is
	// exactly the case the breaker exists for, and a window shorter than the gap
	// between its failures would restart the count every time and never reach
	// the threshold. This is a window sized to outlive a slow failure cadence,
	// not to bound anything.
	_ = g.store.Expire(ctx, g.failureKey(channelID), failureCountLifetime) //nolint:errcheck // 尽力而为的簿记/解锁：失败不影响本次结果
	if count >= int64(g.policy.FailureThreshold) {
		wasOpen := g.breakerRecorded(ctx, channelID)
		_ = g.store.SetWithTTL(ctx, g.breakerKey(channelID), //nolint:errcheck // 尽力而为的簿记/解锁：失败不影响本次结果
			g.now().UTC().Format(time.RFC3339Nano), breakerOpenLifetime)
		return true, !wasOpen
	}
	return false, false
}

// breakerRecorded reports whether the breaker has an open record, regardless of
// whether its cooldown has lapsed.
func (g *Gate) breakerRecorded(ctx context.Context, channelID string) bool {
	openedAt, err := g.store.Get(ctx, g.breakerKey(channelID))
	return err == nil && openedAt != ""
}

// failureCountLifetime is how long a consecutive-failure count survives without
// a new failure. It is deliberately much longer than any cooldown: the counter
// is measuring "this channel has stopped working", and a channel that fails
// every ninety minutes has stopped working just as much as one failing every
// ninety seconds. A day is long enough that a genuinely recovered channel is
// cleared by its own next success rather than by the clock.
const failureCountLifetime = 24 * time.Hour

// breakerOpenLifetime is how long the record of "the breaker opened at T"
// survives. It must outlast the cooldown so a probe arriving after it can read
// when the breaker opened; the cooldown itself is what actually bounds the
// refusal, so this is the outer envelope.
const breakerOpenLifetime = 24 * time.Hour

// Open reports whether a channel's breaker is currently open, for the console.
// It is read-only and does not consume allowance.
func (g *Gate) Open(ctx context.Context, channelID string) bool {
	if g == nil || channelID == "" || g.policy.FailureThreshold <= 0 {
		return false
	}
	decision, ok := g.breakerDecision(ctx, channelID)
	return !ok && decision.Reason == "CIRCUIT_OPEN"
}

func (g *Gate) limitKey(channelID string) string {
	return "notifygate:count:" + channelID
}

func (g *Gate) failureKey(channelID string) string {
	return "notifygate:fail:" + channelID
}

func (g *Gate) breakerKey(channelID string) string {
	return "notifygate:open:" + channelID
}

// ParsePolicy reads the gate configuration from the environment, falling back to
// DefaultPolicy for anything unset or unparseable. A zero or negative value
// disables that control rather than blocking every notification.
func ParsePolicy(get func(string) string, window time.Duration) Policy {
	policy := DefaultPolicy()
	if window > 0 {
		policy.Window = window
	}
	policy.MaxPerWindow = config.PositiveInt(get, "ALERT_MAX_PER_CHANNEL_PER_WINDOW", policy.MaxPerWindow)
	policy.FailureThreshold = config.PositiveInt(get, "ALERT_FAILURE_THRESHOLD", policy.FailureThreshold)
	policy.Cooldown = time.Duration(config.PositiveInt(get, "ALERT_CIRCUIT_COOLDOWN_SECONDS", int(policy.Cooldown.Seconds()))) * time.Second
	return policy
}

// ErrCircuitOpen and ErrLimited are returned to the dispatcher so it can record
// why a delivery did not happen without inventing a transport error for it.
type ErrCircuitOpen struct {
	RetryAfter time.Duration
	ChannelID  string
}

func (e *ErrCircuitOpen) Error() string {
	return fmt.Sprintf("channel %s circuit open, retry after %s", e.ChannelID, e.RetryAfter)
}

type ErrLimited struct {
	RetryAfter time.Duration
	ChannelID  string
}

func (e *ErrLimited) Error() string {
	return fmt.Sprintf("channel %s notification limit reached, retry after %s", e.ChannelID, e.RetryAfter)
}

// NormalizeChannelID trims and lowercases a channel id for use as a key part.
// Ids are generated by the platform and already canonical; this exists so a
// caller cannot make two keys for one channel by passing it differently.
func NormalizeChannelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
