package events

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two tenants that are never equal: one shared by the existing tests, one used
// to prove that a publish reaches exactly one of them.
const (
	testTenant  = "tenant-1"
	otherTenant = "tenant-2"
)

func decodeEvent(t *testing.T, raw []byte) (string, map[string]any) {
	t.Helper()
	var envelope struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode event %q: %v", raw, err)
	}
	return envelope.Type, envelope.Data
}

func TestPublishReachesEverySubscriber(t *testing.T) {
	broker := NewBroker()
	first, closeFirst := broker.Subscribe(testTenant)
	defer closeFirst()
	second, closeSecond := broker.Subscribe(testTenant)
	defer closeSecond()

	broker.Publish(testTenant, "incident.created", map[string]string{"incidentId": "incident-1"})

	for index, channel := range []<-chan []byte{first, second} {
		select {
		case raw := <-channel:
			eventType, data := decodeEvent(t, raw)
			if eventType != "incident.created" {
				t.Fatalf("subscriber %d got type %q", index, eventType)
			}
			if data["incidentId"] != "incident-1" {
				t.Fatalf("subscriber %d got data %v", index, data)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", index)
		}
	}
}

// A publish must reach only the subscribers of the tenant it names. Before the
// broker was scoped, one tenant's incident carried tenantId, title, signals and
// evidenceIds into every other tenant's open console stream.
func TestPublishIsScopedToTenant(t *testing.T) {
	broker := NewBroker()
	mine, closeMine := broker.Subscribe(testTenant)
	defer closeMine()
	theirs, closeTheirs := broker.Subscribe(otherTenant)
	defer closeTheirs()

	broker.Publish(testTenant, "incident.created", map[string]string{"incidentId": "incident-1"})

	select {
	case raw := <-mine:
		if eventType, _ := decodeEvent(t, raw); eventType != "incident.created" {
			t.Fatalf("owner got type %q", eventType)
		}
	case <-time.After(time.Second):
		t.Fatal("the publishing tenant's subscriber received nothing")
	}
	select {
	case raw := <-theirs:
		t.Fatalf("another tenant's subscriber received %q", raw)
	default:
	}
}

// An empty tenant must deliver to nobody rather than to everybody. This is the
// failure-closed end of the scoping: a caller that loses the tenant gets a
// dropped event, never a broadcast.
func TestPublishWithEmptyTenantDeliversToNobody(t *testing.T) {
	broker := NewBroker()
	empty, closeEmpty := broker.Subscribe("")
	defer closeEmpty()
	named, closeNamed := broker.Subscribe(testTenant)
	defer closeNamed()

	broker.Publish("", "incident.created", map[string]string{"incidentId": "incident-1"})

	for name, channel := range map[string]<-chan []byte{"empty tenant": empty, "named tenant": named} {
		select {
		case raw := <-channel:
			t.Fatalf("%s subscriber received %q", name, raw)
		default:
		}
	}
}

func TestUnsubscribeClosesTheChannelAndStopsDelivery(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe(testTenant)

	unsubscribe()
	if _, open := <-channel; open {
		t.Fatal("unsubscribe must close the channel")
	}
	// Publishing after unsubscribe must neither panic (send on a closed channel)
	// nor resurrect the subscriber.
	broker.Publish(testTenant, "incident.created", map[string]string{"incidentId": "ignored"})
	if _, open := <-channel; open {
		t.Fatal("a closed channel must stay empty")
	}
	// Unsubscribing twice must not close the channel twice.
	unsubscribe()
	// Nor may it take another tenant's subscriber down with it.
	survivor, closeSurvivor := broker.Subscribe(otherTenant)
	defer closeSurvivor()
	broker.Publish(otherTenant, "incident.created", map[string]string{"incidentId": "survives"})
	select {
	case <-survivor:
	case <-time.After(time.Second):
		t.Fatal("unsubscribing one tenant removed another tenant's subscriber")
	}
}

func TestSlowSubscriberIsDroppedWithoutBlockingThePublisher(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	const published = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := range published {
			broker.Publish(testTenant, "detection.job.succeeded", map[string]int{"index": index})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a subscriber that is not reading")
	}

	// The buffer is filled and everything beyond it is dropped rather than
	// queueing up unboundedly in memory.
	received := 0
	for {
		select {
		case <-channel:
			received++
			continue
		default:
		}
		break
	}
	if received != cap(channel) {
		t.Fatalf("subscriber buffered %d events, want %d", received, cap(channel))
	}
	if received >= published {
		t.Fatal("the test did not actually drop any event")
	}
}

func TestPublishWithUnmarshalablePayloadIsIgnored(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	// A channel cannot be marshalled, so the event never reaches the wire.
	broker.Publish(testTenant, "incident.created", make(chan int))

	select {
	case raw := <-channel:
		t.Fatalf("unexpected event %q", raw)
	default:
	}
}

// Concurrent publishers and subscribers must not lose each other, deadlock, or
// leave a subscription behind.
//
// Until this was rewritten the body was four goroutines publishing flat out,
// four subscribing and unsubscribing in a loop, and `time.Sleep(50ms)` -- with
// no assertion of any kind. Its only value was as a target for -race, and -race
// was not enabled anywhere, so the test exercised nothing observable in either
// mode. The counters below are what make it a test rather than a warm-up.
func TestConcurrentPublishAndSubscribe(t *testing.T) {
	broker := NewBroker()
	var wait sync.WaitGroup
	var received atomic.Int64
	stop := make(chan struct{})

	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
					broker.Publish(testTenant, "detection.job.queued", map[string]string{"jobId": "job-1"})
				}
			}
		}()
	}
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 200 {
				channel, unsubscribe := broker.Subscribe(testTenant)
				// Blocking rather than non-blocking: publishers are running flat
				// out until stop closes, so a fresh subscriber receives a frame
				// rather than being tested for luck.
				select {
				case <-channel:
					received.Add(1)
				case <-stop:
					unsubscribe()
					return
				}
				unsubscribe()
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wait.Wait()

	if received.Load() == 0 {
		t.Fatal("no subscriber received anything while four publishers were running")
	}
	// Every unsubscribe must have taken effect: a subscription left behind would
	// leak a channel and, in the SSE handler, a goroutine. The outer map is
	// counted too, so a tenant key that outlives its last subscriber fails here.
	if leaked := len(broker.subscribers); leaked != 0 {
		t.Fatalf("%d tenant(s) survived the storm", leaked)
	}
	// And the broker still works afterwards -- publishing to a closed subscriber
	// set would panic on a closed channel, which the loop above could hide.
	channel, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()
	broker.Publish(testTenant, "incident.created", map[string]string{"id": "incident-1"})
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("the broker stopped delivering after the concurrent storm")
	}
}
