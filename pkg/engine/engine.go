package engine

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
	"github.com/AmritRai1234/spine/pkg/middleware"
	"github.com/gorilla/websocket"
)

// contextKey is a private type for context value keys to avoid collisions.
type contextKey string

// accessContextKey is the context key for storing the resolved AccessContext.
const accessContextKey contextKey = "spine_access"

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
	APIKey        string           // Legacy single-key auth (backward compat)
	access        *AccessResolver  // Multi-key role-based access control
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
	eng := &Engine{
		Bus:           bus,
		Hub:           hub,
		Schema:        schema,
		customContext: middleware.NewCustomContextManager(),
	}

	// Wire access resolver if manifest defines access rules
	if len(schema.Access) > 0 {
		eng.access = NewAccessResolver(schema.Access)
	}

	return eng, nil
}

// NewFromFile parses a manifest and creates an Engine.
// Automatically checks and applies environment overlays (e.g. app.prod.spine) if present.
func NewFromFile(spineFile, dbPath string) (*Engine, error) {
	schema, err := manifest.ParseManifest(spineFile)
	if err != nil {
		return nil, err
	}

	// Environment Overlay Layering (Year 3 Feature)
	envMode := os.Getenv("SPINE_ENV")
	if envMode != "" {
		ext := filepath.Ext(spineFile)
		base := strings.TrimSuffix(spineFile, ext)
		overlayFile := fmt.Sprintf("%s.%s%s", base, strings.ToLower(envMode), ext)
		if _, statErr := os.Stat(overlayFile); statErr == nil {
			overlaySchema, overlayErr := manifest.ParseManifest(overlayFile)
			if overlayErr == nil {
				// Merge routes & tables from overlay
				schema.DbTables = append(schema.DbTables, overlaySchema.DbTables...)
				schema.Routes = append(schema.Routes, overlaySchema.Routes...)
				schema.Nodes = append(schema.Nodes, overlaySchema.Nodes...)
				log.Printf("[overlay] Applied environment overlay '%s'", overlayFile)
			}
		}
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
	var h http.HandlerFunc

	if e.access != nil && e.access.HasRules() {
		// Multi-key role-based auth: resolve key → AccessContext, inject into context
		h = func(w http.ResponseWriter, r *http.Request) {
			clientKey := extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
			ac := e.access.Resolve(clientKey)
			if ac == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{
					"status": "error",
					"error":  "unauthorized: invalid or missing API key",
				})
				return
			}
			ctx := context.WithValue(r.Context(), accessContextKey, ac)
			handler(w, r.WithContext(ctx))
		}
	} else {
		// Legacy single-key auth
		h = middleware.AuthMiddleware(e.APIKey, handler)
	}

	if e.rateLimiter != nil {
		h = e.rateLimiter.Middleware(h)
	}

	// Body size limiter for payload protection
	h = middleware.BodyLimitMiddleware(maxRequestBodySize, h)

	// Payload nesting depth limiter (max 32 levels) to prevent JSON bomb DOS attacks
	h = middleware.DepthLimitMiddleware(32, h)

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

// getAccessContext extracts the resolved AccessContext from the request context.
// Returns nil if no access rules are configured (legacy mode).
func getAccessContext(r *http.Request) *AccessContext {
	ac, _ := r.Context().Value(accessContextKey).(*AccessContext)
	return ac
}

// wsAuthCheck validates the API key for WebSocket connections.
// Accepts key via query param ?token= or X-API-Key header.
// Returns (authenticated, accessContext). accessContext may be nil in legacy mode.
func (e *Engine) wsAuthCheck(r *http.Request) (bool, *AccessContext) {
	// Multi-key access mode
	if e.access != nil && e.access.HasRules() {
		var key string
		if token := r.URL.Query().Get("token"); token != "" {
			key = token
		} else {
			key = extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
		}
		ac := e.access.Resolve(key)
		return ac != nil, ac
	}

	// Legacy single-key mode
	if e.APIKey == "" {
		return true, nil
	}

	var clientKey string
	if token := r.URL.Query().Get("token"); token != "" {
		clientKey = token
	} else {
		clientKey = extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
	}

	return subtle.ConstantTimeCompare([]byte(clientKey), []byte(e.APIKey)) == 1, nil
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

	mux.HandleFunc("/webhook/", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			w.Write([]byte(`{"status":"error","error":"method_not_allowed"}`))
			return
		}

		provider := strings.TrimPrefix(r.URL.Path, "/webhook/")
		if provider == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","error":"missing_webhook_provider"}`))
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
		if err != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","error":"cannot_read_body"}`))
			return
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			payload = map[string]interface{}{"raw_body": string(body)}
		}

		eventName := fmt.Sprintf("WEBHOOK_%s", strings.ToUpper(provider))
		payload["_provider"] = provider

		res, err := e.Bus.Emit(eventName, payload)
		if err != nil {
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"provider": provider,
			"event":    eventName,
			"result":   res,
		})
	}))

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		opt := e.Bus.GetOptimizer()
		rps := 0.0
		batchSize := 500
		mode := "Micro-Latency"
		if opt != nil {
			rps = opt.GetRPS()
			batchSize = opt.GetBatchSize()
			mode = opt.GetMode()
		}

		metrics := fmt.Sprintf(`# HELP spine_requests_per_second Current requests per second processed
# TYPE spine_requests_per_second gauge
spine_requests_per_second %.2f

# HELP spine_optimizer_batch_size Current adaptive batch size
# TYPE spine_optimizer_batch_size gauge
spine_optimizer_batch_size %d

# HELP spine_optimizer_mode Current optimization mode
# TYPE spine_optimizer_mode gauge
spine_optimizer_mode{mode="%s"} 1
`, rps, batchSize, mode)

		w.Write([]byte(metrics))
	})

	mux.HandleFunc("/admin/usage", e.wrapMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		opt := e.Bus.GetOptimizer()
		rps := 0.0
		if opt != nil {
			rps = opt.GetRPS()
		}

		usage := map[string]interface{}{
			"status":            "ok",
			"events_per_second": rps,
			"ws_connections":    e.Hub.ClientCount(),
			"timestamp":         time.Now().Format(time.RFC3339),
		}
		json.NewEncoder(w).Encode(usage)
	}))

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

		// Access control: check emit permissions
		if ac := getAccessContext(r); ac != nil {
			if !ac.CanEmit(body.Event) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{
					"status": "error",
					"error":  "forbidden: role '" + ac.Role + "' cannot emit event '" + body.Event + "'",
				})
				return
			}
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

		// Resolve access filter for this role
		var accessFilter string
		if ac := getAccessContext(r); ac != nil {
			accessFilter = ac.Filter
		}

		cursorStr := r.URL.Query().Get("cursor")
		whereParams := r.URL.Query()["where"]

		if cursorStr != "" {
			var lastID int64
			if id, err := strconv.ParseInt(cursorStr, 10, 64); err == nil {
				lastID = id
			}
			rows, nextCursor, err := e.Bus.GetTableRowsCursor(tableName, lastID, limit, accessFilter)
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
				return
			}
			resp := map[string]interface{}{
				"status": "ok",
				"table":  tableName,
				"count":  len(rows),
				"rows":   rows,
			}
			if nextCursor > 0 {
				resp["next_cursor"] = nextCursor
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		rows, err := func() ([]map[string]interface{}, error) {
			if len(whereParams) > 1 {
				filters := make(map[string]string)
				for _, wp := range whereParams {
					if idx := strings.Index(wp, ":"); idx > 0 {
						filters[wp[:idx]] = wp[idx+1:]
					}
				}
				return e.Bus.QueryMultiWhere(tableName, filters, limit, offset, accessFilter)
			} else if len(whereParams) == 1 {
				whereParam := whereParams[0]
				// Format: ?where=column:value
				if idx := strings.Index(whereParam, ":"); idx > 0 {
					col := whereParam[:idx]
					val := whereParam[idx+1:]
					return e.Bus.QueryWhereWithAccess(tableName, col, val, limit, offset, accessFilter)
				}
			}
			if accessFilter != "" {
				return e.Bus.GetTableRowsWithFilter(tableName, limit, offset, accessFilter)
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
		authenticated, wsAccess := e.wsAuthCheck(r)

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

					var authOk bool
					if e.access != nil && e.access.HasRules() {
						// Multi-key mode: resolve token to access context
						ac := e.access.Resolve(token)
						authOk = ac != nil
						if authOk {
							wsAccess = ac
						}
					} else {
						// Legacy single-key mode
						authOk = e.APIKey == "" || subtle.ConstantTimeCompare([]byte(token), []byte(e.APIKey)) == 1
					}

					if authOk {
						if !authenticated {
							authenticated = true
							e.Hub.Register <- client
						}
						ack := map[string]interface{}{"type": "auth_ack", "status": "ok"}
						if wsAccess != nil {
							ack["role"] = wsAccess.Role
						}
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

				// Handle WebSocket reconnect handshake with event log replay
				if msgType, _ := raw["type"].(string); msgType == "reconnect" {
					lastSeenIDFloat, _ := raw["last_seen_id"].(float64)
					lastSeenID := int64(lastSeenIDFloat)

					logs, err := e.Bus.GetEventLogs("", 100, 0)
					var missedEvents []map[string]interface{}
					if err == nil {
						for _, logItem := range logs {
							if id, ok := logItem["id"].(int64); ok && id > lastSeenID {
								missedEvents = append(missedEvents, logItem)
							}
						}
					}

					ack := map[string]interface{}{
						"type":          "reconnect_ack",
						"status":        "ok",
						"replayed":      len(missedEvents),
						"missed_events": missedEvents,
					}
					ackBytes, _ := json.Marshal(ack)
					select {
					case client.Send <- ackBytes:
					default:
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
					} else if wsAccess != nil && !wsAccess.CanEmit(req.Event) {
						ack["status"] = "error"
						ack["error"] = "forbidden: role '" + wsAccess.Role + "' cannot emit event '" + req.Event + "'"
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

				if e.Schema != nil {
					diff := DiffManifests(e.Schema, s)
					log.Printf("[spine] manifest diff: %s", diff.Summary())
				}

				e.Schema = s
				e.Bus.UpdateRegistry(manifest.NewRegistry(s))
				if len(s.Access) > 0 {
					e.access = NewAccessResolver(s.Access)
				}
				log.Printf("[spine] ✓ reloaded: %d nodes, %d routes", len(s.Nodes), len(s.Routes))
			}
		}
	}()
}
