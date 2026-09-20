package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
)

func TestRedisAcknowledgementReclaimAndDeadLetter(t *testing.T) {
	// REDIS_TEST_ADDR may carry credentials (redis://:password@host:port), which is
	// what the queue sees in a deployment with requirepass set.
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("REDIS_TEST_ADDR is not configured")
	}
	ctx := context.Background()
	host, password := config.ParseRedisAddress(address)
	client := redis.NewClient(&redis.Options{Addr: host, Password: password})
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration test unavailable: %v", err)
	}
	defer client.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stream := "webmonitoring:test:" + suffix
	group := "test-group-" + suffix
	if err := ensureGroup(ctx, client, stream, group); err != nil {
		t.Fatal(err)
	}
	var queue *Redis
	t.Cleanup(func() {
		if queue != nil {
			_ = queue.Close()
		}
		cleanupClient := redis.NewClient(&redis.Options{Addr: host, Password: password})
		_ = cleanupClient.Del(context.Background(), stream, stream+deadLetterSuffix).Err()
		_ = cleanupClient.Close()
	})
	queue = &Redis{
		client: client, stream: stream, group: group,
		maxDeliveries: 2, minIdle: time.Millisecond,
	}

	id, err := client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream, Values: map[string]any{"payload": `{"id":"job"}`},
	}).Result()
	if err != nil {
		t.Fatal(err)
	}
	message := readTestMessage(t, ctx, client, stream, group, "first")
	if message.ID != id {
		t.Fatalf("read message %s, want %s", message.ID, id)
	}
	processErr := errors.New("persist failed")
	if err := queue.processMessage(ctx, message, func(context.Context, []byte) error {
		return processErr
	}); !errors.Is(err, processErr) {
		t.Fatalf("failed processing returned %v", err)
	}
	if pending := pendingCount(t, ctx, client, stream, group); pending != 1 {
		t.Fatalf("failed message was acknowledged; pending=%d", pending)
	}

	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newStream(ctx, address, stream, group)
	if err != nil {
		t.Fatal(err)
	}
	restarted.maxDeliveries = 2
	restarted.minIdle = time.Millisecond
	queue = restarted
	client = restarted.client

	time.Sleep(5 * time.Millisecond)
	claimed, _, err := client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: stream, Group: group, Consumer: "second",
		MinIdle: time.Millisecond, Start: "0-0", Count: 1,
	}).Result()
	if err != nil || len(claimed) != 1 {
		t.Fatalf("reclaim pending message: count=%d err=%v", len(claimed), err)
	}
	var handled bool
	queue.SetDeadLetterHandler(func(context.Context, []byte, error) error {
		handled = true
		return nil
	})
	if err := queue.processMessage(ctx, claimed[0], func(context.Context, []byte) error {
		return processErr
	}); err != nil {
		t.Fatalf("dead-letter processing failed: %v", err)
	}
	if !handled {
		t.Fatal("dead-letter handler was not called")
	}
	if pending := pendingCount(t, ctx, client, stream, group); pending != 0 {
		t.Fatalf("dead-letter message remained pending: %d", pending)
	}
	if length, err := client.XLen(ctx, stream+deadLetterSuffix).Result(); err != nil || length != 1 {
		t.Fatalf("dead-letter stream length=%d err=%v", length, err)
	}

	if _, err := client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream, Values: map[string]any{"payload": `{"id":"success"}`},
	}).Result(); err != nil {
		t.Fatal(err)
	}
	success := readTestMessage(t, ctx, client, stream, group, "third")
	if err := queue.processMessage(ctx, success, func(context.Context, []byte) error {
		return nil
	}); err != nil {
		t.Fatalf("successful processing failed: %v", err)
	}
	if pending := pendingCount(t, ctx, client, stream, group); pending != 0 {
		t.Fatalf("successful message remained pending: %d", pending)
	}
}

func readTestMessage(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	stream string,
	group string,
	consumer string,
) redis.XMessage {
	t.Helper()
	result, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: group, Consumer: consumer, Streams: []string{stream, ">"},
		Count: 1, Block: time.Second,
	}).Result()
	if err != nil {
		t.Fatal(err)
	}
	return result[0].Messages[0]
}

func pendingCount(t *testing.T, ctx context.Context, client *redis.Client, stream, group string) int64 {
	t.Helper()
	result, err := client.XPending(ctx, stream, group).Result()
	if err != nil {
		t.Fatal(err)
	}
	return result.Count
}
