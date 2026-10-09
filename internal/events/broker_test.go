package events

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	first, closeFirst := broker.Subscribe()
	defer closeFirst()
	second, closeSecond := broker.Subscribe()
	defer closeSecond()

	broker.Publish("incident.created", map[string]string{"incidentId": "incident-1"})

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

func TestUnsubscribeClosesTheChannelAndStopsDelivery(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe()

	unsubscribe()
	if _, open := <-channel; open {
		t.Fatal("unsubscribe must close the channel")
	}
	// Publishing after unsubscribe must neither panic (send on a closed channel)
	// nor resurrect the subscriber.
	broker.Publish("incident.created", map[string]string{"incidentId": "ignored"})
	if _, open := <-channel; open {
		t.Fatal("a closed channel must stay empty")
	}
	// Unsubscribing twice must not close the channel twice.
	unsubscribe()
}

func TestSlowSubscriberIsDroppedWithoutBlockingThePublisher(t *testing.T) {
	broker := NewBroker()
	channel, unsubscribe := broker.Subscribe()
	defer unsubscribe()

	const published = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := range published {
			broker.Publish("detection.job.succeeded", map[string]int{"index": index})
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
	channel, unsubscribe := broker.Subscribe()
	defer unsubscribe()

	// A channel cannot be marshalled, so the event never reaches the wire.
	broker.Publish("incident.created", make(chan int))

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
					broker.Publish("detection.job.queued", map[string]string{"jobId": "job-1"})
				}
			}
		}()
	}
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 200 {
				channel, unsubscribe := broker.Subscribe()
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
	// leak a channel and, in the SSE handler, a goroutine.
	if leaked := len(broker.subscribers); leaked != 0 {
		t.Fatalf("%d subscription(s) survived the storm", leaked)
	}
	// And the broker still works afterwards -- publishing to a closed subscriber
	// set would panic on a closed channel, which the loop above could hide.
	channel, unsubscribe := broker.Subscribe()
	defer unsubscribe()
	broker.Publish("incident.created", map[string]string{"id": "incident-1"})
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("the broker stopped delivering after the concurrent storm")
	}
}
