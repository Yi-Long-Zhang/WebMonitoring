package auth

import (
	"context"
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
	if err := repo.CreateUser(user); err != nil {
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

	got, ok := service.Get(ctx, session.ID)
	if !ok {
		t.Fatal("expected session to be retrievable")
	}
	if got.CSRFToken != session.CSRFToken || got.User.ID != user.ID {
		t.Fatalf("rehydrated session mismatch: %+v", got)
	}

	service.Delete(ctx, session.ID)
	if _, ok := service.Get(ctx, session.ID); ok {
		t.Fatal("expected session gone after delete")
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
	if _, ok := service.Get(ctx, session.ID); ok {
		t.Fatal("expected expired session to be rejected")
	}
}

func TestGetUnknownSession(t *testing.T) {
	repo := store.NewMemoryRepository()
	service := NewService(repo, time.Hour)
	if _, ok := service.Get(context.Background(), "does-not-exist"); ok {
		t.Fatal("expected unknown session to be rejected")
	}
}
