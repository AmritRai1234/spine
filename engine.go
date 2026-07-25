package spine

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Engine is the top-level Spine runtime combining Bus, Hub, and Server.
type Engine struct {
	Bus       *Bus
	Hub       *Hub
	Schema    *SpineSchema
	spineFile string
}

// New creates a fully wired Engine from a parsed schema.
func New(schema *SpineSchema, dbPath string) (*Engine, error) {
	hub := NewHub()
	go hub.Run()
	reg := NewRegistry(schema)
	bus, err := NewBus(reg, dbPath, hub)
	if err != nil {
		return nil, err
	}
	return &Engine{Bus: bus, Hub: hub, Schema: schema}, nil
}

// NewFromFile parses a manifest and creates an Engine.
func NewFromFile(spineFile, dbPath string) (*Engine, error) {
	schema, err := ParseManifest(spineFile)
	if err != nil {
		return nil, err
	}
	eng, err := New(schema, dbPath)
	if err != nil {
		return nil, err
	}
	eng.spineFile = spineFile
	return eng, nil
}

// Close shuts down the engine.
func (e *Engine) Close() error {
	return e.Bus.Close()
}

// ListenAndServe starts HTTP+WS server and optional hot-reload watcher.
func (e *Engine) ListenAndServe(addr string) error {
	if e.spineFile != "" {
		e.startHotReload()
	}
	mux := e.buildMux()
	fmt.Println()
	fmt.Println("┌──────────────────────────────────────────┐")
	fmt.Println("│  SPINE v1 Runtime Server (Go)            │")
	fmt.Printf("│  HTTP:  http://%-26s │\n", addr)
	fmt.Printf("│  WS:    ws://%-28s │\n", addr+"/ws")
	fmt.Println("└──────────────────────────────────────────┘")
	fmt.Println()
	return http.ListenAndServe(addr, mux)
}

func (e *Engine) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","engine":"spine-go","version":1}`))
	})

	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(e.Bus.GetRegistry().GetSchema())
	})

	mux.HandleFunc("/emit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(405)
			w.Write([]byte(`{"status":"error","error":"method_not_allowed"}`))
			return
		}
		var body struct {
			Event   string                 `json:"event"`
			Payload map[string]interface{} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","error":"invalid_json_body"}`))
			return
		}
		if body.Event == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","error":"missing_event_name"}`))
			return
		}
		if body.Payload == nil {
			body.Payload = make(map[string]interface{})
		}
		result, err := e.Bus.Emit(body.Event, body.Payload)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "error", "error": err.Error(), "event": body.Event,
			})
			return
		}
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade error: %v", err)
			return
		}
		client := &WsClient{Conn: conn, Send: make(chan []byte, 256)}
		e.Hub.Register <- client

		go func() {
			defer conn.Close()
			for msg := range client.Send {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					break
				}
			}
		}()

		go func() {
			defer func() { e.Hub.Unregister <- client; conn.Close() }()
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					break
				}
				var req struct {
					Event   string                 `json:"event"`
					Payload map[string]interface{} `json:"payload"`
				}
				if err := json.Unmarshal(message, &req); err == nil && req.Event != "" {
					if req.Payload == nil {
						req.Payload = make(map[string]interface{})
					}
					result, err := e.Bus.Emit(req.Event, req.Payload)
					ack := map[string]interface{}{"type": "event_ack", "event": req.Event}
					if err != nil {
						ack["status"] = "error"
						ack["error"] = err.Error()
					} else {
						ack["status"] = "ok"
						ack["result"] = result
					}
					ackBytes, _ := json.Marshal(ack)
					select {
					case client.Send <- ackBytes:
					default:
					}
				}
			}
		}()
	})

	return mux
}

func (e *Engine) startHotReload() {
	var lastMod time.Time
	if fi, err := os.Stat(e.spineFile); err == nil {
		lastMod = fi.ModTime()
	}
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			fi, err := os.Stat(e.spineFile)
			if err != nil {
				continue
			}
			if fi.ModTime().After(lastMod) {
				lastMod = fi.ModTime()
				log.Printf("[spine] manifest change detected, reloading...")
				s, err := ParseManifest(e.spineFile)
				if err != nil {
					log.Printf("[spine] ✗ hot-reload failed: %v", err)
					continue
				}
				e.Bus.UpdateRegistry(NewRegistry(s))
				log.Printf("[spine] ✓ reloaded: %d nodes, %d routes", len(s.Nodes), len(s.Routes))
			}
		}
	}()
}
