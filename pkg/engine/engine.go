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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
	"github.com/AmritRai1234/spine/pkg/middleware"
	"github.com/gorilla/websocket"
)

// contextKey is a private type for context value keys to avoid collisions.
type contextKey string

// accessContextKey is the context key for storing the resolved AccessContext.
const accessContextKey contextKey = "spine_access"

// maxRequestBodySize is the maximum allowed request body size (1 MB by
// default; override with SPINE_MAX_BODY_BYTES for image-heavy payloads such
// as data-URL product images). Fail-closed: invalid/empty values keep 1 MB.
func maxBodyBytesFromEnv(v string) int64 {
	if v == "" {
		return 1 << 20
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return n
	}
	return 1 << 20
}

var maxRequestBodySize = maxBodyBytesFromEnv(os.Getenv("SPINE_MAX_BODY_BYTES"))

// WebSocket resource hardening knobs (gorilla canonical keepalive pattern).
const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 45 * time.Second
	wsPingPeriod     = 30 * time.Second
	wsMaxMessageSize = 1 << 20 // 1 MB, matches the HTTP body cap
)

// wsOriginPolicy builds a CheckOrigin function that allows origins based on env SPINE_WS_ORIGINS.
// - No Origin header => allowed (non-browser clients).
// - Origin host[:port] == r.Host => allowed (same-origin).
// - Origin exactly matches an entry in the allowlist => allowed.
// - Allowlist contains "*" => all origins allowed.
// The allowlist is re-read per check (cheap: one os.Getenv) so tests and
// hot-reconfigured deployments can change SPINE_WS_ORIGINS at runtime.
func wsOriginCheck(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	// No Origin => non-browser client, allow
	if origin == "" {
		return true
	}
	// Same-origin check: compare authority (host[:port]) on both sides so a
	// dev server on localhost:3000 talking to an engine on :8080 is NOT
	// silently same-origin, but an engine behind its own origin is.
	if extractHost(origin) == extractHost(r.Host) {
		return true
	}
	raw := os.Getenv("SPINE_WS_ORIGINS")
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// extractHost extracts the authority (host or host:port) from an Origin URL
// (scheme://host[:port]/path) or from r.Host. Port is preserved — same-origin
// means scheme-host-port equality for our purposes; only the scheme is dropped.
func extractHost(origin string) string {
	// Strip scheme
	if idx := strings.Index(origin, "://"); idx > 0 {
		origin = origin[idx+3:]
	}
	// Strip any path (Origins normally carry none, but be strict)
	if idx := strings.Index(origin, "/"); idx > 0 {
		origin = origin[:idx]
	}
	return origin
}

var upgrader = websocket.Upgrader{
	CheckOrigin: wsOriginCheck,
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
	APIKey        string                         // Legacy single-key auth (backward compat)
	accessPtr     atomic.Pointer[AccessResolver] // Multi-key role-based access control (hot-swappable)
	rateLimiter   *middleware.RateLimitManager
	customContext *middleware.CustomContextManager
	spineFile     string

	webhookSecrets map[string]string // provider -> secret
	webhookMu      sync.RWMutex

	reloadMu       sync.Mutex    // guards Schema field swaps & diff logging during hot-reload
	reloadStop     chan struct{} // closed to stop the hot-reload watcher goroutine
	reloadStopOnce sync.Once
	reloadInterval time.Duration // manifest poll interval for hot-reload

	// WebSocket hardening knobs
	wsMaxConns    int           // connection cap (refuse upgrades with 503 above this)
	wsAuthTimeout time.Duration // first-message auth deadline (close 4001)
	wsConnCount   atomic.Int64  // live WebSocket connections
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
		Bus:            bus,
		Hub:            hub,
		Schema:         schema,
		customContext:  middleware.NewCustomContextManager(),
		reloadInterval: time.Second,
		webhookSecrets: make(map[string]string),
		wsMaxConns:     10000,
		wsAuthTimeout:  5 * time.Second,
	}

	// WebSocket connection cap from env (invalid/negative values fall back to
	// the default).
	if v := os.Getenv("SPINE_WS_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			eng.wsMaxConns = n
		}
	}

	// Wire access resolver if manifest defines access rules
	if len(schema.Access) > 0 {
		eng.accessPtr.Store(NewAccessResolver(schema.Access))
	}

	// Auto-populate webhook secrets from environment
	eng.loadWebhookSecretsFromEnv()

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

// Access returns the engine's AccessResolver (nil if no rules are defined).
func (e *Engine) Access() *AccessResolver {
	return e.accessPtr.Load()
}

// SetAPIKey configures the API key requirement for protected HTTP endpoints.
func (e *Engine) SetAPIKey(key string) {
	e.APIKey = key
}

// SetWebhookSecret configures the HMAC secret for a given webhook provider.
// Use before serving; not safe for concurrent use during serving.
func (e *Engine) SetWebhookSecret(provider, secret string) {
	e.webhookMu.Lock()
	defer e.webhookMu.Unlock()
	if e.webhookSecrets == nil {
		e.webhookSecrets = make(map[string]string)
	}
	if secret == "" {
		delete(e.webhookSecrets, provider)
	} else {
		e.webhookSecrets[provider] = secret
	}
}

// GetWebhookSecret returns the configured secret for a provider.
func (e *Engine) GetWebhookSecret(provider string) string {
	e.webhookMu.RLock()
	defer e.webhookMu.RUnlock()
	return e.webhookSecrets[provider]
}

// loadWebhookSecretsFromEnv reads webhook secrets from environment variables.
// Looks for SPINE_WEBHOOK_SECRET_<PROVIDER> and the canonical <PROVIDER>_WEBHOOK_SECRET.
func (e *Engine) loadWebhookSecretsFromEnv() {
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		key := parts[0]
		val := parts[1]

		// Match SPINE_WEBHOOK_SECRET_<PROVIDER>
		if strings.HasPrefix(key, "SPINE_WEBHOOK_SECRET_") {
			provider := strings.ToLower(strings.TrimPrefix(key, "SPINE_WEBHOOK_SECRET_"))
			e.SetWebhookSecret(provider, val)
			continue
		}

		// Match <PROVIDER>_WEBHOOK_SECRET (canonical form like STRIPE_WEBHOOK_SECRET)
		if strings.HasSuffix(key, "_WEBHOOK_SECRET") {
			provider := strings.ToLower(strings.TrimSuffix(key, "_WEBHOOK_SECRET"))
			e.SetWebhookSecret(provider, val)
		}
	}
}

// Close shuts down the engine, its hot-reload watcher, and its rate limiter.
func (e *Engine) Close() error {
	e.reloadStopOnce.Do(func() {
		if e.reloadStop != nil {
			close(e.reloadStop)
		}
	})
	if e.rateLimiter != nil {
		e.rateLimiter.Close()
	}
	return e.Bus.Close()
}

// ListenAndServe starts HTTP+WS server with graceful shutdown on SIGINT/SIGTERM.
func (e *Engine) ListenAndServe(addr string) error {
	if e.spineFile != "" {
		e.StartHotReload()
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

	return e.serveWithShutdown(
		[]*http.Server{srv},
		[]func() error{srv.ListenAndServe},
	)
}

// HTTPHandler returns the configured http.Handler for embedding in custom servers.
func (e *Engine) HTTPHandler() http.Handler {
	return e.buildMux()
}

func (e *Engine) wrapMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	// Precompute the legacy single-key handler once.
	legacyAuth := middleware.AuthMiddleware(e.APIKey, handler)

	// Resolve the access resolver per-request so hot-reloaded rules take
	// effect immediately without rebuilding the mux.
	h := func(w http.ResponseWriter, r *http.Request) {
		if resolver := e.accessPtr.Load(); resolver != nil && resolver.HasRules() {
			clientKey := extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
			ac := resolver.Resolve(clientKey)
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
			return
		}
		legacyAuth(w, r)
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

// wrapWebhookMiddleware applies the same chain as wrapMiddleware minus the auth layer.
// Webhook endpoints carry provider-signed payloads authenticated via webhook_verify middleware.
func (e *Engine) wrapWebhookMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	h := handler

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

	// Webhook signature verification (HMAC-SHA256)
	h = middleware.WebhookVerifyMiddleware(e.GetWebhookSecret, h)

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
	if resolver := e.accessPtr.Load(); resolver != nil && resolver.HasRules() {
		var key string
		if token := r.URL.Query().Get("token"); token != "" {
			key = token
		} else {
			key = extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
		}
		ac := resolver.Resolve(key)
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
		// Truthful readiness: ping the database so orchestrators don't get a
		// healthy signal while the engine's write path is down.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := e.Bus.DB().PingContext(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not_ready","error":"database unreachable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ready"}`))
	})

	mux.HandleFunc("/webhook/", e.wrapWebhookMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

		// Idempotency stamp: use the event "id" (Stripe event id) as idempotency key
		if idVal, ok := payload["id"]; ok {
			if idStr, ok := idVal.(string); ok && idStr != "" {
				payload["_idempotency_key"] = idStr
			}
		}

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

# HELP spine_commit_failures Batch commits that failed after retries (batch spilled)
# TYPE spine_commit_failures counter
spine_commit_failures %d

# HELP spine_spill_writes Writes durably retained in _spine_write_spill
# TYPE spine_spill_writes counter
spine_spill_writes %d

# HELP spine_lost_writes Writes dropped because even the spill insert failed
# TYPE spine_lost_writes counter
spine_lost_writes %d

# HELP spine_dropped_audit Audit rows dropped due to shard saturation
# TYPE spine_dropped_audit counter
spine_dropped_audit %d
`, rps, batchSize, mode,
			e.Bus.CommitFailures(), e.Bus.SpillWrites(), e.Bus.LostWrites(), e.Bus.DroppedAudit())

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

	mux.HandleFunc("/ws", wrapWSHandler(func(w http.ResponseWriter, r *http.Request) {
		// Connection cap: refuse upgrades above the limit BEFORE any upgrade
		// work (cheap atomic read).
		if e.wsConnCount.Load() >= int64(e.wsMaxConns) {
			http.Error(w, "too many websocket connections", http.StatusServiceUnavailable)
			return
		}

		// WebSocket auth check — upfront header/query param check
		authenticated, wsAccess := e.wsAuthCheck(r)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade error: %v", err)
			return
		}
		// Decrement happens in the read-loop cleanup (below), NOT here — this
		// handler returns immediately after spawning the read/write loops, so
		// a deferred decrement would undercount live connections.
		e.wsConnCount.Add(1)

		// Resource hardening (gorilla canonical pattern): cap message size,
		// and require pongs to reset the read deadline so dead peers are
		// reaped instead of holding connections forever.
		conn.SetReadLimit(wsMaxMessageSize)
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(wsPongWait))
		})

		client := &WsClient{Conn: conn, Send: make(chan []byte, 256)}

		if authenticated {
			e.Hub.Register <- client
		}

		// First-message auth deadline: unauthenticated connections are closed
		// with 4001 after e.wsAuthTimeout (websocket.org guidance — without
		// it, unauthenticated sockets sit open indefinitely).
		var authTimer *time.Timer
		if !authenticated {
			authTimer = time.AfterFunc(e.wsAuthTimeout, func() {
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication timeout"),
					time.Now().Add(wsWriteWait))
				_ = conn.Close()
			})
		}

		// Writer goroutine: state messages + periodic pings.
		go func() {
			defer conn.Close()
			pingTicker := time.NewTicker(wsPingPeriod)
			defer pingTicker.Stop()
			for {
				select {
				case msg, ok := <-client.Send:
					if !ok {
						return
					}
					_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
					if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						return
					}
				case <-pingTicker.C:
					_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
					if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
						return
					}
				}
			}
		}()

		go func() {
			defer func() {
				if authTimer != nil {
					authTimer.Stop()
				}
				if authenticated {
					e.Hub.Unregister <- client
				}
				e.wsConnCount.Add(-1) // connection fully closed
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
					if resolver := e.accessPtr.Load(); resolver != nil && resolver.HasRules() {
						// Multi-key mode: resolve token to access context
						ac := resolver.Resolve(token)
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
							if authTimer != nil {
								authTimer.Stop()
							}
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

				// Handle WebSocket reconnect handshake with event log replay.
				// AUTH-GATED: replay returns full event payloads, so it must
				// never be served to unauthenticated connections.
				if msgType, _ := raw["type"].(string); msgType == "reconnect" {
					if !authenticated {
						ack := map[string]interface{}{"type": "reconnect_ack", "status": "error", "error": "unauthorized: authentication required"}
						ackBytes, _ := json.Marshal(ack)
						select {
						case client.Send <- ackBytes:
						default:
						}
						continue
					}
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
	}))

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

// SetHotReloadInterval overrides the manifest poll interval (default 1s).
// Must be called before StartHotReload / ListenAndServe.
func (e *Engine) SetHotReloadInterval(d time.Duration) {
	if d > 0 {
		e.reloadInterval = d
	}
}

// SetMaxWSConns overrides the WebSocket connection cap (default 10000, or
// SPINE_WS_MAX_CONNS). Must be called before serving.
func (e *Engine) SetMaxWSConns(n int) {
	if n > 0 {
		e.wsMaxConns = n
	}
}

// SetWSAuthTimeout overrides the first-message authentication deadline
// (default 5s). Must be called before serving.
func (e *Engine) SetWSAuthTimeout(d time.Duration) {
	if d > 0 {
		e.wsAuthTimeout = d
	}
}

// wrapWSHandler applies panic recovery to a WebSocket handler. The standard
// HTTP middleware chain does not cover /ws (it is registered directly on the
// mux), so a panic there would otherwise kill the whole process.
func wrapWSHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[ws] panic recovered: %v", rec)
			}
		}()
		h(w, r)
	}
}

// StartHotReload begins watching the manifest and its includes for changes.
// On change: re-parse, validate, atomically swap the route registry, ensure
// newly declared tables, and refresh the access resolver. Invalid manifests
// are rejected and the previous schema keeps serving (rollback by omission).
func (e *Engine) StartHotReload() {
	if e.spineFile == "" || e.reloadStop != nil {
		return
	}
	e.reloadStop = make(chan struct{})
	stop := e.reloadStop
	interval := e.reloadInterval

	snapshot := watchSnapshot(e.watchSet())

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				current := watchSnapshot(e.watchSet())
				if !snapshotChanged(snapshot, current) {
					continue
				}
				time.Sleep(150 * time.Millisecond) // debounce editor multi-writes
				e.reloadManifest()
				snapshot = watchSnapshot(e.watchSet()) // pick up newly added includes
			}
		}
	}()
	log.Printf("[spine] hot-reload watcher active on '%s' (+includes)", e.spineFile)
}

// reloadManifest parses the current manifest and swaps it in if valid.
func (e *Engine) reloadManifest() {
	s, err := manifest.ParseManifest(e.spineFile)
	if err != nil {
		log.Printf("[spine] ✗ hot-reload failed (keeping previous schema): %v", err)
		return
	}

	e.reloadMu.Lock()
	if e.Schema != nil {
		diff := DiffManifests(e.Schema, s)
		if summary := diff.Summary(); summary != "" {
			log.Printf("[spine] manifest diff: %s", summary)
		}
	}
	e.Schema = s
	e.reloadMu.Unlock()

	// Ensure tables BEFORE swapping the registry: if the new manifest declares
	// tables the database cannot create, keep serving the previous schema
	// instead of routing into missing tables.
	if err := e.Bus.EnsureTables(s.DbTables); err != nil {
		log.Printf("[spine] ✗ hot-reload table creation failed (keeping previous schema): %v", err)
		return
	}
	e.Bus.UpdateRegistry(manifest.NewRegistry(s))
	// Always rebuild the access resolver — an empty ruleset must clear a
	// previously configured resolver, not leave stale rules in force.
	e.accessPtr.Store(NewAccessResolver(s.Access))
	log.Printf("[spine] ✓ hot-reloaded: %d nodes, %d routes", len(s.Nodes), len(s.Routes))
}

// watchSet returns the root manifest plus all transitively included .spine files.
func (e *Engine) watchSet() []string {
	files := []string{e.spineFile}
	seen := map[string]bool{e.spineFile: true}
	queue := []string{e.spineFile}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		dir := filepath.Dir(f)
		for _, inc := range parseIncludeRefs(data) {
			p := inc
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			if !seen[p] {
				seen[p] = true
				files = append(files, p)
				queue = append(queue, p)
			}
		}
	}
	return files
}

// parseIncludeRefs extracts include file paths from a manifest's raw text,
// accepting both canonical `includes:` and legacy `include:` spellings.
func parseIncludeRefs(data []byte) []string {
	var refs []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "includes:" || trimmed == "include:" {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				item = strings.Trim(item, `"'`)
				if item != "" && !strings.HasPrefix(item, "#") {
					refs = append(refs, item)
				}
				continue
			}
			inBlock = false // next top-level key reached
		}
	}
	return refs
}

// watchSnapshot captures modtimes of the given files. Missing files map to
// the zero time so creation/deletion are detected as changes.
func watchSnapshot(files []string) map[string]time.Time {
	snap := make(map[string]time.Time, len(files))
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			snap[f] = time.Time{}
			continue
		}
		snap[f] = fi.ModTime()
	}
	return snap
}

// snapshotChanged reports whether two watch snapshots differ.
func snapshotChanged(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return true
	}
	for f, mt := range a {
		if b[f] != mt {
			return true
		}
	}
	return false
}
