package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
		ID:                  session.ID,
		CSRFToken:           session.CSRFToken,
		UserID:              user.ID,
		TenantID:            tenantID,
		ExpiresAt:           session.ExpiresAt,
		PasswordFingerprint: passwordFingerprint(user.PasswordHash),
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
// is absent, expired, or invalidated by a disabled user/tenant or by a password
// change, and a wrapped backend error when the store itself fails — callers must
// answer the former with "not authenticated" and the latter with "try again
// later", so a store outage cannot log everyone out.
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
	if record.PasswordFingerprint != passwordFingerprint(user.PasswordHash) {
		// The password behind this session changed after it was issued. Ending
		// the session here is what makes "change your password" also mean "sign
		// the other party out": a stolen cookie is the reason someone rotates a
		// credential, and until this check existed it kept working for the rest
		// of the 12-hour TTL. HashPassword salts randomly, so any re-set --
		// including setting the same password again -- produces a new hash, and
		// therefore a new fingerprint.
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

// Reissue rewrites the record behind an existing session so it survives the
// password change that just happened in it.
//
// Without this the user who changes their own password is signed out of the very
// session they changed it in: Get compares the record's fingerprint against the
// hash it now reads, and a record written before the change is older than the
// hash. Carrying one session across the change is also the whole point of the
// design -- every *other* session of that user is expected to die, and calling
// this for anything but the session that performed the change would undermine
// exactly the revocation the fingerprint exists to provide.
//
// session.User.PasswordHash must already be the new hash.
func (s *Service) Reissue(ctx context.Context, session Session) error {
	return s.store.Put(ctx, sessionRecord{
		ID:                  session.ID,
		CSRFToken:           session.CSRFToken,
		UserID:              session.User.ID,
		TenantID:            session.User.TenantID,
		ExpiresAt:           session.ExpiresAt,
		PasswordFingerprint: passwordFingerprint(session.User.PasswordHash),
	})
}

// passwordFingerprint is what a session records to notice that the password
// behind it changed. It hashes the stored verifier rather than carrying it: the
// record is read back on every authenticated request, and comparing for change
// needs no more than a digest. The hash format already embeds a random salt, so
// this changes whenever the password is re-set.
func passwordFingerprint(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}
