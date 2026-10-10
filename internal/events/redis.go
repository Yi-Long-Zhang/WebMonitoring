package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/config"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/metrics"
)

// eventChannelPrefix namespaces the per-tenant event channels. It follows the
// same "one deployment, one Redis" assumption as the job streams in
// internal/jobqueue, which hard-code webmonitoring:detection-jobs.
const eventChannelPrefix = "webmonitoring:events:"

const (
	relayStartupTimeout = 5 * time.Second
	relayPublishTimeout = 3 * time.Second
	relayMinBackoff     = 500 * time.Millisecond
	relayMaxBackoff     = 30 * time.Second
)

func eventChannel(tenantID string) string {
	return eventChannelPrefix + tenantID
}

// envelope is one event on the wire.
//
// ID exists so a single event can be followed across processes in a log; it
// deliberately takes no part in deduplication, which would need a set of seen
// ids and grow without bound. Origin is what suppresses a process's own echo,
// and it costs one string comparison.
type envelope struct {
	ID      string          `json:"id"`
	Origin  string          `json:"origin"`
	Tenant  string          `json:"tenant"`
	Payload json.RawMessage `json:"payload"`
}

// RedisRelay carries events between the processes of a deployment over Redis
// pub/sub, one channel per tenant.
//
// Per-tenant channels, rather than one channel filtered in-process: a single
// channel would pull every tenant's payloads into every replica's memory and
// reduce the isolation to one `if` being written correctly. Here the routing key
// is structural, and a channel that disagrees with the envelope it carries is
// dropped rather than guessed at.
//
// Delivery is best effort, and that is the intended contract. The consumer is a
// console that treats an event as "something changed, fetch again" -- it listens
// for the event name and never reads the payload (apps/console/src/services/
// events/stream.ts) -- so a lost event costs one missed refresh, not stale state.
type RedisRelay struct {
	ctx     context.Context
	client  *redis.Client
	origin  string
	deliver func(tenantID string, payload []byte)
	logger  *slog.Logger

	mu      sync.RWMutex
	metrics *metrics.Registry
}

// NewRedisRelay connects a relay to Redis.
//
// A nil deliver makes the relay publish-only, which is what a detection worker
// wants: it produces events but serves no console, so subscribing there would
// fan every tenant's events into a process that has no subscribers for them.
//
// A Redis that cannot be reached at start-up is an error rather than a silent
// downgrade, matching how the job queue behaves in the same situation
// (apps/api/main.go). Callers that would rather start anyway can ignore this
// error and run without a relay: tenant isolation lives in the broker and does
// not depend on the relay existing.
func NewRedisRelay(ctx context.Context, address string, deliver func(tenantID string, payload []byte), logger *slog.Logger) (*RedisRelay, error) {
	addr, password := config.ParseRedisAddress(address)
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password})

	pingCtx, cancel := context.WithTimeout(ctx, relayStartupTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close() //nolint:errcheck // 连接已经不可用，关闭失败无可补救
		return nil, fmt.Errorf("connect redis for the event relay: %w", err)
	}

	relay := &RedisRelay{
		ctx:     ctx,
		client:  client,
		origin:  identity.New(),
		deliver: deliver,
		logger:  logger,
	}
	if deliver != nil {
		go relay.receive()
	}
	return relay, nil
}

// SetMetrics attaches the registry the failure counters report through. Nil is
// accepted and simply turns them off.
func (r *RedisRelay) SetMetrics(registry *metrics.Registry) {
	r.mu.Lock()
	r.metrics = registry
	r.mu.Unlock()
}

// Publish sends one already-serialised event to its tenant's channel.
//
// This runs on the broker's sender goroutine, never on the path a request waits
// on, so blocking for the publish timeout costs only outbound backlog.
func (r *RedisRelay) Publish(tenantID string, payload []byte) {
	if tenantID == "" {
		return
	}
	message, err := json.Marshal(envelope{
		ID:      identity.New(),
		Origin:  r.origin,
		Tenant:  tenantID,
		Payload: payload,
	})
	if err != nil {
		r.countFailure("encode")
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, relayPublishTimeout)
	defer cancel()
	if err := r.client.Publish(ctx, eventChannel(tenantID), message).Err(); err != nil {
		r.countFailure("redis_error")
		r.log("publish event failed", "error", err)
	}
}

// Close releases the Redis client. The subscription ends with the context the
// relay was built with.
func (r *RedisRelay) Close() error {
	return r.client.Close()
}

// receive holds a subscription open, rebuilding it with backoff whenever it
// fails. The rebuild also covers the case go-redis cannot see: it re-subscribes
// on a connection it knows is broken, and this loop replaces one it does not.
func (r *RedisRelay) receive() {
	backoff := relayMinBackoff
	for {
		if r.ctx.Err() != nil {
			return
		}
		err := r.consume()
		if r.ctx.Err() != nil {
			return
		}
		r.countFailure("redis_error")
		r.log("event relay subscription stopped; resubscribing", "error", err, "backoff", backoff.String())

		timer := time.NewTimer(backoff)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < relayMaxBackoff {
			backoff *= 2
			if backoff > relayMaxBackoff {
				backoff = relayMaxBackoff
			}
		}
	}
}

// consume holds one pattern subscription open until it fails or the context
// ends.
//
// Channel() rather than the Receive APIs: it carries go-redis's own connection
// health check and automatic resubscription, and the two cannot be mixed --
// "Receive* APIs can not be used after channel is created".
func (r *RedisRelay) consume() error {
	pubsub := r.client.PSubscribe(r.ctx, eventChannelPrefix+"*")
	defer pubsub.Close() //nolint:errcheck // 释放本次订阅；失败不影响重订阅

	messages := pubsub.Channel()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case message, open := <-messages:
			if !open {
				return errors.New("event subscription closed")
			}
			r.handle(message)
		}
	}
}

// handle validates one delivered message before it reaches any subscriber.
//
// The channel name and the envelope's tenant have to agree. They are redundant
// on purpose: if a future edit computes one of them wrongly the result is a
// dropped event, not an event delivered to the wrong tenant.
func (r *RedisRelay) handle(message *redis.Message) {
	var decoded envelope
	if err := json.Unmarshal([]byte(message.Payload), &decoded); err != nil {
		r.countFailure("decode")
		return
	}
	if decoded.Tenant == "" {
		r.countFailure("empty_tenant")
		return
	}
	if channelTenant := strings.TrimPrefix(message.Channel, eventChannelPrefix); channelTenant != decoded.Tenant {
		r.countFailure("tenant_mismatch")
		return
	}
	if decoded.Origin == r.origin {
		// Our own echo. Publish already fanned this out locally, so delivering
		// it again would show the console the same event twice. This is
		// duplicate suppression, not loop prevention -- what rules out a loop
		// is that Broker.DeliverRemote never publishes back.
		return
	}
	r.deliver(decoded.Tenant, decoded.Payload)
}

// countFailure reports a dropped message. The label is a fixed set of reasons:
// a tenant label here would leak tenant names through the metrics endpoint and
// make the number of series grow with the number of tenants.
func (r *RedisRelay) countFailure(reason string) {
	r.mu.RLock()
	registry := r.metrics
	r.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.Inc("webmonitoring_events_relay_failures_total", map[string]string{"reason": reason})
}

func (r *RedisRelay) log(message string, args ...any) {
	if r.logger == nil {
		return
	}
	r.logger.Warn(message, args...)
}
