package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	user, err := s.repository.FindUserByUsername(tenantID, username)
	if err != nil || !security.VerifyPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	if user.Disabled {
		// A disabled account is indistinguishable from invalid credentials so
		// the login path does not reveal which usernames exist in the tenant.
		return Session{}, ErrInvalidCredentials
	}
	tenant, err := s.repository.GetTenant(tenantID)
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
	_ = s.store.Put(ctx, record)
	return session, nil
}

func (s *Service) Get(ctx context.Context, id string) (Session, bool) {
	record, ok := s.store.Get(ctx, id)
	if !ok || time.Now().UTC().After(record.ExpiresAt) {
		if ok {
			_ = s.store.Delete(ctx, id)
		}
		return Session{}, false
	}
	user, err := s.repository.GetUser(record.TenantID, record.UserID)
	if err != nil || user.Disabled {
		// Deleted or disabled users lose their session immediately: the record
		// is re-hydrated from the repository on every authenticated request.
		_ = s.store.Delete(ctx, id)
		return Session{}, false
	}
	tenant, err := s.repository.GetTenant(record.TenantID)
	if err != nil || tenant.Disabled {
		// A disabled or deleted tenant loses its live sessions on the next
		// request rather than waiting for the session TTL to expire.
		_ = s.store.Delete(ctx, id)
		return Session{}, false
	}
	return Session{
		ID:        record.ID,
		CSRFToken: record.CSRFToken,
		User:      user,
		Tenant:    tenant,
		ExpiresAt: record.ExpiresAt,
	}, true
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
