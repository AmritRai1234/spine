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

type Hub struct {
	mu         sync.Mutex
	clients    map[*WsClient]bool
	Register   chan *WsClient
	Unregister chan *WsClient
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*WsClient]bool),
		Register:   make(chan *WsClient),
		Unregister: make(chan *WsClient),
	}
}

func (h *Hub) Run() {
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

func (h *Hub) BroadcastState(stateName, eventName string, payload map[string]interface{}) {
	msg := StateBroadcast{
		Type: "state", State: stateName, Event: eventName,
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client.Send <- data:
		default:
			client.closeOnce.Do(func() { close(client.Send) })
			delete(h.clients, client)
		}
	}
}

func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
