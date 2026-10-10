package events

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"webmonitoring/internal/metrics"
)

// fakeRelay stands in for the cross-process transport. block, when non-nil,
// makes Publish hang until it is closed -- the shape of a relay whose Redis
// connection has stopped draining, which is the case the broker must survive
// without slowing down its caller.
type fakeRelay struct {
	mu        sync.Mutex
	items     []relayItem
	published int
	closes    int
	block     chan struct{}
}

func newFakeRelay() *fakeRelay { return &fakeRelay{} }

func (r *fakeRelay) Publish(tenantID string, payload []byte) {
	r.mu.Lock()
	r.published++
	r.mu.Unlock()
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.items = append(r.items, relayItem{tenantID: tenantID, payload: append([]byte(nil), payload...)})
	r.mu.Unlock()
}

func (r *fakeRelay) Close() error {
	r.mu.Lock()
	r.closes++
	r.mu.Unlock()
	return nil
}

func (r *fakeRelay) waitForItem(t *testing.T) relayItem {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.items) > 0 {
			item := r.items[0]
			r.mu.Unlock()
			return item
		}
		r.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the relay received nothing")
	return relayItem{}
}

func (r *fakeRelay) publishCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.published
}

func (r *fakeRelay) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

// counterValue reads one counter out of the registry's exposition text, so the
// assertion is on the number rather than on the series merely existing.
func counterValue(t *testing.T, registry *metrics.Registry, metricKey string) float64 {
	t.Helper()
	for _, line := range strings.Split(registry.Snapshot(), "\n") {
		name, value, found := strings.Cut(line, " ")
		if !found || name != metricKey {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return parsed
	}
	return 0
}

func TestPublishForwardsToRelay(t *testing.T) {
	broker := NewBroker()
	relay := newFakeRelay()
	broker.SetRelay(relay)
	defer broker.Close()

	broker.Publish(testTenant, "incident.created", map[string]string{"incidentId": "incident-1"})

	item := relay.waitForItem(t)
	if item.tenantID != testTenant {
		t.Fatalf("relayed tenant %q, want %q", item.tenantID, testTenant)
	}
	eventType, data := decodeEvent(t, item.payload)
	if eventType != "incident.created" {
		t.Fatalf("relayed type %q", eventType)
	}
	if data["incidentId"] != "incident-1" {
		t.Fatalf("relayed data %v", data)
	}
}

// The relay is on the request path -- a detection worker publishing a job
// result must not wait on another process's network. Overflow is dropped, which
// is the contract the per-subscriber buffer already had.
func TestPublishDoesNotBlockOnStalledRelay(t *testing.T) {
	broker := NewBroker()
	stalled := newFakeRelay()
	stalled.block = make(chan struct{})
	broker.SetRelay(stalled)
	defer func() {
		close(stalled.block)
		broker.Close()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := range relayQueueDepth * 8 {
			broker.Publish(testTenant, "detection.job.succeeded", map[string]int{"index": index})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a relay that is not draining")
	}
}

func TestRelayOverflowIsDroppedAndCounted(t *testing.T) {
	broker := NewBroker()
	stalled := newFakeRelay()
	stalled.block = make(chan struct{})
	broker.SetRelay(stalled)
	registry := metrics.New()
	broker.SetMetrics(registry)
	defer func() {
		close(stalled.block)
		broker.Close()
	}()

	// The sender takes one item and blocks; the queue absorbs relayQueueDepth
	// more; everything past that has nowhere to go and must be dropped rather
	// than wait. Even if the sender never runs, the arithmetic holds.
	for range relayQueueDepth * 4 {
		broker.Publish(testTenant, "detection.job.queued", map[string]string{"jobId": "job-1"})
	}

	key := `webmonitoring_events_relay_dropped_total{reason="queue_full"}`
	if dropped := counterValue(t, registry, key); dropped <= 0 {
		t.Fatalf("%s = %v, want a positive count", key, dropped)
	}
}

// The receive path must not feed back into the send path. This is what rules
// out a loop; the relay's origin check is about not delivering an event twice,
// not about termination.
func TestDeliverRemoteDeliversLocallyWithoutRepublishing(t *testing.T) {
	broker := NewBroker()
	relay := newFakeRelay()
	broker.SetRelay(relay)
	defer broker.Close()

	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	broker.DeliverRemote(testTenant, []byte(`{"type":"incident.updated","data":{"id":"remote-1"}}`))

	select {
	case raw := <-channel:
		if eventType, _ := decodeEvent(t, raw); eventType != "incident.updated" {
			t.Fatalf("delivered type %q", eventType)
		}
	case <-time.After(time.Second):
		t.Fatal("a remote event was not delivered to the local subscriber")
	}
	if published := relay.publishCount(); published != 0 {
		t.Fatalf("the receive path republished to the relay %d time(s)", published)
	}
}

func TestDeliverRemoteWithEmptyTenantIsDropped(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe("")
	defer unsubscribe()

	broker.DeliverRemote("", []byte(`{"type":"incident.created","data":{}}`))

	select {
	case raw := <-channel:
		t.Fatalf("an empty-tenant remote event was delivered: %q", raw)
	default:
	}
}

// Close stops the sender and the relay, and leaves subscribers alone: an SSE
// handler reading a closed channel would take the zero value and write an empty
// event, and closing a channel a publisher still selects on would panic.
func TestCloseStopsSenderAndLeavesSubscribersUsable(t *testing.T) {
	broker := NewBroker()
	relay := newFakeRelay()
	broker.SetRelay(relay)
	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	broker.Close()
	broker.Close() // idempotent: a second close must not panic or re-close the relay

	if closes := relay.closeCount(); closes != 1 {
		t.Fatalf("relay closed %d times, want 1", closes)
	}

	// Still delivering locally afterwards, and not blocking on the queue whose
	// sender has gone.
	broker.Publish(testTenant, "incident.created", map[string]string{"id": "after-close"})
	select {
	case raw := <-channel:
		if eventType, _ := decodeEvent(t, raw); eventType != "incident.created" {
			t.Fatalf("delivered type %q after Close", eventType)
		}
	case <-time.After(time.Second):
		t.Fatal("local delivery stopped after Close")
	}
}

func TestSetRelayTwiceIsIgnored(t *testing.T) {
	broker := NewBroker()
	first := newFakeRelay()
	second := newFakeRelay()
	broker.SetRelay(first)
	broker.SetRelay(second)
	defer broker.Close()

	broker.Publish(testTenant, "incident.created", map[string]string{"id": "one"})
	first.waitForItem(t)
	if published := second.publishCount(); published != 0 {
		t.Fatalf("the second relay received %d event(s); the sender was started twice", published)
	}
}

// Embedded mode: no relay, no queue, and the behaviour that existed before the
// relay seam is untouched.
func TestBrokerWithoutRelayIsProcessLocal(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	broker.Publish(testTenant, "incident.created", map[string]string{"id": "local"})
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("a broker with no relay stopped delivering locally")
	}

	broker.Close() // must be safe with no relay attached
	broker.Publish(testTenant, "incident.created", map[string]string{"id": "after-close"})
}
