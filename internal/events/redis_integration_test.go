package events

import (
	"context"
	"os"
	"testing"
	"time"
)

// requireServices mirrors the gate the other integration suites use: once the
// runner has declared services mandatory, a missing address is a failure rather
// than a skip, so a runner that failed to pass the address down cannot look
// green.
func requireServices() bool {
	return os.Getenv("WEBMONITORING_TEST_REQUIRE_SERVICES") != ""
}

func redisTestAddress(t *testing.T) string {
	t.Helper()
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		if requireServices() {
			t.Fatal("REDIS_TEST_ADDR is not configured although this run declared the services mandatory")
		}
		t.Skip("REDIS_TEST_ADDR is not configured")
	}
	return address
}

// TestRedisRelayOnRealRedis runs the same invariants as the miniredis tests
// against a real server.
//
// miniredis implements enough of pub/sub to prove the wiring; this is what
// exercises go-redis's own protocol handling, connection health check and
// resubscription -- the parts an in-process fake cannot speak for.
func TestRedisRelayOnRealRedis(t *testing.T) {
	address := redisTestAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := newRelayedBroker(t, ctx, address)
	receiver := newRelayedBroker(t, ctx, address)

	mine, closeMine := receiver.Subscribe(testTenant)
	defer closeMine()
	theirs, closeTheirs := receiver.Subscribe(otherTenant)
	defer closeTheirs()

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

// One event, one delivery. The round trip on a real server is a network hop, so
// a process's own echo arrives later than it does over miniredis -- which is
// when a missing origin check would show up as the console refreshing twice for
// one change.
func TestRedisRelayDeliversOnceOnRealRedis(t *testing.T) {
	address := redisTestAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := newRelayedBroker(t, ctx, address)
	sender := newRelayedBroker(t, ctx, address)
	events, unsubscribe := broker.Subscribe(testTenant)
	defer unsubscribe()

	deliverOnce(t, sender, testTenant, events)
	time.Sleep(200 * time.Millisecond)
	clear(events)

	broker.Publish(testTenant, "incident.created", map[string]string{"id": "once"})

	expectPayloadID(t, events, "once")
	expectNothing(t, events, "the publishing broker delivered its own event twice")
}
