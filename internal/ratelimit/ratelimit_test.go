package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifySendsBulkByPath(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   Class
	}{
		{"GET", "/api/v1/dashboard", ClassRead},
		{"GET", "/api/v1/assets", ClassRead},
		{"POST", "/api/v1/sites", ClassWrite},
		{"PATCH", "/api/v1/assets/abc", ClassWrite},
		{"DELETE", "/api/v1/alert-channels/abc", ClassWrite},
		// Bulk is decided by path, not method: every one of these is a POST that
		// also exists in a cheap form, so the method cannot distinguish them.
		{"POST", "/api/v1/assets/import", ClassBulk},
		{"GET", "/api/v1/assets/export", ClassBulk},
		{"POST", "/api/v1/detection-jobs", ClassBulk},
		{"POST", "/api/v1/maintenance/retention", ClassBulk},
	}
	for _, test := range cases {
		if got := Classify(test.method, test.path); got != test.want {
			t.Errorf("Classify(%s, %s) = %s, want %s", test.method, test.path, got, test.want)
		}
	}
}

// TestAllowIsPerTenantAndClass is the core of the policy: two tenants must not
// share a counter, and one class being exhausted must not touch another.
func TestAllowIsPerTenantAndClass(t *testing.T) {
	limiter := NewMemoryLimiter(Limits{Read: 1, Write: 2, Bulk: 1, Window: time.Minute})
	ctx := context.Background()

	if allowed, _ := limiter.Allow(ctx, "tenant-a", ClassRead); !allowed {
		t.Fatal("first read for tenant-a was refused")
	}
	if allowed, _ := limiter.Allow(ctx, "tenant-a", ClassRead); allowed {
		t.Fatal("second read for tenant-a was allowed past the limit")
	}
	if allowed, _ := limiter.Allow(ctx, "tenant-b", ClassRead); !allowed {
		t.Fatal("tenant-b was refused because tenant-a exhausted its own allowance")
	}
	// The write bucket is untouched by reads being spent.
	if allowed, _ := limiter.Allow(ctx, "tenant-a", ClassWrite); !allowed {
		t.Fatal("a write was refused because the read allowance was exhausted")
	}
}

func TestAllowWithoutTenantIsUnlimited(t *testing.T) {
	limiter := NewMemoryLimiter(Limits{Read: 1, Write: 1, Bulk: 1, Window: time.Minute})
	// An empty tenant cannot be keyed safely: every anonymous caller would share
	// one bucket, so the limiter declines to count rather than over-counting.
	for attempt := 0; attempt < 5; attempt++ {
		if allowed, _ := limiter.Allow(context.Background(), "", ClassRead); !allowed {
			t.Fatalf("attempt %d with no tenant was refused", attempt)
		}
	}
}

func TestZeroLimitIsUnlimited(t *testing.T) {
	limiter := NewMemoryLimiter(Limits{Read: 0, Write: 1, Bulk: 1, Window: time.Minute})
	// 0 means "off" for that class. A deployment that wants no read limit should
	// get none rather than a limit of zero that refuses everything.
	for attempt := 0; attempt < 5; attempt++ {
		if allowed, _ := limiter.Allow(context.Background(), "tenant", ClassRead); !allowed {
			t.Fatalf("attempt %d was refused by a zero read limit", attempt)
		}
	}
}

func TestNilLimiterAndNilStoreFailOpen(t *testing.T) {
	var nilLimiter *Limiter
	if allowed, err := nilLimiter.Allow(context.Background(), "tenant", ClassRead); !allowed || err != nil {
		t.Fatal("a nil limiter must allow")
	}
	if nilLimiter.RetryAfter() != 0 {
		t.Fatal("a nil limiter must report no retry delay")
	}

	// A store that errors is a degraded dependency, not a reason to refuse an
	// already-authorized request.
	broken := NewLimiter(errorStore{}, DefaultLimits())
	if allowed, err := broken.Allow(context.Background(), "tenant", ClassRead); !allowed || err != nil {
		t.Fatalf("a broken store allowed=%v err=%v, want it to fail open", allowed, err)
	}
}

// TestWindowExpiryReopensTheAllowance is what keeps the control from becoming a
// permanent lockout: the counter must reset when its window ends.
func TestWindowExpiryReopensTheAllowance(t *testing.T) {
	store := newMemoryStore()
	limiter := NewLimiter(store, Limits{Read: 1, Write: 1, Bulk: 1, Window: 50 * time.Millisecond})
	ctx := context.Background()

	if allowed, _ := limiter.Allow(ctx, "tenant", ClassRead); !allowed {
		t.Fatal("first read was refused")
	}
	if allowed, _ := limiter.Allow(ctx, "tenant", ClassRead); allowed {
		t.Fatal("second read was allowed inside the window")
	}
	time.Sleep(80 * time.Millisecond)
	if allowed, _ := limiter.Allow(ctx, "tenant", ClassRead); !allowed {
		t.Fatal("the allowance did not reopen after the window elapsed")
	}
}

func TestRetryAfterMatchesWindow(t *testing.T) {
	limiter := NewMemoryLimiter(Limits{Read: 1, Write: 1, Bulk: 1, Window: 30 * time.Second})
	if got := limiter.RetryAfter(); got != 30*time.Second {
		t.Fatalf("RetryAfter = %s, want the window", got)
	}
}

func TestParseLimitsFallsBackToDefaults(t *testing.T) {
	empty := ParseLimits(func(string) string { return "" }, 0)
	if empty != DefaultLimits() {
		t.Fatalf("ParseLimits with no environment = %+v, want the defaults", empty)
	}

	configured := ParseLimits(func(name string) string {
		switch name {
		case "RATE_LIMIT_READ_PER_WINDOW":
			return "30"
		case "RATE_LIMIT_BULK_PER_WINDOW":
			return "2"
		}
		return ""
	}, 2*time.Minute)
	if configured.Read != 30 || configured.Bulk != 2 {
		t.Fatalf("configured = %+v, want the supplied read and bulk values", configured)
	}
	if configured.Write != DefaultLimits().Write {
		t.Fatalf("write = %d, want the default since it was unset", configured.Write)
	}
	if configured.Window != 2*time.Minute {
		t.Fatalf("window = %s, want the supplied one", configured.Window)
	}
}

// errorStore fails every operation, standing in for an unreachable Redis.
type errorStore struct{}

func (errorStore) Incr(context.Context, string) (int64, error) {
	return 0, errUnavailable
}

func (errorStore) Expire(context.Context, string, time.Duration) error {
	return errUnavailable
}

var errUnavailable = errors.New("store unavailable")
