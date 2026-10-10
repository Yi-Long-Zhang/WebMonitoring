package events

import (
	"encoding/json"
	"sync"
)

// Broker fans an event out to the subscribers of one tenant.
//
// Tenancy is a parameter of Subscribe and Publish rather than a field read off
// the payload. Reading it off the payload would be fail-open: the day someone
// publishes an event type whose payload has no tenant field, the lookup yields
// the zero value and the event goes somewhere it should not. As a parameter it
// cannot be forgotten -- a caller that drops it does not compile -- and an empty
// value is refused outright (see Publish). The same shape is why every
// repository call in internal/api passes principal(request).User.TenantID
// explicitly.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan []byte]struct{}
}

func NewBroker() *Broker {
	return &Broker{subscribers: make(map[string]map[chan []byte]struct{})}
}

// Subscribe registers a subscriber for one tenant's events and returns the
// channel plus the function that removes it.
func (b *Broker) Subscribe(tenantID string) (<-chan []byte, func()) {
	channel := make(chan []byte, 16)
	b.mu.Lock()
	if b.subscribers[tenantID] == nil {
		b.subscribers[tenantID] = make(map[chan []byte]struct{})
	}
	b.subscribers[tenantID][channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		if subscribers, ok := b.subscribers[tenantID]; ok {
			if _, ok := subscribers[channel]; ok {
				delete(subscribers, channel)
				close(channel)
			}
			// Drop the tenant key itself once its last subscriber leaves.
			// Keeping the empty map would leak one entry per tenant that ever
			// connected, and the concurrency test asserts this map drains.
			if len(subscribers) == 0 {
				delete(b.subscribers, tenantID)
			}
		}
		b.mu.Unlock()
	}
}

// Publish delivers an event to the subscribers of one tenant.
//
// An empty tenant is dropped rather than treated as a wildcard: delivering to
// nobody is the failure-closed outcome, and it is the one an event can reach
// without a live subscriber list having been consulted by mistake.
func (b *Broker) Publish(tenantID, eventType string, payload any) {
	if tenantID == "" {
		return
	}
	data, err := json.Marshal(map[string]any{"type": eventType, "data": payload})
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscriber := range b.subscribers[tenantID] {
		select {
		case subscriber <- data:
		default:
		}
	}
}
