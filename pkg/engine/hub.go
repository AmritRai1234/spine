package engine

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
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
	// ID is the originating event's audit-log id (0 = omitted). Clients use
	// it as the reconnect replay cursor — without it the SDKs' lastSeenID
	// never advances and every reconnect replays the whole event log.
	ID int64 `json:"id,omitempty"`
}

type WsClient struct {
	Conn      *websocket.Conn
	Send      chan []byte
	closeOnce sync.Once
	// Access is the resolved RLAC context for this client (nil in legacy
	// mode or when no access rules are configured). The broadcast loop uses
	// it to withhold state messages whose originating event is outside the
	// client's Events whitelist.
	Access *AccessContext
}

// broadcastMsg is an internal type for the async broadcast channel.
type broadcastMsg struct {
	data      []byte
	eventName string // originating event — used for RLAC fan-out filtering
}

type Hub struct {
	mu          sync.RWMutex
	clients     map[*WsClient]bool
	Register    chan *WsClient
	Unregister  chan *WsClient
	broadcastCh chan broadcastMsg // async broadcast channel decouples Emit from WS fan-out

	stopCh            chan struct{}
	stopOnce          sync.Once
	closed            atomic.Bool // set by Close; BroadcastState drops instead of panicking
	droppedBroadcasts atomic.Int64 // broadcasts dropped because broadcastCh was full
}

func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*WsClient]bool),
		Register:    make(chan *WsClient),
		Unregister:  make(chan *WsClient),
		broadcastCh: make(chan broadcastMsg, 4096), // buffered to decouple from Emit path
		stopCh:      make(chan struct{}),
	}
}

// Close stops the hub's Run and broadcast loops. Idempotent; safe to call
// multiple times and from Engine.Close. After Close, BroadcastState drops
// messages (counted) instead of panicking.
func (h *Hub) Close() {
	h.stopOnce.Do(func() {
		h.closed.Store(true)
		close(h.stopCh)
	})
}

// DroppedBroadcasts returns how many state broadcasts were dropped because
// the async broadcast channel was saturated (observability for missed state).
func (h *Hub) DroppedBroadcasts() int64 {
	return h.droppedBroadcasts.Load()
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
		case <-h.stopCh:
			// NOTE: broadcastCh is deliberately NOT closed — closing it
			// while BroadcastState sends (Emit path) would be a data race
			// and a send-on-closed panic. broadcastLoop exits on stopCh;
			// post-close broadcasts are dropped via the closed flag.
			return
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
	for {
		select {
		case <-h.stopCh:
			return
		case msg := <-h.broadcastCh:
			// Collect slow clients under RLock (no goroutine spawns while holding lock)
			var slowClients []*WsClient

			h.mu.RLock()
			for client := range h.clients {
				// RLAC fan-out filter: a client with an Events whitelist only
				// receives state messages originating from whitelisted events.
				if client.Access != nil && !client.Access.CanReceive(msg.eventName) {
					continue
				}
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
}

// BroadcastState marshals the payload using a pooled buffer and enqueues it to the
// async broadcast channel. This is non-blocking — if the broadcast channel is full,
// the message is dropped to prevent backpressure from slow WS clients from stalling
// the Emit pipeline.
// BroadcastState broadcasts a state change without an audit id (0).
func (h *Hub) BroadcastState(stateName, eventName string, payload map[string]interface{}) {
	h.BroadcastStateID(stateName, eventName, payload, 0)
}

// BroadcastStateID broadcasts a state change stamped with the originating
// event's audit-log id — the WS reconnect replay cursor. id 0 omits the field
// (broadcasts with no committed audit row: failure states, chained emits,
// queue.publish).
func (h *Hub) BroadcastStateID(stateName, eventName string, payload map[string]interface{}, id int64) {
	// After Close, broadcastCh is closed — a select-send on it would PANIC
	// (sends on closed channels are "ready" in select), so drop and count.
	if h.closed.Load() {
		h.droppedBroadcasts.Add(1)
		return
	}
	buf := broadcastBufPool.Get().(*bytes.Buffer)
	buf.Reset()

	msg := StateBroadcast{
		Type: "state", State: stateName, Event: eventName,
		Payload: payload, Timestamp: time.Now().UnixMilli(), ID: id,
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
	case h.broadcastCh <- broadcastMsg{data: data, eventName: eventName}:
	default:
		// Broadcast channel full — drop to protect Emit throughput, but
		// COUNT the drop so missed state changes are observable.
		h.droppedBroadcasts.Add(1)
	}
}
