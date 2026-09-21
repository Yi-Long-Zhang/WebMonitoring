package notifygate

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type memoryEntry struct {
	count  int64
	exp    time.Time
	value  string
	hasVal bool
}

type memoryStore struct {
	mu sync.Mutex
	m  map[string]memoryEntry
	// now is the same injectable clock the gate uses. Expiry has to be measured
	// against the same source as the breaker's cooldown, or a test that advances
	// the clock would move one and not the other and the two would disagree
	// about whether a window had passed.
	now func() time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{m: make(map[string]memoryEntry), now: time.Now}
}

// live reports the entry at key and whether it is still within its window. An
// entry with a zero expiry never expires, which is how a stored value with no
// TTL is represented.
func (s *memoryStore) live(key string) (memoryEntry, bool) {
	entry, ok := s.m[key]
	if !ok {
		return memoryEntry{}, false
	}
	if !entry.exp.IsZero() && s.now().After(entry.exp) {
		delete(s.m, key)
		return memoryEntry{}, false
	}
	return entry, true
}

func (s *memoryStore) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.live(key)
	if !ok {
		// A lapsed window starts a fresh count. Any stored value under the same
		// key is preserved: the breaker writes a value rather than a count, and
		// dropping it here would let an open breaker read as never-opened.
		entry = s.m[key]
		entry.count = 0
		entry.exp = time.Time{}
	}
	entry.count++
	s.m[key] = entry
	return entry.count, nil
}

func (s *memoryStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.m[key]; ok {
		entry.exp = s.now().Add(ttl)
		s.m[key] = entry
	}
	return nil
}

func (s *memoryStore) Reset(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

func (s *memoryStore) SetWithTTL(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := memoryEntry{value: value, hasVal: true}
	if ttl > 0 {
		entry.exp = s.now().Add(ttl)
	}
	s.m[key] = entry
	return nil
}

func (s *memoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.live(key)
	if !ok || !entry.hasVal {
		return "", nil
	}
	return entry.value, nil
}

type redisStore struct {
	client *redis.Client
}

func (s *redisStore) Incr(ctx context.Context, key string) (int64, error) {
	return s.client.Incr(ctx, key).Result()
}

func (s *redisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}

func (s *redisStore) Reset(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

func (s *redisStore) SetWithTTL(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *redisStore) Get(ctx context.Context, key string) (string, error) {
	value, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return value, err
}
