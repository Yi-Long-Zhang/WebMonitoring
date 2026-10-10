package events

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRelayedBroker builds a broker wired to a real (mini) Redis over the relay,
// which is the shape both main binaries use.
func newRelayedBroker(t *testing.T, ctx context.Context, address string) *Broker {
	t.Helper()
	broker := NewBroker()
	relay, err := NewRedisRelay(ctx, address, broker.DeliverRemote, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	broker.SetRelay(relay)
	t.Cleanup(broker.Close)
	return broker
}

// deliverOnce keeps publishing until the receiving broker sees an event.
//
// A pub/sub subscription comes up asynchronously: PSubscribe puts the command on
// the wire, but the first message can still race Redis processing it. Retrying
// is how these tests stay honest about that rather than passing because they
// happened to be slow.
func deliverOnce(t *testing.T, sender *Broker, tenant string, events <-chan []byte) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sender.Publish(tenant, "incident.created", map[string]string{"id": "across"})
		select {
		case payload := <-events:
			return payload
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("the relay never delivered across brokers")
	return nil
}

// clear removes whatever is already buffered, so a later negative assertion
// cannot fail on a straggler from the warm-up.
func clear(events <-chan []byte) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func expectPayloadID(t *testing.T, events <-chan []byte, want string) {
	t.Helper()
	select {
	case payload := <-events:
		_, data := decodeEvent(t, payload)
		if data["id"] != want {
			t.Fatalf("received %v, want id %q", data, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no event with id %q arrived", want)
	}
}

func expectNothing(t *testing.T, events <-chan []byte, message string) {
	t.Helper()
	select {
	case payload := <-events:
		t.Fatalf("%s: received %q", message, payload)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRedisRelayDeliversAcrossBrokers(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := newRelayedBroker(t, ctx, server.Addr())
	receiver := newRelayedBroker(t, ctx, server.Addr())

	events, unsubscribe := receiver.Subscribe(testTenant)
	defer unsubscribe()

	eventType, data := decodeEvent(t, deliverOnce(t, sender, testTenant, events))
	if eventType != "incident.created" {
		t.Fatalf("delivered type %q", eventType)
	}
	if data["id"] != "across" {
		t.Fatalf("delivered data %v", data)
	}
}

// The scoping has to survive the wire, not just the local fan-out: a relay that
// delivered every message to every local subscriber regardless of tenant would
// reintroduce the leak one hop away from where it was fixed. Both directions are
// asserted -- an event that reaches the wrong tenant is as much a failure as one
// that reaches none.
func TestRedisRelayIsTenantScopedOverTheWire(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := newRelayedBroker(t, ctx, server.Addr())
	receiver := newRelayedBroker(t, ctx, server.Addr())

	mine, closeMine := receiver.Subscribe(testTenant)
	defer closeMine()
	theirs, closeTheirs := receiver.Subscribe(otherTenant)
	defer closeTheirs()

	// Warm both subscriptions up first: an event that never arrived because
	// nothing was subscribed yet would make the negatives below pass for the
	// wrong reason.
	deliverOnce(t, sender, testTenant, mine)
	deliverOnce(t, sender, otherTenant, theirs)
	time.Sleep(200 * time.Millisecond)
	clear(mine)
	clear(theirs)

	sender.Publish(testTenant, "incident.created", map[string]string{"id": "for-mine"})
	expectPayloadID(t, mine, "for-mine")
	expectNothing(t, theirs, "an otherTenant subscriber received a testTenant event")

	sender.Publish(otherTenant, "incident.created", map[string]string{"id": "for-theirs"})
	expectPayloadID(t, theirs, "for-theirs")
	expectNothing(t, mine, "a testTenant subscriber received an otherTenant event")
}

// The channel name and the envelope's tenant are redundant on purpose. When they
// disagree the message is dropped, which is the failure-closed direction: the
// alternative is delivering an event to whichever of the two happened to win.
//
// The subscriber is on the tenant the envelope *claims*, because that is who
// would receive it if the channel were ignored. Subscribing to the channel's
// tenant instead makes this test pass whether the check exists or not -- which
// is exactly what the first version of it did.
func TestRedisRelayDropsTenantMismatchedEnvelope(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	receiver := newRelayedBroker(t, ctx, server.Addr())
	sender := newRelayedBroker(t, ctx, server.Addr())
	events, unsubscribe := receiver.Subscribe(otherTenant)
	defer unsubscribe()

	deliverOnce(t, sender, otherTenant, events)
	time.Sleep(200 * time.Millisecond)
	clear(events)

	raw, err := json.Marshal(envelope{
		ID:      "mismatched",
		Origin:  "some-other-process",
		Tenant:  otherTenant,
		Payload: json.RawMessage(`{"type":"incident.created","data":{"id":"wrong"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close() //nolint:errcheck // 测试收尾
	// Posted on testTenant's channel while claiming to be for otherTenant.
	if err := client.Publish(ctx, eventChannel(testTenant), raw).Err(); err != nil {
		t.Fatal(err)
	}

	expectNothing(t, events, "an envelope whose tenant disagreed with its channel was delivered")
}

// An event must reach a process's own subscribers exactly once. The local fan-out
// is synchronous; the copy that comes back over Redis is the same event, and
// delivering it again is what the origin check prevents.
func TestRedisRelaySuppressesItsOwnEcho(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := newRelayedBroker(t, ctx, server.Addr())
	sender := newRelayedBroker(t, ctx, server.Addr())
	events, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	// Prove the subscription is live before claiming anything about a duplicate.
	deliverOnce(t, sender, testTenant, events)
	time.Sleep(200 * time.Millisecond)
	clear(events)

	broker.Publish(testTenant, "incident.created", map[string]string{"id": "echo"})

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("the publishing broker did not deliver locally")
	}
	select {
	case payload := <-events:
		t.Fatalf("the publishing broker delivered its own event twice: %q", payload)
	case <-time.After(500 * time.Millisecond):
	}
}

// A relay whose Redis has gone must cost nothing on the request path: the
// publish still returns, and local delivery -- the part that serves the console
// in this process -- is unaffected.
func TestRedisRelayKeepsWorkingLocallyWhenRedisIsGone(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := newRelayedBroker(t, ctx, server.Addr())
	events, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	server.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Publish(testTenant, "incident.created", map[string]string{"id": "local"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a Redis that is gone")
	}

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("local delivery stopped when Redis went away")
	}
}

// A detection worker produces events but serves no console, so it is given no
// delivery function and must still publish.
func TestRedisRelayWithoutDeliverStillPublishes(t *testing.T) {
	server := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	receiver := newRelayedBroker(t, ctx, server.Addr())
	events, unsubscribe := receiver.Subscribe(testTenant)
	defer unsubscribe()

	publisher := NewBroker()
	relay, err := NewRedisRelay(ctx, server.Addr(), nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	publisher.SetRelay(relay)
	t.Cleanup(publisher.Close)

	deliverOnce(t, publisher, testTenant, events)
}
