package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
	"github.com/AmritRai1234/spine/pkg/middleware"
	"github.com/gorilla/websocket"
)

// maxRequestBodySize is the maximum allowed request body size (1 MB).
const maxRequestBodySize = 1 << 20 // 1 MB

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
	Bus           *Bus
	Hub           *Hub
	Schema        *manifest.SpineSchema
	APIKey        string
	rateLimiter   *middleware.RateLimitManager
	customContext *middleware.CustomContextManager
	spineFile     string
}

// SetRateLimit enables IP-based token bucket rate limiting on public endpoints.
func (e *Engine) SetRateLimit(rps, burst float64) {
	e.rateLimiter = middleware.NewRateLimitManager(rps, burst)
}

// RegisterCustomExtractor registers a function to dynamically extract custom attributes (e.g. location, temperature, device info) from HTTP requests.
func (e *Engine) RegisterCustomExtractor(fn middleware.CustomExtractorFunc) {
	e.customContext.AddExtractor(fn)
}

// SetStaticContext registers a static key-value attribute into request context (e.g. region="us-west-2", environment="production").
func (e *Engine) SetStaticContext(key string, value interface{}) {
	e.customContext.SetStaticAttribute(key, value)
}

// New creates a fully wired Engine from a parsed schema.
func New(schema *manifest.SpineSchema, dbPath string) (*Engine, error) {
	hub := NewHub()
	go hub.Run()
	reg := manifest.NewRegistry(schema)
	bus, err := NewBus(reg, dbPath, hub)
	if err != nil {
		return nil, err
	}
	return &Engine{
		Bus:           bus,
		Hub:           hub,
		Schema:        schema,
		customContext: middleware.NewCustomContextManager(),
	}, nil
}

// NewFromFile parses a manifest and creates an Engine.
func NewFromFile(spineFile, dbPath string) (*Engine, error) {
	schema, err := manifest.ParseManifest(spineFile)
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

// Close shuts down the engine and its rate limiter.
func (e *Engine) Close() error {
	if e.rateLimiter != nil {
		e.rateLimiter.Close()
	}
	return e.Bus.Close()
}

// ListenAndServe starts HTTP+WS server with graceful shutdown on SIGINT/SIGTERM.
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

	// Graceful shutdown on SIGINT/SIGTERM
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("[spine] received signal %v, shutting down gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[spine] graceful shutdown error: %v", err)
			return err
		}
		log.Printf("[spine] server stopped")
		return nil
	}
}

// HTTPHandler returns the configured http.Handler for embedding in custom servers.
func (e *Engine) HTTPHandler() http.Handler {
	return e.buildMux()
}

func (e *Engine) wrapMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	// Base handler wrapped with Auth and RateLimiting
	h := middleware.AuthMiddleware(e.APIKey, handler)
	if e.rateLimiter != nil {
		h = e.rateLimiter.Middleware(h)
	}

	// Body size limiter for payload protection
	h = middleware.BodyLimitMiddleware(maxRequestBodySize, h)

	// Security headers & CORS
	h = middleware.SecurityHeadersMiddleware(h)
	h = middleware.CORSMiddleware(middleware.DefaultCORSOptions(), h)

	// Custom Context Extractor (Location, Temperature, Custom Metadata)
	if e.customContext != nil {
		h = e.customContext.Middleware(h)
	}

	// Logging & Request ID tracing
	h = middleware.LoggingMiddleware(h)

	// Outer panic recovery handler
	h = middleware.RecoveryMiddleware(h)

	return h
}

// wsAuthCheck validates the API key for WebSocket connections.
// Accepts key via query param ?token= or X-API-Key header.
func (e *Engine) wsAuthCheck(r *http.Request) bool {
	if e.APIKey == "" {
		return true
	}

	// Check query parameter
	if token := r.URL.Query().Get("token"); token != "" {
		return token == e.APIKey
	}

	// Check headers (same logic as AuthMiddleware)
	clientKey := r.Header.Get("X-API-Key")
	if clientKey == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			clientKey = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	return clientKey == e.APIKey
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

	mux.HandleFunc("/schema", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(e.Bus.GetRegistry().GetSchema())
	}))

	mux.HandleFunc("/emit", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

		// Read body with size limit to prevent memory exhaustion
		limitedReader := io.LimitReader(r.Body, maxRequestBodySize)
		raw, err := io.ReadAll(limitedReader)
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

		// Merge custom context attributes (e.g. location, temperature, device, custom fields)
		body.Payload = middleware.MergeCustomContextIntoPayload(r.Context(), body.Payload)

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

	mux.HandleFunc("/tables", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/tables/", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

		rows, err := func() ([]map[string]interface{}, error) {
			if whereParam := r.URL.Query().Get("where"); whereParam != "" {
				// Format: ?where=column:value
				if idx := strings.Index(whereParam, ":"); idx > 0 {
					col := whereParam[:idx]
					val := whereParam[idx+1:]
					return e.Bus.QueryWhere(tableName, col, val, limit, offset)
				}
			}
			return e.Bus.GetTableRows(tableName, limit, offset)
		}()
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

	mux.HandleFunc("/events", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
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
		// WebSocket auth check — upfront header/query param check
		authenticated := e.wsAuthCheck(r)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade error: %v", err)
			return
		}
		client := &WsClient{Conn: conn, Send: make(chan []byte, 256)}
		
		if authenticated {
			e.Hub.Register <- client
		}

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
			defer func() {
				if authenticated {
					e.Hub.Unregister <- client
				}
				conn.Close()
			}()
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					break
				}

				var raw map[string]interface{}
				if err := json.Unmarshal(message, &raw); err != nil {
					continue
				}

				// Handle in-band WS authentication handshake (for browser clients)
				if msgType, _ := raw["type"].(string); msgType == "auth" {
					token, _ := raw["token"].(string)
					if e.APIKey == "" || token == e.APIKey {
						if !authenticated {
							authenticated = true
							e.Hub.Register <- client
						}
						ack := map[string]interface{}{"type": "auth_ack", "status": "ok"}
						ackBytes, _ := json.Marshal(ack)
						select {
						case client.Send <- ackBytes:
						default:
						}
					} else {
						ack := map[string]interface{}{"type": "auth_ack", "status": "error", "error": "invalid API key"}
						ackBytes, _ := json.Marshal(ack)
						select {
						case client.Send <- ackBytes:
						default:
						}
					}
					continue
				}

				// Handle event emit request
				var req struct {
					Event   string                 `json:"event"`
					Payload map[string]interface{} `json:"payload"`
				}
				if err := json.Unmarshal(message, &req); err == nil && req.Event != "" {
					ack := map[string]interface{}{"type": "event_ack", "event": req.Event}
					if !authenticated {
						ack["status"] = "error"
						ack["error"] = "unauthorized: authentication required"
					} else {
						if req.Payload == nil {
							req.Payload = make(map[string]interface{})
						}
						result, err := e.Bus.Emit(req.Event, req.Payload)
						if err != nil {
							ack["status"] = "error"
							ack["error"] = err.Error()
						} else {
							ack["status"] = "ok"
							ack["result"] = result
						}
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

	// Serve static web dashboard
	if fi, err := os.Stat("web/dist"); err == nil && fi.IsDir() {
		fs := http.FileServer(http.Dir("web/dist"))
		mux.Handle("/", fs)
	} else if fi, err := os.Stat("web"); err == nil && fi.IsDir() {
		fs := http.FileServer(http.Dir("web"))
		mux.Handle("/", fs)
	}

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
				s, err := manifest.ParseManifest(e.spineFile)
				if err != nil {
					log.Printf("[spine] ✗ hot-reload failed: %v", err)
					continue
				}
				e.Bus.UpdateRegistry(manifest.NewRegistry(s))
				log.Printf("[spine] ✓ reloaded: %d nodes, %d routes", len(s.Nodes), len(s.Routes))
			}
		}
	}()
}
