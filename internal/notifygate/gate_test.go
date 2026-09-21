package notifygate

import (
	"context"
	"strings"
	"testing"
	"time"
)

// clock is a manually advanced time source, so the breaker's cooldown can be
// exercised without sleeping through it.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newTestGate(policy Policy) (*Gate, *clock) {
	c := &clock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	gate := NewMemoryGate(policy)
	gate.setClock(c.now)
	return gate, c
}

func TestAllowIsNilSafe(t *testing.T) {
	var gate *Gate
	if decision := gate.Allow(context.Background(), "channel"); !decision.Allowed {
		t.Fatal("a nil gate must allow")
	}
	// A nil gate must also survive being told about an outcome.
	gate.RecordOutcome(context.Background(), "channel", false)
	if gate.Open(context.Background(), "channel") {
		t.Fatal("a nil gate must report no open breaker")
	}
	// An empty channel id cannot be keyed safely: every anonymous caller would
	// share one bucket, so the gate declines to count rather than over-counting.
	if decision := NewMemoryGate(DefaultPolicy()).Allow(context.Background(), ""); !decision.Allowed {
		t.Fatal("a gate with no channel id must allow")
	}
}

// TestLimiterCapsOneChannel is the first control: a detection run that finds a
// thousand changed pages must not hand all thousand to one webhook.
func TestLimiterCapsOneChannel(t *testing.T) {
	gate, _ := newTestGate(Policy{MaxPerWindow: 2, Window: time.Minute, FailureThreshold: 0})
	ctx := context.Background()

	for attempt := 1; attempt <= 2; attempt++ {
		if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
			t.Fatalf("attempt %d was refused below the allowance", attempt)
		}
	}
	refused := gate.Allow(ctx, "channel")
	if refused.Allowed {
		t.Fatal("the allowance was exceeded without a refusal")
	}
	if refused.Reason != "LIMITED" {
		t.Fatalf("reason = %q, want LIMITED", refused.Reason)
	}
	if refused.RetryAfter != time.Minute {
		t.Fatalf("RetryAfter = %s, want the window", refused.RetryAfter)
	}
	// A second channel has its own allowance; one busy destination must not
	// silence another.
	if decision := gate.Allow(ctx, "other"); !decision.Allowed {
		t.Fatal("a second channel was refused because the first was busy")
	}
}

func TestLimiterReopensAfterTheWindow(t *testing.T) {
	gate, clock := newTestGate(Policy{MaxPerWindow: 1, Window: time.Minute, FailureThreshold: 0})
	ctx := context.Background()

	if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
		t.Fatal("first notification was refused")
	}
	if decision := gate.Allow(ctx, "channel"); decision.Allowed {
		t.Fatal("second notification was allowed inside the window")
	}
	clock.advance(61 * time.Second)
	if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
		t.Fatal("the allowance did not reopen after the window elapsed")
	}
}

// TestBreakerOpensAfterConsecutiveFailures: the second control. The threshold
// counts a channel that has stopped working, so it must take that many failures
// and no fewer.
func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	gate, _ := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 3, Cooldown: 5 * time.Minute})
	ctx := context.Background()

	for attempt := 1; attempt <= 2; attempt++ {
		gate.RecordOutcome(ctx, "channel", false)
		if gate.Open(ctx, "channel") {
			t.Fatalf("the breaker opened after %d failures, before the threshold", attempt)
		}
	}
	gate.RecordOutcome(ctx, "channel", false)
	if !gate.Open(ctx, "channel") {
		t.Fatal("the breaker did not open at the threshold")
	}
	refused := gate.Allow(ctx, "channel")
	if refused.Allowed {
		t.Fatal("an open breaker allowed a notification")
	}
	if refused.Reason != "CIRCUIT_OPEN" {
		t.Fatalf("reason = %q, want CIRCUIT_OPEN", refused.Reason)
	}
}

// TestSuccessClosesTheBreaker: a channel that comes back must be trusted again.
// Without this the breaker would be a one-way door and a transient outage would
// silence a channel permanently.
func TestSuccessClosesTheBreaker(t *testing.T) {
	gate, _ := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 2, Cooldown: time.Hour})
	ctx := context.Background()

	gate.RecordOutcome(ctx, "channel", false)
	gate.RecordOutcome(ctx, "channel", false)
	if !gate.Open(ctx, "channel") {
		t.Fatal("the breaker did not open")
	}
	gate.RecordOutcome(ctx, "channel", true)
	if gate.Open(ctx, "channel") {
		t.Fatal("a success did not close the breaker")
	}
	if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
		t.Fatal("a recovered channel was still refused")
	}
}

// TestSuccessResetsTheFailureCount is what keeps "consecutive" true: a channel
// that fails, succeeds, then fails again has not failed twice in a row and must
// not accumulate toward the threshold.
func TestSuccessResetsTheFailureCount(t *testing.T) {
	gate, _ := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 3, Cooldown: time.Hour})
	ctx := context.Background()

	for round := 0; round < 5; round++ {
		gate.RecordOutcome(ctx, "channel", false)
		gate.RecordOutcome(ctx, "channel", false)
		gate.RecordOutcome(ctx, "channel", true)
	}
	if gate.Open(ctx, "channel") {
		t.Fatal("interleaved successes let the failure count accumulate")
	}
}

// TestCooldownAdmitsExactlyOneProbe is the subtle half of the breaker. After the
// cooldown one attempt must be allowed through to find out whether the channel
// recovered -- but only one, or a channel that is still dead would have the
// whole retry ladder spent against it again on every subsequent incident.
func TestCooldownAdmitsExactlyOneProbe(t *testing.T) {
	gate, clock := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 1, Cooldown: 5 * time.Minute})
	ctx := context.Background()

	gate.RecordOutcome(ctx, "channel", false)
	if !gate.Open(ctx, "channel") {
		t.Fatal("the breaker did not open")
	}
	clock.advance(4 * time.Minute)
	if decision := gate.Allow(ctx, "channel"); decision.Allowed {
		t.Fatal("a notification was allowed before the cooldown elapsed")
	}
	clock.advance(2 * time.Minute)
	if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
		t.Fatal("the probe after the cooldown was refused")
	}
	// The probe failed, so the breaker re-opened and the cooldown restarts from
	// the new failure rather than from the original one.
	gate.RecordOutcome(ctx, "channel", false)
	if decision := gate.Allow(ctx, "channel"); decision.Allowed {
		t.Fatal("a second probe was allowed after the first one failed")
	}
}

// TestFailureCountOutlivesTheCooldown: the counter is re-armed on every failure
// so it cannot expire between two failures a while apart. A channel failing once
// an hour must still reach the threshold rather than counting as healthy every
// time its counter lapsed.
func TestFailureCountOutlivesTheCooldown(t *testing.T) {
	gate, clock := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 2, Cooldown: time.Minute})
	ctx := context.Background()

	gate.RecordOutcome(ctx, "channel", false)
	// Well past what a naive one-window TTL would have kept the count for.
	clock.advance(90 * time.Second)
	gate.RecordOutcome(ctx, "channel", false)
	if !gate.Open(ctx, "channel") {
		t.Fatal("the failure count lapsed between two failures")
	}
}

func TestDisabledControlsAreNoOps(t *testing.T) {
	ctx := context.Background()
	// A zero threshold disables the breaker; a zero allowance disables the
	// limiter. Both must leave the channel usable no matter what happens to it.
	gate, clock := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 0})
	for attempt := 0; attempt < 20; attempt++ {
		gate.RecordOutcome(ctx, "channel", false)
	}
	clock.advance(time.Hour)
	for attempt := 0; attempt < 20; attempt++ {
		if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
			t.Fatalf("attempt %d was refused with both controls disabled", attempt)
		}
	}
}

// TestUnreadableStateFailsOpen: the gate's whole job is to let an alert through
// when something is wrong, so a store it cannot read must not become a reason to
// stop telling operators about tampering.
func TestUnreadableStateFailsOpen(t *testing.T) {
	ctx := context.Background()
	gate := NewGate(brokenStore{}, Policy{MaxPerWindow: 5, FailureThreshold: 3, Cooldown: time.Minute})
	for attempt := 0; attempt < 10; attempt++ {
		if decision := gate.Allow(ctx, "channel"); !decision.Allowed {
			t.Fatalf("attempt %d was refused with an unreachable store", attempt)
		}
	}
	// Recording must not panic either.
	gate.RecordOutcome(ctx, "channel", false)
}

func TestParsePolicyFallsBackToDefaults(t *testing.T) {
	empty := ParsePolicy(func(string) string { return "" }, 0)
	if empty != DefaultPolicy() {
		t.Fatalf("ParsePolicy with no environment = %+v, want the defaults", empty)
	}
	configured := ParsePolicy(func(name string) string {
		switch name {
		case "ALERT_MAX_PER_CHANNEL_PER_WINDOW":
			return "10"
		case "ALERT_FAILURE_THRESHOLD":
			return "2"
		case "ALERT_CIRCUIT_COOLDOWN_SECONDS":
			return "120"
		}
		return ""
	}, 30*time.Second)
	if configured.MaxPerWindow != 10 || configured.FailureThreshold != 2 {
		t.Fatalf("configured = %+v, want the supplied counts", configured)
	}
	if configured.Cooldown != 2*time.Minute {
		t.Fatalf("cooldown = %s, want the supplied one", configured.Cooldown)
	}
	if configured.Window != 30*time.Second {
		t.Fatalf("window = %s, want the supplied one", configured.Window)
	}
}

// brokenStore fails every operation, standing in for an unreachable Redis.
type brokenStore struct{}

func (brokenStore) Incr(context.Context, string) (int64, error) { return 0, errUnavailable }
func (brokenStore) Expire(context.Context, string, time.Duration) error {
	return errUnavailable
}
func (brokenStore) Reset(context.Context, string) error { return errUnavailable }
func (brokenStore) SetWithTTL(context.Context, string, string, time.Duration) error {
	return errUnavailable
}
func (brokenStore) Get(context.Context, string) (string, error) { return "", errUnavailable }

// TestFailureCountSurvivesASlowCadence is the case the breaker exists for and
// the one a naive window gets wrong: a channel failing once an hour has stopped
// working just as much as one failing once a minute, and a counter that lapsed
// between its failures would restart every time and never reach the threshold.
func TestFailureCountSurvivesASlowCadence(t *testing.T) {
	gate, clock := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 2, Cooldown: time.Minute})
	ctx := context.Background()

	gate.RecordOutcome(ctx, "channel", false)
	clock.advance(6 * time.Hour)
	gate.RecordOutcome(ctx, "channel", false)
	if !gate.Open(ctx, "channel") {
		t.Fatal("two failures six hours apart did not open the breaker")
	}
}

func TestRefusalErrorsNameTheReason(t *testing.T) {
	gate, _ := newTestGate(Policy{MaxPerWindow: 0, FailureThreshold: 1, Cooldown: 5 * time.Minute})
	ctx := context.Background()
	gate.RecordOutcome(ctx, "channel", false)

	decision := gate.Allow(ctx, "channel")
	if decision.Allowed {
		t.Fatal("an open breaker allowed a notification")
	}
	open := &ErrCircuitOpen{ChannelID: "channel", RetryAfter: decision.RetryAfter}
	if !strings.Contains(open.Error(), "circuit open") {
		t.Fatalf("unexpected circuit-open message: %s", open.Error())
	}
	limited := &ErrLimited{ChannelID: "channel", RetryAfter: time.Minute}
	if !strings.Contains(limited.Error(), "limit reached") {
		t.Fatalf("unexpected limit message: %s", limited.Error())
	}
}

func TestNormalizeChannelIDIsStable(t *testing.T) {
	// Two spellings of one id must not become two buckets.
	if NormalizeChannelID(" CH-1 ") != NormalizeChannelID("ch-1") {
		t.Fatal("normalisation does not collapse case and surrounding space")
	}
}
