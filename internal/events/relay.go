package events

import "time"

// Relay carries the payloads a broker has already fanned out locally to the
// other processes of a deployment.
//
// It is an interface rather than a concrete Redis client so the outbound path
// can be tested without a server, and so an embedded deployment -- which has no
// other process to talk to -- simply leaves it nil and behaves exactly as it
// did before a relay existed.
//
// Publish is called from the broker's single sender goroutine and is allowed to
// block on the network. The broker never calls it from the path a request is
// waiting on, which is what keeps a stalled relay from slowing down detection.
type Relay interface {
	Publish(tenantID string, payload []byte)
	Close() error
}

// relayItem is one queued outbound event. The tenant travels with the payload
// because the relay publishes on a per-tenant channel.
type relayItem struct {
	tenantID string
	payload  []byte
}

const (
	// relayQueueDepth bounds the outbound backlog. It takes the same shape as
	// the per-subscriber buffer: bounded, and full means drop rather than grow.
	relayQueueDepth = 256
	// relayCloseTimeout bounds how long Close waits for the sender goroutine. A
	// relay stuck on a dead connection must not hold up graceful shutdown.
	relayCloseTimeout = 2 * time.Second
)
