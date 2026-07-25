package spine

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Pools to reduce allocations on the hot path
var bufPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

type emitRequest struct {
	Event   string                 `json:"event"`
	Payload map[string]interface{} `json:"payload"`
}

var emitReqPool = sync.Pool{
	New: func() interface{} { return new(emitRequest) },
}

// Engine is the top-level Spine runtime combining Bus, Hub, and Server.
type Engine struct {
	Bus       *Bus
	Hub       *Hub
	Schema    *SpineSchema
	APIKey    string
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

// SetAPIKey configures the API key requirement for protected HTTP endpoints.
func (e *Engine) SetAPIKey(key string) {
	e.APIKey = key
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
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	srv.SetKeepAlivesEnabled(true)
	return srv.ListenAndServe()
}

// HTTPHandler returns the configured http.Handler for embedding in custom servers.
func (e *Engine) HTTPHandler() http.Handler {
	return e.buildMux()
}

func (e *Engine) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","engine":"spine-go","version":1}`))
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy"}`))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ready"}`))
	})

	mux.HandleFunc("/schema", AuthMiddleware(e.APIKey, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(e.Bus.GetRegistry().GetSchema())
	}))

	mux.HandleFunc("/emit", AuthMiddleware(e.APIKey, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(405)
			w.Write([]byte(`{"status":"error","error":"method_not_allowed"}`))
			return
		}

		// Pool the request struct to avoid allocs
		body := emitReqPool.Get().(*emitRequest)
		body.Event = ""
		body.Payload = nil
		defer emitReqPool.Put(body)

		// Read body once, unmarshal (faster than NewDecoder for small payloads)
		raw, err := io.ReadAll(r.Body)
		if err != nil || json.Unmarshal(raw, body) != nil {
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

		result, emitErr := e.Bus.Emit(body.Event, body.Payload)

		// Pool the response buffer
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)

		w.Header().Set("Content-Type", "application/json")
		if emitErr != nil {
			w.WriteHeader(400)
			json.NewEncoder(buf).Encode(map[string]interface{}{
				"status": "error", "error": emitErr.Error(), "event": body.Event,
			})
		} else {
			json.NewEncoder(buf).Encode(result)
		}
		w.Write(buf.Bytes())
	}))

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		opt := e.Bus.GetOptimizer()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":            "ok",
			"optimizer_mode":    opt.GetMode(),
			"target_batch_size": opt.GetBatchSize(),
			"flush_interval":    opt.GetFlushInterval().String(),
		})
	})

	mux.HandleFunc("/tables", AuthMiddleware(e.APIKey, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tables, err := e.Bus.GetTables()
		if err != nil {
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"tables": tables,
		})
	}))

	mux.HandleFunc("/tables/", AuthMiddleware(e.APIKey, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tableName := strings.TrimPrefix(r.URL.Path, "/tables/")
		if tableName == "" {
			tables, err := e.Bus.GetTables()
			if err != nil {
				w.WriteHeader(500)
				json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "tables": tables})
			return
		}

		limit := 50
		offset := 0
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if l, err := strconv.Atoi(lStr); err == nil {
				limit = l
			}
		}
		if oStr := r.URL.Query().Get("offset"); oStr != "" {
			if o, err := strconv.Atoi(oStr); err == nil {
				offset = o
			}
		}

		rows, err := e.Bus.GetTableRows(tableName, limit, offset)
		if err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"table":  tableName,
			"count":  len(rows),
			"rows":   rows,
		})
	}))

	mux.HandleFunc("/events", AuthMiddleware(e.APIKey, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eventName := r.URL.Query().Get("event")
		limit := 50
		offset := 0
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if l, err := strconv.Atoi(lStr); err == nil {
				limit = l
			}
		}
		if oStr := r.URL.Query().Get("offset"); oStr != "" {
			if o, err := strconv.Atoi(oStr); err == nil {
				offset = o
			}
		}

		logs, err := e.Bus.GetEventLogs(eventName, limit, offset)
		if err != nil {
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"count":  len(logs),
			"events": logs,
		})
	}))

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
