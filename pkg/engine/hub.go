package engine

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// broadcastBufPool pools JSON encoding buffers for BroadcastState.
// Avoids a fresh buffer allocation on every state broadcast.
var broadcastBufPool = sync.Pool{
	New: func() interface{} { return bytes.NewBuffer(make([]byte, 0, 512)) },
}

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

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
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
		// Collect slow clients under RLock (no goroutine spawns while holding lock)
		var slowClients []*WsClient

		h.mu.RLock()
		for client := range h.clients {
			select {
			case client.Send <- msg.data:
			default:
				// Client can't keep up — mark for removal
				slowClients = append(slowClients, client)
			}
		}
		h.mu.RUnlock()

		// Remove slow clients under write lock (outside RLock to prevent priority inversion)
		if len(slowClients) > 0 {
			h.mu.Lock()
			for _, c := range slowClients {
				if _, ok := h.clients[c]; ok {
					delete(h.clients, c)
					c.closeOnce.Do(func() { close(c.Send) })
				}
			}
			h.mu.Unlock()
		}
	}
}

// BroadcastState marshals the payload using a pooled buffer and enqueues it to the
// async broadcast channel. This is non-blocking — if the broadcast channel is full,
// the message is dropped to prevent backpressure from slow WS clients from stalling
// the Emit pipeline.
func (h *Hub) BroadcastState(stateName, eventName string, payload map[string]interface{}) {
	buf := broadcastBufPool.Get().(*bytes.Buffer)
	buf.Reset()

	msg := StateBroadcast{
		Type: "state", State: stateName, Event: eventName,
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	if err := json.NewEncoder(buf).Encode(msg); err != nil {
		broadcastBufPool.Put(buf)
		return
	}

	// Copy out the encoded bytes (strip trailing newline from Encode)
	raw := buf.Bytes()
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	data := make([]byte, len(raw))
	copy(data, raw)
	broadcastBufPool.Put(buf)

	// Non-blocking enqueue to async broadcast channel
	select {
	case h.broadcastCh <- broadcastMsg{data: data}:
	default:
		// Broadcast channel full — drop to protect Emit throughput
	}
}
