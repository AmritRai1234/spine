package spine

import (
	"sync"
)

// PubSub defines the event broadcast backplane interface.
type PubSub interface {
	Publish(channel string, payload map[string]interface{}) error
	Subscribe(channel string, handler func(payload map[string]interface{})) error
}

// LocalPubSub provides an in-memory pubsub implementation for single-process deployments.
type LocalPubSub struct {
	mu          sync.RWMutex
	subscribers map[string][]func(payload map[string]interface{})
}

// NewLocalPubSub initializes an in-memory PubSub adapter.
func NewLocalPubSub() *LocalPubSub {
	return &LocalPubSub{
		subscribers: make(map[string][]func(payload map[string]interface{})),
	}
}

// Publish broadcasts a message payload to all local channel subscribers.
func (p *LocalPubSub) Publish(channel string, payload map[string]interface{}) error {
	p.mu.RLock()
	handlers := p.subscribers[channel]
	p.mu.RUnlock()

	for _, fn := range handlers {
		go fn(payload)
	}
	return nil
}

// Subscribe registers a message handler callback for a channel.
func (p *LocalPubSub) Subscribe(channel string, handler func(payload map[string]interface{})) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscribers[channel] = append(p.subscribers[channel], handler)
	return nil
}
