package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

// ErrSessionNotFound marks a session that does not exist, has expired, or is
// unreadable. It is deliberately distinct from a backend failure: callers must
// answer "not found" with 401 but a Redis outage with 5xx, otherwise one
// blip logs every user out.
var ErrSessionNotFound = errors.New("session not found")

// sessionRecord is the slim, storage-friendly representation of a session.
// It holds only identifiers and an expiry; the full Session (with User and
// Tenant) is re-hydrated from the repository on every Get so that role
// changes, disables, and deletions take effect immediately.
type sessionRecord struct {
	ID        string    `json:"id"`
	CSRFToken string    `json:"csrfToken"`
	UserID    string    `json:"userId"`
	TenantID  string    `json:"tenantId"`
	ExpiresAt time.Time `json:"expiresAt"`
	// PasswordFingerprint ties the session to the password it was issued under.
	// A record written before this field existed carries "" -- which matches no
	// live fingerprint, so those sessions are ended on their next use rather
	// than trusted. That is the intended migration for a credential change.
	PasswordFingerprint string `json:"passwordFingerprint,omitempty"`
}

// SessionStore persists session records. It is implemented over an in-process
// map (dev / tests / embedded mode) or Redis (production, shared across
// replicas). Get returns ErrSessionNotFound for an absent record and a wrapped
// backend error for a store failure — the two must not be conflated.
type SessionStore interface {
	Put(ctx context.Context, record sessionRecord) error
	Get(ctx context.Context, id string) (sessionRecord, error)
	Delete(ctx context.Context, id string) error
}

type memorySessionStore struct {
	mu sync.RWMutex
	m  map[string]sessionRecord
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{m: make(map[string]sessionRecord)}
}

func (s *memorySessionStore) Put(_ context.Context, record sessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[record.ID] = record
	return nil
}

func (s *memorySessionStore) Get(_ context.Context, id string) (sessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.m[id]
	if !ok {
		return sessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (s *memorySessionStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

type redisSessionStore struct {
	client *redis.Client
}

// NewRedisSessionStore returns a SessionStore backed by Redis at addr, shared
// across replicas. The client connects lazily.
func NewRedisSessionStore(addr string) SessionStore {
	host, password := config.ParseRedisAddress(addr)
	return &redisSessionStore{client: redis.NewClient(&redis.Options{Addr: host, Password: password})}
}

func (s *redisSessionStore) key(id string) string {
	return "session:" + id
}

func (s *redisSessionStore) Put(ctx context.Context, record sessionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	ttl := time.Until(record.ExpiresAt)
	if ttl <= 0 {
		ttl = time.Minute
	}
	return s.client.Set(ctx, s.key(record.ID), data, ttl).Err()
}

func (s *redisSessionStore) Get(ctx context.Context, id string) (sessionRecord, error) {
	data, err := s.client.Get(ctx, s.key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return sessionRecord{}, ErrSessionNotFound
	}
	if err != nil {
		return sessionRecord{}, fmt.Errorf("read session %s: %w", id, err)
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// A corrupt record is treated as missing: the user re-authenticates
		// rather than the request failing on data it cannot use.
		return sessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (s *redisSessionStore) Delete(ctx context.Context, id string) error {
	return s.client.Del(ctx, s.key(id)).Err()
}
