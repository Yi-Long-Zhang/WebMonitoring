package auth

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

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
}

// SessionStore persists session records. It is implemented over an in-process
// map (dev / tests / embedded mode) or Redis (production, shared across
// replicas).
type SessionStore interface {
	Put(ctx context.Context, record sessionRecord) error
	Get(ctx context.Context, id string) (sessionRecord, bool)
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

func (s *memorySessionStore) Get(_ context.Context, id string) (sessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.m[id]
	return record, ok
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

func (s *redisSessionStore) Get(ctx context.Context, id string) (sessionRecord, bool) {
	data, err := s.client.Get(ctx, s.key(id)).Bytes()
	if err != nil {
		return sessionRecord{}, false
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return sessionRecord{}, false
	}
	return record, true
}

func (s *redisSessionStore) Delete(ctx context.Context, id string) error {
	return s.client.Del(ctx, s.key(id)).Err()
}
