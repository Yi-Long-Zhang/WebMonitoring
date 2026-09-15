package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoginGuardLocksAccountAfterFailures(t *testing.T) {
	guard := NewMemoryLoginGuard(5, 15*time.Minute)
	ctx := context.Background()

	if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4"); err != nil {
		t.Fatalf("unexpected block before failures: %v", err)
	}
	for i := 0; i < 5; i++ {
		guard.RecordFailure(ctx, "tenant-1", "alice")
	}
	err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4")
	if err == nil {
		t.Fatal("expected account lock after max failures")
	}
	var locked *ErrAccountLocked
	if !errors.As(err, &locked) {
		t.Fatalf("expected ErrAccountLocked, got %T: %v", err, err)
	}
}

func TestLoginGuardSuccessResetsFailures(t *testing.T) {
	guard := NewMemoryLoginGuard(5, 15*time.Minute)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		guard.RecordFailure(ctx, "tenant-1", "alice")
	}
	guard.RecordSuccess(ctx, "tenant-1", "alice")
	if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4"); err != nil {
		t.Fatalf("success should reset the failure counter: %v", err)
	}
}

func TestLoginGuardLockoutExpires(t *testing.T) {
	guard := NewMemoryLoginGuard(2, 50*time.Millisecond)
	ctx := context.Background()

	guard.RecordFailure(ctx, "tenant-1", "alice")
	guard.RecordFailure(ctx, "tenant-1", "alice")
	if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4"); err == nil {
		t.Fatal("expected lock")
	}
	time.Sleep(70 * time.Millisecond)
	if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4"); err != nil {
		t.Fatalf("lockout should expire after the window: %v", err)
	}
}

func TestLoginGuardScopesAccountsAndTenants(t *testing.T) {
	guard := NewMemoryLoginGuard(2, 15*time.Minute)
	ctx := context.Background()

	guard.RecordFailure(ctx, "tenant-1", "alice")
	guard.RecordFailure(ctx, "tenant-1", "alice")

	// Same username, different tenant, and different username, same tenant
	// must both remain unlocked.
	if err := guard.BeforeLogin(ctx, "tenant-2", "alice", "1.2.3.4"); err != nil {
		t.Fatalf("tenant scope leaked lockout: %v", err)
	}
	if err := guard.BeforeLogin(ctx, "tenant-1", "bob", "1.2.3.4"); err != nil {
		t.Fatalf("username scope leaked lockout: %v", err)
	}
}

func TestLoginGuardPerIPThrottle(t *testing.T) {
	guard := NewMemoryLoginGuard(100, time.Minute)
	ctx := context.Background()

	var rateErr error
	for i := 0; i < 40; i++ {
		if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "9.9.9.9"); err != nil {
			rateErr = err
			break
		}
	}
	if rateErr == nil {
		t.Fatal("expected per-IP rate limit")
	}
	var limited *ErrRateLimited
	if !errors.As(rateErr, &limited) {
		t.Fatalf("expected ErrRateLimited, got %T: %v", rateErr, rateErr)
	}
}

func TestLoginGuardNilReceiverIsNoop(t *testing.T) {
	var guard *LoginGuard
	ctx := context.Background()
	if err := guard.BeforeLogin(ctx, "tenant-1", "alice", "1.2.3.4"); err != nil {
		t.Fatalf("nil guard BeforeLogin should be a no-op: %v", err)
	}
	guard.RecordFailure(ctx, "tenant-1", "alice")
	guard.RecordSuccess(ctx, "tenant-1", "alice")
}
