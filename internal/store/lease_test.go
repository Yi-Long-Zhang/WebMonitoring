package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/migrate"
)

// newMigratedPostgresRepository brings the schema up to date and returns a
// repository on its own pool, which is what a separate replica would hold.
func newMigratedPostgresRepository(t *testing.T, dsn string) *PostgresRepository {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		t.Fatalf("PostgreSQL integration test unavailable: %v", err)
	}
	t.Cleanup(runner.Close)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey)
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	t.Cleanup(repository.Close)
	return repository
}

func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not configured")
	}
	return dsn
}

// Two replicas must not sweep retention at the same time. The advisory lock is
// session-scoped, so the loser has to report acquired=false rather than block:
// the winner is already doing the same work over the same evidence.
func TestPostgresReplicaLeaseIsExclusive(t *testing.T) {
	dsn := postgresTestDSN(t)
	ctx := context.Background()
	first := newMigratedPostgresRepository(t, dsn)
	second := newMigratedPostgresRepository(t, dsn)

	release, acquired, err := first.TryAcquire(ctx, "maintenance.retention")
	if err != nil || !acquired {
		t.Fatalf("first replica: acquired=%v err=%v", acquired, err)
	}
	// Registering the release as well as calling it keeps a failure from
	// stranding the connection, which would hang pool shutdown instead of
	// reporting the assertion.
	t.Cleanup(release)

	if _, acquired, err := second.TryAcquire(ctx, "maintenance.retention"); err != nil || acquired {
		t.Fatalf("second replica took a held lease: acquired=%v err=%v", acquired, err)
	}
	// A different lease name is a different lock and must not contend.
	if other, acquired, err := second.TryAcquire(ctx, "some.other.task"); err != nil || !acquired {
		t.Fatalf("unrelated lease was blocked: acquired=%v err=%v", acquired, err)
	} else {
		other()
	}

	release()
	released, acquired, err := second.TryAcquire(ctx, "maintenance.retention")
	if err != nil || !acquired {
		t.Fatalf("lease was not released: acquired=%v err=%v", acquired, err)
	}
	released()
}

// The lock lives on the session that took it, so a connection returned to the
// pool early would silently drop the exclusion.
func TestPostgresReplicaLeaseSurvivesUntilRelease(t *testing.T) {
	dsn := postgresTestDSN(t)
	ctx := context.Background()
	first := newMigratedPostgresRepository(t, dsn)
	second := newMigratedPostgresRepository(t, dsn)

	release, acquired, err := first.TryAcquire(ctx, "maintenance.retention")
	if err != nil || !acquired {
		t.Fatalf("first replica: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(release)
	// Exercise the pool the holder took its connection from: if the lease were
	// released when the connection went back, this would hand it out.
	for range 4 {
		if _, _, err := first.ListTenants(ListOptions{Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, acquired, err := second.TryAcquire(ctx, "maintenance.retention"); err != nil || acquired {
		t.Fatalf("lease leaked while held: acquired=%v err=%v", acquired, err)
	}
	release()
}
