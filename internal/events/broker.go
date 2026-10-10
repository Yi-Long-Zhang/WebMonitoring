package events

import (
	"encoding/json"
	"sync"
	"time"

	"webmonitoring/internal/metrics"
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
//
// A broker with no relay is process-local: it delivers to the subscribers in
// this process and to nothing else. That is the whole of embedded mode, and it
// is byte-for-byte the behaviour the broker had before a relay existed.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan []byte]struct{}

	// Set once by SetRelay before the broker serves traffic, then only read.
	// They live under mu so Publish can read the queue in the same critical
	// section that walks the subscriber map.
	relay      Relay
	queue      chan relayItem
	done       chan struct{}
	senderDone chan struct{}
	closed     bool

	// metrics is set by SetMetrics, which main calls once the server exists --
	// so unlike the relay fields it can be written while the broker is already
	// serving, and it is always read under the lock.
	metrics *metrics.Registry
}

func NewBroker() *Broker {
	return &Broker{subscribers: make(map[string]map[chan []byte]struct{})}
}

// SetRelay attaches a cross-process transport and starts the goroutine that
// drains the outbound queue into it.
//
// Call it once at start-up. A second call, or a nil relay, is ignored: starting
// the sender twice would publish every event to the relay twice.
func (b *Broker) SetRelay(relay Relay) {
	if relay == nil {
		return
	}
	b.mu.Lock()
	if b.relay != nil {
		b.mu.Unlock()
		return
	}
	b.relay = relay
	b.queue = make(chan relayItem, relayQueueDepth)
	b.done = make(chan struct{})
	b.senderDone = make(chan struct{})
	queue, done, senderDone := b.queue, b.done, b.senderDone
	b.mu.Unlock()

	go func() {
		defer close(senderDone)
		for {
			select {
			case <-done:
				return
			case item := <-queue:
				relay.Publish(item.tenantID, item.payload)
			}
		}
	}()
}

// SetMetrics attaches the registry the relay counters report through. Nil is
// accepted and simply turns them off.
func (b *Broker) SetMetrics(registry *metrics.Registry) {
	b.mu.Lock()
	b.metrics = registry
	b.mu.Unlock()
}

// Close stops the sender goroutine and closes the relay. Events still queued at
// that moment are dropped: they are refresh hints, and a hint that arrives after
// shutdown has begun is worth less than a shutdown that finishes.
//
// It deliberately does not close any subscriber channel. An SSE handler reading
// a closed channel would take the zero value and write an empty event, and
// closing a channel that a publisher is still selecting on would panic.
// Subscribers end through their request context instead.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	relay, done, senderDone := b.relay, b.done, b.senderDone
	b.mu.Unlock()

	if relay == nil {
		return
	}
	close(done)
	select {
	case <-senderDone:
	case <-time.After(relayCloseTimeout):
	}
	_ = relay.Close() //nolint:errcheck // 关闭失败已无可补救，且调用方在停机路径上
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

// Publish delivers an event to the subscribers of one tenant, and to the other
// processes of the deployment when a relay is attached.
//
// An empty tenant is dropped rather than treated as a wildcard: delivering to
// nobody is the failure-closed outcome, and it is the one an event can reach
// without a live subscriber list having been consulted by mistake.
//
// Local fan-out stays synchronous and non-blocking, and the hand-off to the
// relay is a non-blocking enqueue. A stalled or absent relay may drop an event,
// but it must never slow the caller that published it.
func (b *Broker) Publish(tenantID, eventType string, payload any) {
	if tenantID == "" {
		return
	}
	data, err := json.Marshal(map[string]any{"type": eventType, "data": payload})
	if err != nil {
		return
	}
	b.mu.RLock()
	b.fanOutLocked(tenantID, data)
	forward := b.queue
	b.mu.RUnlock()

	if forward == nil {
		return
	}
	select {
	case forward <- relayItem{tenantID: tenantID, payload: data}:
	default:
		b.countRelayDrop("queue_full")
	}
}

// DeliverRemote delivers an event that arrived from another process.
//
// It fans out locally and stops there. The receive path must never call back
// into the send path -- that, rather than any origin bookkeeping, is what rules
// out a loop. Suppressing a process's own echo is the relay's job, and it is
// about duplicate delivery rather than about loops.
func (b *Broker) DeliverRemote(tenantID string, payload []byte) {
	if tenantID == "" {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.fanOutLocked(tenantID, payload)
}

// fanOutLocked delivers to one tenant's subscribers. The caller holds mu.
func (b *Broker) fanOutLocked(tenantID string, data []byte) {
	for subscriber := range b.subscribers[tenantID] {
		select {
		case subscriber <- data:
		default:
		}
	}
}

// countRelayDrop reports a dropped outbound event. The label is a fixed set of
// reasons: a tenant label here would leak tenant names through the metrics
// endpoint and make the number of series grow with the number of tenants.
func (b *Broker) countRelayDrop(reason string) {
	b.mu.RLock()
	registry := b.metrics
	b.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.Inc("webmonitoring_events_relay_dropped_total", map[string]string{"reason": reason})
}
