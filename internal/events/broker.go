package events

import (
	"encoding/json"
	"sync"
)

type Broker struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
}

func NewBroker() *Broker {
	return &Broker{subscribers: make(map[chan []byte]struct{})}
}

func (b *Broker) Subscribe() (<-chan []byte, func()) {
	channel := make(chan []byte, 16)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		if _, ok := b.subscribers[channel]; ok {
			delete(b.subscribers, channel)
			close(channel)
		}
		b.mu.Unlock()
	}
}

func (b *Broker) Publish(eventType string, payload any) {
	data, err := json.Marshal(map[string]any{"type": eventType, "data": payload})
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscriber := range b.subscribers {
		select {
		case subscriber <- data:
		default:
		}
	}
}
