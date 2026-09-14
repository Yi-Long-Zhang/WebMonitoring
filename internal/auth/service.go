package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
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
	mu         sync.RWMutex
	repository store.Repository
	sessions   map[string]Session
	ttl        time.Duration
}

func NewService(repository store.Repository, ttl time.Duration) *Service {
	return &Service{repository: repository, sessions: make(map[string]Session), ttl: ttl}
}

func (s *Service) Login(tenantID, username, password string) (Session, error) {
	user, err := s.repository.FindUserByUsername(tenantID, username)
	if err != nil || !security.VerifyPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	tenant, err := s.repository.GetTenant(tenantID)
	if err != nil {
		// Fail closed: an unresolvable tenant is treated as invalid credentials
		// so the caller cannot probe tenant existence through the login path.
		return Session{}, ErrInvalidCredentials
	}
	session := Session{
		ID:        randomToken(32),
		CSRFToken: randomToken(32),
		User:      user,
		Tenant:    tenant,
		ExpiresAt: time.Now().UTC().Add(s.ttl),
	}
	s.mu.Lock()
	s.sessions[session.ID] = session
	s.mu.Unlock()
	return session, nil
}

func (s *Service) Get(id string) (Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok || time.Now().UTC().After(session.ExpiresAt) {
		if ok {
			s.Delete(id)
		}
		return Session{}, false
	}
	return session, true
}

func (s *Service) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func randomToken(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}
