package engine

import (
	"log"
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
//
// Delivery is synchronous with a deep copy per subscriber and per-subscriber
// panic recovery: the old fire-and-forget goroutine per subscriber shared the
// payload map by reference (a data race if handlers mutate it), had no
// lifecycle, and a panicking subscriber killed the process.
func (p *LocalPubSub) Publish(channel string, payload map[string]interface{}) error {
	p.mu.RLock()
	handlers := p.subscribers[channel]
	p.mu.RUnlock()

	for _, fn := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[pubsub] subscriber panic on channel %q: %v", channel, r)
				}
			}()
			fn(deepCopyPayload(payload))
		}()
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
