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
