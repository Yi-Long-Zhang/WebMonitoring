package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func authTestUser(t *testing.T, repo store.Repository, username string, role domain.Role) domain.User {
	t.Helper()
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{
		ID: "user-" + username, TenantID: domain.DefaultTenantID, Username: username,
		PasswordHash: hash, Role: role, CreatedAt: time.Now().UTC(),
	}
	if err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

func TestLoginGetDeleteRoundtrip(t *testing.T) {
	repo := store.NewMemoryRepository()
	user := authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	ctx := context.Background()

	session, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if session.User.ID != user.ID || session.User.Role != domain.RoleAdmin {
		t.Fatalf("session user mismatch: %+v", session.User)
	}

	got, err := service.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CSRFToken != session.CSRFToken || got.User.ID != user.ID {
		t.Fatalf("rehydrated session mismatch: %+v", got)
	}

	service.Delete(ctx, session.ID)
	if _, err := service.Get(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	repo := store.NewMemoryRepository()
	authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	if _, err := service.Login(context.Background(), domain.DefaultTenantID, "admin", "wrong"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

// A session the store cannot persist must fail the login, not return a cookie
// that the very next request will reject.
func TestLoginFailsWhenStorePutFails(t *testing.T) {
	repo := store.NewMemoryRepository()
	authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	service.SetSessionStore(failingSessionStore{putErr: errors.New("redis is down")})

	_, err := service.Login(context.Background(), domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err == nil {
		t.Fatal("expected login to fail when the session cannot be persisted")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("a store failure must not be reported as invalid credentials")
	}
}

// Get must distinguish "no such session" (the caller answers 401) from a
// backend failure (the caller answers 5xx), or one Redis blip logs everyone
// out.
func TestGetDistinguishesMissingFromBackendFailure(t *testing.T) {
	repo := store.NewMemoryRepository()
	authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	ctx := context.Background()

	session, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, "no-such-session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session: expected ErrSessionNotFound, got %v", err)
	}
	service.SetSessionStore(failingSessionStore{getErr: fmt.Errorf("read: %w", errors.New("connection refused"))})
	if _, err := service.Get(ctx, session.ID); err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("backend failure must surface as its own error, got %v", err)
	}
}

type failingSessionStore struct {
	putErr error
	getErr error
}

func (s failingSessionStore) Put(_ context.Context, _ sessionRecord) error { return s.putErr }
func (s failingSessionStore) Get(_ context.Context, _ string) (sessionRecord, error) {
	return sessionRecord{}, s.getErr
}
func (s failingSessionStore) Delete(_ context.Context, _ string) error { return nil }

func TestGetExpiredSession(t *testing.T) {
	repo := store.NewMemoryRepository()
	authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, 10*time.Millisecond)
	ctx := context.Background()
	session, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := service.Get(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected expired session to be rejected with ErrSessionNotFound, got %v", err)
	}
}

func TestGetUnknownSession(t *testing.T) {
	repo := store.NewMemoryRepository()
	service := NewService(repo, time.Hour)
	if _, err := service.Get(context.Background(), "does-not-exist"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound for unknown session, got %v", err)
	}
}

// A session issued before the password changed must stop resolving: a stolen
// cookie is the reason someone rotates a credential, and before the fingerprint
// existed it kept working for the rest of the 12-hour TTL.
func TestGetRejectsASessionIssuedBeforeAPasswordChange(t *testing.T) {
	repo := store.NewMemoryRepository()
	user := authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	ctx := context.Background()

	session, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Someone rotates the password -- an administrator reset, or the owner from
	// another device. Either way this session was not part of it.
	rotated, err := security.HashPassword("a-rotated-long-password-1")
	if err != nil {
		t.Fatal(err)
	}
	user.PasswordHash = rotated
	if err := repo.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Get(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a session predating the password change still resolved: %v", err)
	}
}

// The other half: exactly one session survives a password change, and it is the
// one that performed it. Signing the changer out of the browser they changed it
// in would teach people to avoid the action that revokes a stolen cookie.
func TestReissueCarriesOneSessionAcrossItsOwnPasswordChange(t *testing.T) {
	repo := store.NewMemoryRepository()
	user := authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	ctx := context.Background()

	changing, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	elsewhere, err := service.Login(ctx, domain.DefaultTenantID, "admin", "a-long-password-for-testing")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	rotated, err := security.HashPassword("a-rotated-long-password-1")
	if err != nil {
		t.Fatal(err)
	}
	user.PasswordHash = rotated
	if err := repo.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	changing.User = user
	if err := service.Reissue(ctx, changing); err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if _, err := service.Get(ctx, changing.ID); err != nil {
		t.Fatalf("the session that changed the password did not survive: %v", err)
	}
	if _, err := service.Get(ctx, elsewhere.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a second session survived the password change: %v", err)
	}
}

// The documented upgrade behaviour: a session record written before
// PasswordFingerprint existed reads back as "" and matches no live fingerprint,
// so it ends on its next use instead of being trusted. Deploying the change
// therefore logs everyone out once -- operations.md says so, and this is what
// makes that claim true rather than hopeful.
func TestSessionWrittenBeforeTheFingerprintExistsIsRejected(t *testing.T) {
	repo := store.NewMemoryRepository()
	user := authTestUser(t, repo, "admin", domain.RoleAdmin)
	service := NewService(repo, time.Hour)
	ctx := context.Background()

	// Exactly the shape a pre-upgrade deployment still has in Redis: identifiers
	// and an expiry, no fingerprint.
	legacy := sessionRecord{
		ID: "legacy-session", CSRFToken: "csrf", UserID: user.ID,
		TenantID: domain.DefaultTenantID, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := service.store.Put(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Get(ctx, legacy.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a session predating the fingerprint survived: %v", err)
	}
}
