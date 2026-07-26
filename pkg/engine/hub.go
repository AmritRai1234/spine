package engine

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type StateBroadcast struct {
	Type      string                 `json:"type"`
	State     string                 `json:"state,omitempty"`
	Event     string                 `json:"event,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp int64                  `json:"timestamp"`
}

type WsClient struct {
	Conn      *websocket.Conn
	Send      chan []byte
	closeOnce sync.Once
}

// broadcastMsg is an internal type for the async broadcast channel.
type broadcastMsg struct {
	data []byte
}

type Hub struct {
	mu          sync.RWMutex
	clients     map[*WsClient]bool
	Register    chan *WsClient
	Unregister  chan *WsClient
	broadcastCh chan broadcastMsg // async broadcast channel decouples Emit from WS fan-out
}

func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*WsClient]bool),
		Register:    make(chan *WsClient),
		Unregister:  make(chan *WsClient),
		broadcastCh: make(chan broadcastMsg, 4096), // buffered to decouple from Emit path
	}
}

func (h *Hub) Run() {
	// Start the async broadcast drainer
	go h.broadcastLoop()

	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = true
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("[ws] client connected (total: %d)", count)
		case client := <-h.Unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.closeOnce.Do(func() { close(client.Send) })
				count := len(h.clients)
				h.mu.Unlock()
				log.Printf("[ws] client disconnected (total: %d)", count)
			} else {
				h.mu.Unlock()
			}
		}
	}
}

// broadcastLoop drains the async broadcast channel and fans out to clients.
// This runs in its own goroutine so BroadcastState never blocks the Emit path.
func (h *Hub) broadcastLoop() {
	for msg := range h.broadcastCh {
		h.mu.RLock()
		for client := range h.clients {
			select {
			case client.Send <- msg.data:
			default:
				// Client can't keep up — schedule removal outside RLock
				go func(c *WsClient) {
					h.mu.Lock()
					if _, ok := h.clients[c]; ok {
						delete(h.clients, c)
						c.closeOnce.Do(func() { close(c.Send) })
					}
					h.mu.Unlock()
				}(client)
			}
		}
		h.mu.RUnlock()
	}
}

// BroadcastState marshals the payload and enqueues it to the async broadcast channel.
// This is non-blocking — if the broadcast channel is full, the message is dropped
// to prevent backpressure from slow WS clients from stalling the Emit pipeline.
func (h *Hub) BroadcastState(stateName, eventName string, payload map[string]interface{}) {
	msg := StateBroadcast{
		Type: "state", State: stateName, Event: eventName,
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	// Non-blocking enqueue to async broadcast channel
	select {
	case h.broadcastCh <- broadcastMsg{data: data}:
	default:
		// Broadcast channel full — drop to protect Emit throughput
	}
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
