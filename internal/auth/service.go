package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

var ErrInvalidCredentials = errors.New("invalid username or password")

type Session struct {
	ID        string
	CSRFToken string
	User      domain.User
	Tenant    domain.Tenant
	ExpiresAt time.Time
}

type Service struct {
	repository store.Repository
	store      SessionStore
	ttl        time.Duration
}

func NewService(repository store.Repository, ttl time.Duration) *Service {
	return &Service{repository: repository, store: newMemorySessionStore(), ttl: ttl}
}

// SetSessionStore replaces the in-process session store, e.g. with a shared
// Redis store in production. Sessions are stored as slim records and re-hydrated
// from the repository on Get, so role changes and user deletion/disable take
// effect on the next authenticated request.
func (s *Service) SetSessionStore(store SessionStore) {
	s.store = store
}

func (s *Service) Login(ctx context.Context, tenantID, username, password string) (Session, error) {
	user, err := s.repository.FindUserByUsername(ctx, tenantID, username)
	if err != nil || !security.VerifyPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	if user.Disabled {
		// A disabled account is indistinguishable from invalid credentials so
		// the login path does not reveal which usernames exist in the tenant.
		return Session{}, ErrInvalidCredentials
	}
	tenant, err := s.repository.GetTenant(ctx, tenantID)
	if err != nil || tenant.Disabled {
		// Fail closed: an unresolvable or disabled tenant is treated as invalid
		// credentials so the caller cannot probe tenant existence or state
		// through the login path.
		return Session{}, ErrInvalidCredentials
	}
	session := Session{
		ID:        randomToken(32),
		CSRFToken: randomToken(32),
		User:      user,
		Tenant:    tenant,
		ExpiresAt: time.Now().UTC().Add(s.ttl),
	}
	record := sessionRecord{
		ID:        session.ID,
		CSRFToken: session.CSRFToken,
		UserID:    user.ID,
		TenantID:  tenantID,
		ExpiresAt: session.ExpiresAt,
	}
	if err := s.store.Put(ctx, record); err != nil {
		// Fail closed: a session the store cannot persist must not be reported
		// as a successful login — the client would hold a cookie that the next
		// request rejects, and the outage would look like a mystery logout.
		return Session{}, fmt.Errorf("persist login session: %w", err)
	}
	return session, nil
}

// Get resolves a session by id. It returns ErrSessionNotFound when the session
// is absent, expired, or invalidated by a disabled user/tenant, and a wrapped
// backend error when the store itself fails — callers must answer the former
// with "not authenticated" and the latter with "try again later", so a store
// outage cannot log everyone out.
func (s *Service) Get(ctx context.Context, id string) (Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	if time.Now().UTC().After(record.ExpiresAt) {
		_ = s.store.Delete(ctx, id)
		return Session{}, ErrSessionNotFound
	}
	user, err := s.repository.GetUser(ctx, record.TenantID, record.UserID)
	if err != nil || user.Disabled {
		// Deleted or disabled users lose their session immediately: the record
		// is re-hydrated from the repository on every authenticated request.
		_ = s.store.Delete(ctx, id)
		return Session{}, ErrSessionNotFound
	}
	tenant, err := s.repository.GetTenant(ctx, record.TenantID)
	if err != nil || tenant.Disabled {
		// A disabled or deleted tenant loses its live sessions on the next
		// request rather than waiting for the session TTL to expire.
		_ = s.store.Delete(ctx, id)
		return Session{}, ErrSessionNotFound
	}
	return Session{
		ID:        record.ID,
		CSRFToken: record.CSRFToken,
		User:      user,
		Tenant:    tenant,
		ExpiresAt: record.ExpiresAt,
	}, nil
}

func (s *Service) Delete(ctx context.Context, id string) {
	_ = s.store.Delete(ctx, id)
}

func randomToken(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}
