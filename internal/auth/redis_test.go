package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newTestRedis starts an in-process Redis so the Redis-backed stores are
// exercised without a service dependency in CI.
func newTestRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	return server
}

func TestRedisSessionStoreRoundTrip(t *testing.T) {
	server := newTestRedis(t)
	store := NewRedisSessionStore(server.Addr())
	ctx := context.Background()
	record := sessionRecord{
		ID: "session-1", CSRFToken: "csrf-1", UserID: "user-1", TenantID: "tenant-1",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != record {
		t.Fatalf("stored record = %+v, want %+v", stored, record)
	}
	// The key must expire on its own so an abandoned session cannot be replayed.
	if ttl := server.TTL("session:" + record.ID); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("session TTL = %s, want a positive value within the session lifetime", ttl)
	}
	if err := store.Delete(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, record.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestRedisSessionStoreIsSharedBetweenReplicas(t *testing.T) {
	server := newTestRedis(t)
	ctx := context.Background()
	record := sessionRecord{
		ID: "session-shared", UserID: "user-1", TenantID: "tenant-1",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	// Two independent clients stand in for two control-plane replicas.
	if err := NewRedisSessionStore(server.Addr()).Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRedisSessionStore(server.Addr()).Get(ctx, record.ID); err != nil {
		t.Fatalf("a session created by one replica is not visible to another: %v", err)
	}
}

func TestRedisSessionStoreFailsClosed(t *testing.T) {
	server := newTestRedis(t)
	store := NewRedisSessionStore(server.Addr())
	ctx := context.Background()

	// A malformed record must not be handed to the caller.
	server.Set("session:corrupt", "not-json")
	if _, err := store.Get(ctx, "corrupt"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a corrupt session record must read as missing, got %v", err)
	}
	// An unreachable Redis is a backend failure, not "not logged in": the
	// caller must be able to answer it with 5xx rather than 401.
	server.Close()
	if _, err := store.Get(ctx, "session-1"); err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("an unreachable session store must surface a backend error, got %v", err)
	}
	if err := store.Put(ctx, sessionRecord{ID: "session-1"}); err == nil {
		t.Fatal("failing to persist a session must be reported")
	}
}

func TestRedisSessionStoreShortExpiryStillPersists(t *testing.T) {
	server := newTestRedis(t)
	store := NewRedisSessionStore(server.Addr())
	// An already-expired record would make Redis reject a non-positive TTL, so
	// the store substitutes a short one.
	record := sessionRecord{ID: "session-stale", ExpiresAt: time.Now().UTC().Add(-time.Hour)}
	if err := store.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL("session:session-stale"); ttl <= 0 {
		t.Fatalf("TTL = %s, want a positive fallback", ttl)
	}
}

func TestRedisLoginGuardLocksAnAccountAndUnlocksAfterTheWindow(t *testing.T) {
	server := newTestRedis(t)
	guard := NewRedisLoginGuard(server.Addr(), 3, time.Minute)
	ctx := context.Background()
	const tenantID = "tenant-1"
	const username = "operator"

	for attempt := 1; attempt <= 2; attempt++ {
		guard.RecordFailure(ctx, tenantID, username)
		if err := guard.BeforeLogin(ctx, tenantID, username, "203.0.113.7"); err != nil {
			t.Fatalf("attempt %d was throttled too early: %v", attempt, err)
		}
	}
	guard.RecordFailure(ctx, tenantID, username)

	var locked *ErrAccountLocked
	if err := guard.BeforeLogin(ctx, tenantID, username, "203.0.113.7"); !errors.As(err, &locked) {
		t.Fatalf("third failure did not lock the account: %v", err)
	}
	// The counter must expire, otherwise a locked account stays locked forever.
	if ttl := server.TTL("login:fail:" + tenantID + ":" + username); ttl <= 0 {
		t.Fatalf("lockout TTL = %s, want a positive value", ttl)
	}
	server.FastForward(2 * time.Minute)
	if err := guard.BeforeLogin(ctx, tenantID, username, "203.0.113.7"); err != nil {
		t.Fatalf("lockout did not expire with the window: %v", err)
	}
}

func TestRedisLoginGuardSuccessClearsFailures(t *testing.T) {
	server := newTestRedis(t)
	guard := NewRedisLoginGuard(server.Addr(), 3, time.Minute)
	ctx := context.Background()

	guard.RecordFailure(ctx, "tenant-1", "operator")
	guard.RecordFailure(ctx, "tenant-1", "operator")
	guard.RecordSuccess(ctx, "tenant-1", "operator")
	guard.RecordFailure(ctx, "tenant-1", "operator")

	if count, err := guard.store.Get(ctx, "login:fail:tenant-1:operator"); err != nil || count != 1 {
		t.Fatalf("failure count after a success = %d (err %v), want 1", count, err)
	}
	if err := guard.BeforeLogin(ctx, "tenant-1", "operator", ""); err != nil {
		t.Fatalf("account locked after the counter was cleared: %v", err)
	}
}

func TestRedisLoginGuardSharesStateBetweenReplicas(t *testing.T) {
	server := newTestRedis(t)
	first := NewRedisLoginGuard(server.Addr(), 2, time.Minute)
	second := NewRedisLoginGuard(server.Addr(), 2, time.Minute)
	ctx := context.Background()

	first.RecordFailure(ctx, "tenant-1", "operator")
	second.RecordFailure(ctx, "tenant-1", "operator")

	var locked *ErrAccountLocked
	if err := first.BeforeLogin(ctx, "tenant-1", "operator", ""); !errors.As(err, &locked) {
		t.Fatalf("failures recorded on one replica did not lock the account on the other: %v", err)
	}
}

func TestRedisLoginGuardThrottlesASourceIP(t *testing.T) {
	server := newTestRedis(t)
	guard := NewRedisLoginGuard(server.Addr(), 1000, time.Minute)
	ctx := context.Background()
	const remoteIP = "203.0.113.9"

	for attempt := 1; attempt <= guard.maxIP; attempt++ {
		// Distinct accounts, so only the per-IP throttle can trigger.
		username := "user-" + strconv.Itoa(attempt)
		if err := guard.BeforeLogin(ctx, "tenant-1", username, remoteIP); err != nil {
			t.Fatalf("attempt %d was throttled before the limit: %v", attempt, err)
		}
	}
	var limited *ErrRateLimited
	if err := guard.BeforeLogin(ctx, "tenant-1", "one-more", remoteIP); !errors.As(err, &limited) {
		t.Fatalf("the per-IP throttle did not engage: %v", err)
	}
	if ttl := server.TTL("login:ip:" + remoteIP); ttl <= 0 {
		t.Fatalf("per-IP window TTL = %s, want a positive value", ttl)
	}
	// A different source is unaffected.
	if err := guard.BeforeLogin(ctx, "tenant-1", "one-more", "198.51.100.4"); err != nil {
		t.Fatalf("an unrelated source was throttled: %v", err)
	}
}

func TestRedisLoginGuardFailsOpenWhenRedisIsUnavailable(t *testing.T) {
	server := newTestRedis(t)
	guard := NewRedisLoginGuard(server.Addr(), 2, time.Minute)
	ctx := context.Background()
	server.Close()

	// The password gate stays authoritative: an unavailable throttle must not
	// lock legitimate users out.
	if err := guard.BeforeLogin(ctx, "tenant-1", "operator", "203.0.113.7"); err != nil {
		t.Fatalf("throttle did not fail open: %v", err)
	}
	guard.RecordFailure(ctx, "tenant-1", "operator")
	guard.RecordSuccess(ctx, "tenant-1", "operator")
}
