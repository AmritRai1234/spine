// main.go — Spine v1 Runtime Engine (Go)
//
// Features:
//   - Manifest Parsing (State Machine)
//   - Thread-Safe Dynamic Registry & Hot Reloading
//   - Schema-Based Payload Type Validation
//   - SQLite WAL Persistence Engine with Auto-Schema
//   - HTTP API (/emit, /health, /schema) & Real-time WebSockets (/ws)
//   - State Broadcast Engine over WebSockets

package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

// =====================================================================
//  Schema Structs
// =====================================================================

type PayloadField struct {
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
}

type Emit struct {
	Event  string         `json:"event"`
	Fields []PayloadField `json:"fields,omitempty"`
}

type Listen struct {
	State  string         `json:"state"`
	Fields []PayloadField `json:"fields,omitempty"`
}

type Node struct {
	Name      string   `json:"name"`
	OwnsFiles []string `json:"owns_files,omitempty"`
	Emits     []Emit   `json:"emits,omitempty"`
	Listens   []Listen `json:"listens,omitempty"`
}

type RouteStep struct {
	Action string `json:"action"`
	Table  string `json:"table,omitempty"`
	Input  string `json:"input,omitempty"`
}

type Route struct {
	OnEvent   string      `json:"on_event"`
	Steps     []RouteStep `json:"steps"`
	EmitState string      `json:"emit_state,omitempty"`
}

type SpineSchema struct {
	SpineVersion int      `json:"spine_version"`
	DbTables     []string `json:"db_tables"`
	Nodes        []Node   `json:"nodes"`
	Routes       []Route  `json:"routes"`
}

// =====================================================================
//  Parser — Line-by-Line State Machine
// =====================================================================

type parseState int

const (
	sTop parseState = iota
	sDatabase
	sDbTables
	sNodes
	sNodeBody
	sNodeOwnFiles
	sNodeEmits
	sNodeEmitEntry
	sNodeEmitPayload
	sNodeListens
	sNodeListenEntry
	sNodeListenPayload
	sRoutes
	sRouteBody
	sRouteSteps
	sRouteStepBody
)

func getIndent(line string) int {
	count := 0
	for _, c := range line {
		if c == ' ' {
			count++
		} else {
			break
		}
	}
	return count / 2
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func kvValue(trimmed, key string) (string, bool) {
	if strings.HasPrefix(trimmed, key) && len(trimmed) > len(key) && trimmed[len(key)] == ':' {
		return strings.TrimSpace(trimmed[len(key)+1:]), true
	}
	return "", false
}

func isListItem(trimmed string) bool {
	return strings.HasPrefix(trimmed, "- ")
}

func listKvValue(trimmed, key string) (string, bool) {
	if !isListItem(trimmed) {
		return "", false
	}
	return kvValue(trimmed[2:], key)
}

func parseSpine(filepath string) (*SpineSchema, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("cannot open manifest '%s': %w", filepath, err)
	}
	defer f.Close()

	schema := &SpineSchema{}
	state := sTop

	var curNode *Node
	var curEmit *Emit
	var curListen *Listen
	var curRoute *Route
	var curStep *RouteStep

	scanner := bufio.NewScanner(f)
	lineno := 0

	for scanner.Scan() {
		lineno++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		indent := getIndent(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// ===== TOP LEVEL =====
		if indent == 0 {
			state = sTop
			curNode = nil
			curEmit = nil
			curListen = nil
			curRoute = nil
			curStep = nil

			if v, ok := kvValue(trimmed, "spine_version"); ok {
				fmt.Sscanf(v, "%d", &schema.SpineVersion)
				continue
			}
			if trimmed == "database:" {
				state = sDatabase
				continue
			}
			if trimmed == "nodes:" {
				state = sNodes
				continue
			}
			if trimmed == "routes:" {
				state = sRoutes
				continue
			}
		}

		// ===== DATABASE =====
		if state == sDatabase && indent == 1 && trimmed == "tables:" {
			state = sDbTables
			continue
		}
		if state == sDbTables {
			if indent == 2 && isListItem(trimmed) {
				schema.DbTables = append(schema.DbTables, unquote(trimmed[2:]))
				continue
			}
			if indent <= 1 {
				state = sTop
			}
		}

		// ===== NODES =====
		if state >= sNodes && state <= sNodeListenPayload {
			// New node
			if indent == 1 && strings.HasSuffix(trimmed, ":") && !isListItem(trimmed) {
				n := Node{Name: trimmed[:len(trimmed)-1]}
				schema.Nodes = append(schema.Nodes, n)
				curNode = &schema.Nodes[len(schema.Nodes)-1]
				curEmit = nil
				curListen = nil
				state = sNodeBody
				continue
			}

			if indent == 2 && curNode != nil {
				switch trimmed {
				case "owns_files:":
					state = sNodeOwnFiles
					continue
				case "emits:":
					state = sNodeEmits
					continue
				case "listens:":
					state = sNodeListens
					continue
				}
			}

			if state == sNodeOwnFiles && indent == 3 && isListItem(trimmed) && curNode != nil {
				curNode.OwnsFiles = append(curNode.OwnsFiles, unquote(trimmed[2:]))
				continue
			}

			if state == sNodeEmits && indent == 3 {
				if v, ok := listKvValue(trimmed, "event"); ok && curNode != nil {
					curNode.Emits = append(curNode.Emits, Emit{Event: unquote(v)})
					curEmit = &curNode.Emits[len(curNode.Emits)-1]
					state = sNodeEmitEntry
					continue
				}
			}

			if (state == sNodeEmitEntry || state == sNodeEmitPayload) && indent == 4 {
				if trimmed == "payload:" {
					state = sNodeEmitPayload
					continue
				}
			}

			if state == sNodeEmitPayload && indent == 5 && curEmit != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curEmit.Fields = append(curEmit.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: unquote(trimmed[idx+1:]),
					})
					continue
				}
			}

			if state == sNodeListens && indent == 3 {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if (state == sNodeListenEntry || state == sNodeListenPayload) && indent == 4 {
				if trimmed == "payload:" {
					state = sNodeListenPayload
					continue
				}
			}

			if state == sNodeListenPayload && indent == 5 && curListen != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curListen.Fields = append(curListen.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: unquote(trimmed[idx+1:]),
					})
					continue
				}
			}

			// Transitions
			if indent == 3 && (state == sNodeEmitEntry || state == sNodeEmitPayload) {
				if v, ok := listKvValue(trimmed, "event"); ok && curNode != nil {
					curNode.Emits = append(curNode.Emits, Emit{Event: unquote(v)})
					curEmit = &curNode.Emits[len(curNode.Emits)-1]
					state = sNodeEmitEntry
					continue
				}
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}
			if indent == 3 && (state == sNodeListenEntry || state == sNodeListenPayload) {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if indent == 2 && curNode != nil && state >= sNodeOwnFiles && state <= sNodeListenPayload {
				state = sNodeBody
				curEmit = nil
				curListen = nil
				switch trimmed {
				case "owns_files:":
					state = sNodeOwnFiles
					continue
				case "emits:":
					state = sNodeEmits
					continue
				case "listens:":
					state = sNodeListens
					continue
				}
			}

			continue
		}

		// ===== ROUTES =====
		if state >= sRoutes && state <= sRouteStepBody {
			if indent == 1 && isListItem(trimmed) {
				afterDash := trimmed[2:]
				var val string
				var found bool
				if strings.HasPrefix(afterDash, "\"on\":") {
					val = strings.TrimSpace(afterDash[5:])
					found = true
				} else if strings.HasPrefix(afterDash, "on:") {
					val = strings.TrimSpace(afterDash[3:])
					found = true
				}
				if found {
					schema.Routes = append(schema.Routes, Route{OnEvent: unquote(val)})
					curRoute = &schema.Routes[len(schema.Routes)-1]
					curStep = nil
					state = sRouteBody
					continue
				}
			}

			if indent == 2 && curRoute != nil && (state == sRouteBody || state == sRouteSteps || state == sRouteStepBody) {
				if trimmed == "steps:" {
					state = sRouteSteps
					continue
				}
				if v, ok := kvValue(trimmed, "emit"); ok {
					curRoute.EmitState = unquote(v)
					state = sRouteBody
					continue
				}
			}

			if (state == sRouteSteps || state == sRouteStepBody) && indent == 3 && curRoute != nil {
				if v, ok := listKvValue(trimmed, "action"); ok {
					curRoute.Steps = append(curRoute.Steps, RouteStep{Action: unquote(v)})
					curStep = &curRoute.Steps[len(curRoute.Steps)-1]
					state = sRouteStepBody
					continue
				}
			}

			if state == sRouteStepBody && indent == 4 && curStep != nil {
				if v, ok := kvValue(trimmed, "table"); ok {
					curStep.Table = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "input"); ok {
					curStep.Input = unquote(v)
					continue
				}
			}

			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading manifest: %w", err)
	}

	return schema, nil
}

// =====================================================================
//  Registry & Payload Type Validator
// =====================================================================

type Registry struct {
	mu          sync.RWMutex
	schema      *SpineSchema
	nodes       map[string]*Node
	routes      map[string][]*Route
	eventEmits  map[string][]PayloadField // event -> expected payload fields
}

func buildRegistry(schema *SpineSchema) *Registry {
	reg := &Registry{
		schema:     schema,
		nodes:      make(map[string]*Node),
		routes:     make(map[string][]*Route),
		eventEmits: make(map[string][]PayloadField),
	}

	for i := range schema.Nodes {
		node := &schema.Nodes[i]
		reg.nodes[node.Name] = node
		for _, e := range node.Emits {
			if len(e.Fields) > 0 {
				reg.eventEmits[e.Event] = e.Fields
			}
		}
	}

	for i := range schema.Routes {
		r := &schema.Routes[i]
		reg.routes[r.OnEvent] = append(reg.routes[r.OnEvent], r)
	}

	return reg
}

func (r *Registry) ValidatePayload(event string, payload map[string]interface{}) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	expectedFields, ok := r.eventEmits[event]
	if !ok {
		return nil // No schema constraints declared for this event
	}

	for _, field := range expectedFields {
		val, exists := payload[field.Name]
		if !exists || val == nil {
			return fmt.Errorf("missing required field '%s' (expected type %s)", field.Name, field.FieldType)
		}

		// Type validation
		t := strings.ToLower(field.FieldType)
		switch t {
		case "string", "str", "text":
			if _, ok := val.(string); !ok {
				return fmt.Errorf("field '%s' must be a string (got %T)", field.Name, val)
			}
		case "number", "float", "int", "integer":
			switch v := val.(type) {
			case float64, float32, int, int64, int32:
				// valid numeric types from JSON
			case string:
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					return fmt.Errorf("field '%s' must be a number (got invalid string '%s')", field.Name, v)
				}
			default:
				return fmt.Errorf("field '%s' must be a number (got %T)", field.Name, val)
			}
		case "bool", "boolean":
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("field '%s' must be a boolean (got %T)", field.Name, val)
			}
		}
	}

	return nil
}

func (r *Registry) GetRoutes(event string) ([]*Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	routes, ok := r.routes[event]
	return routes, ok
}

func (r *Registry) GetSchema() *SpineSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.schema
}

// =====================================================================
//  WebSocket Hub (State Broadcasting Engine)
// =====================================================================

type StateBroadcast struct {
	Type      string                 `json:"type"`      // "state" or "event_ack"
	State     string                 `json:"state,omitempty"`
	Event     string                 `json:"event,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp int64                  `json:"timestamp"`
}

type WsClient struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
}

type Hub struct {
	mu         sync.Mutex
	clients    map[*WsClient]bool
	register   chan *WsClient
	unregister chan *WsClient
}

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*WsClient]bool),
		register:   make(chan *WsClient),
		unregister: make(chan *WsClient),
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("[ws] client connected (total: %d)", count)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.closeOnce.Do(func() { close(client.send) })
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
		Type:      "state",
		State:     stateName,
		Event:     eventName,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client.send <- data:
		default:
			client.closeOnce.Do(func() { close(client.send) })
			delete(h.clients, client)
		}
	}
}

// =====================================================================
//  Bus Engine
// =====================================================================

type Bus struct {
	regMu    sync.RWMutex
	registry *Registry
	db       *sql.DB
	hub      *Hub
}

func newBus(reg *Registry, dbPath string, hub *Hub) *Bus {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		log.Fatalf("[spine-go] cannot open sqlite: %v", err)
	}
	return &Bus{
		registry: reg,
		db:       db,
		hub:      hub,
	}
}

func (b *Bus) updateRegistry(newReg *Registry) {
	b.regMu.Lock()
	defer b.regMu.Unlock()
	b.registry = newReg
}

func (b *Bus) getRegistry() *Registry {
	b.regMu.RLock()
	defer b.regMu.RUnlock()
	return b.registry
}

func (b *Bus) emit(event string, payload map[string]interface{}) (map[string]interface{}, error) {
	reg := b.getRegistry()

	// 1. Validate payload against manifest schema
	if err := reg.ValidatePayload(event, payload); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// 2. Find matching routes
	routes, ok := reg.GetRoutes(event)
	if !ok || len(routes) == 0 {
		return map[string]interface{}{
			"status":         "no_route",
			"event":          event,
			"routes_matched": 0,
		}, nil
	}

	// 3. Process steps & broadcast emitted states
	var emittedStates []string
	for _, route := range routes {
		for _, step := range route.Steps {
			if err := b.execStep(&step, payload); err != nil {
				return nil, fmt.Errorf("step execution failed (action=%s, table=%s): %w", step.Action, step.Table, err)
			}
		}

		if route.EmitState != "" {
			b.hub.BroadcastState(route.EmitState, event, payload)
			emittedStates = append(emittedStates, route.EmitState)
		}
	}

	return map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
		"emitted_states": emittedStates,
	}, nil
}

func (b *Bus) execStep(step *RouteStep, payload map[string]interface{}) error {
	switch step.Action {
	case "db.insert":
		if step.Table != "" {
			return b.dbInsert(step.Table, payload)
		}
	case "db.update":
		if step.Table != "" {
			return b.dbUpdate(step.Table, payload)
		}
	}
	return nil
}

// sanitizeIdent strips anything that isn't alphanumeric or underscore
// to prevent SQL injection through table/column names.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (b *Bus) dbInsert(table string, payload map[string]interface{}) error {
	if len(payload) == 0 {
		return nil
	}

	table = sanitizeIdent(table)
	var colDefs []string
	var colNames []string
	var placeholders []string
	var values []interface{}

	for k, v := range payload {
		safe := sanitizeIdent(k)
		colDefs = append(colDefs, fmt.Sprintf(`"%s" TEXT`, safe))
		colNames = append(colNames, fmt.Sprintf(`"%s"`, safe))
		placeholders = append(placeholders, "?")
		values = append(values, fmt.Sprintf("%v", v))
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	insertSQL := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		table, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))
	if _, err := b.db.Exec(insertSQL, values...); err != nil {
		return fmt.Errorf("insert failed: %w", err)
	}

	return nil
}

func (b *Bus) dbUpdate(table string, payload map[string]interface{}) error {
	if len(payload) < 2 {
		return nil
	}

	table = sanitizeIdent(table)

	// Use 'id' as WHERE key if present, otherwise first key found
	whereKey := ""
	if _, ok := payload["id"]; ok {
		whereKey = "id"
	} else {
		for k := range payload {
			whereKey = k
			break
		}
	}

	var colDefs []string
	var setClauses []string
	var params []interface{}

	for k, v := range payload {
		safe := sanitizeIdent(k)
		colDefs = append(colDefs, fmt.Sprintf(`"%s" TEXT`, safe))
		if k != whereKey {
			setClauses = append(setClauses, fmt.Sprintf(`"%s" = ?`, safe))
			params = append(params, fmt.Sprintf("%v", v))
		}
	}
	params = append(params, fmt.Sprintf("%v", payload[whereKey]))

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	updateSQL := fmt.Sprintf(`UPDATE "%s" SET %s WHERE "%s" = ?`,
		table, strings.Join(setClauses, ", "), sanitizeIdent(whereKey))
	if _, err := b.db.Exec(updateSQL, params...); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	return nil
}

// =====================================================================
//  Hot Reload Watcher
// =====================================================================

func startHotReloadWatcher(spineFile string, bus *Bus) {
	var lastModTime time.Time

	if fi, err := os.Stat(spineFile); err == nil {
		lastModTime = fi.ModTime()
	}

	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			fi, err := os.Stat(spineFile)
			if err != nil {
				continue
			}

			if fi.ModTime().After(lastModTime) {
				lastModTime = fi.ModTime()
				log.Printf("[spine-go] manifest change detected in '%s', reloading...", spineFile)

				newSchema, err := parseSpine(spineFile)
				if err != nil {
					log.Printf("[spine-go] ✗ hot-reload failed: %v", err)
					continue
				}

				newReg := buildRegistry(newSchema)
				bus.updateRegistry(newReg)
				log.Printf("[spine-go] ✓ hot-reloaded successfully: %d nodes, %d routes, %d tables",
					len(newSchema.Nodes), len(newSchema.Routes), len(newSchema.DbTables))
			}
		}
	}()
}

// =====================================================================
//  HTTP & WebSocket Server
// =====================================================================

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for dev/API usage
	},
}

func startServer(bus *Bus, hub *Hub, port string) {
	mux := http.NewServeMux()

	// GET /health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","engine":"spine-go","version":1}`))
	})

	// GET /schema -> Live introspection of the manifest
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		schema := bus.getRegistry().GetSchema()
		json.NewEncoder(w).Encode(schema)
	})

	// POST /emit
	mux.HandleFunc("/emit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte(`{"status":"error","error":"method_not_allowed"}`))
			return
		}

		var body struct {
			Event   string                 `json:"event"`
			Payload map[string]interface{} `json:"payload"`
		}

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":"error","error":"invalid_json_body"}`))
			return
		}

		if body.Event == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":"error","error":"missing_event_name"}`))
			return
		}

		if body.Payload == nil {
			body.Payload = make(map[string]interface{})
		}

		result, err := bus.emit(body.Event, body.Payload)
		w.Header().Set("Content-Type", "application/json")

		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "error",
				"error":  err.Error(),
				"event":  body.Event,
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(result)
	})

	// GET /ws -> WebSockets state push & incoming event trigger
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade error: %v", err)
			return
		}

		client := &WsClient{
			conn: conn,
			send: make(chan []byte, 256),
		}
		hub.register <- client

		// Client writer pump
		go func() {
			defer conn.Close()
			for message := range client.send {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
					break
				}
			}
		}()

		// Client reader pump (allows clients to emit over WS too!)
		go func() {
			defer func() {
				hub.unregister <- client
				conn.Close()
			}()

			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					break
				}

				var req struct {
					Action  string                 `json:"action"`
					Event   string                 `json:"event"`
					Payload map[string]interface{} `json:"payload"`
				}

				if err := json.Unmarshal(message, &req); err == nil && req.Event != "" {
					if req.Payload == nil {
						req.Payload = make(map[string]interface{})
					}
					result, err := bus.emit(req.Event, req.Payload)

					// Send ACK back over websocket
					ack := map[string]interface{}{
						"type":  "event_ack",
						"event": req.Event,
					}
					if err != nil {
						ack["status"] = "error"
						ack["error"] = err.Error()
					} else {
						ack["status"] = "ok"
						ack["result"] = result
					}

					ackBytes, _ := json.Marshal(ack)
					select {
					case client.send <- ackBytes:
					default:
						// send buffer full, drop ack
					}
				}
			}
		}()
	})

	fmt.Println()
	fmt.Println("┌──────────────────────────────────────────┐")
	fmt.Println("│  SPINE v1 Runtime Server (Go)            │")
	fmt.Printf("│  HTTP:  http://0.0.0.0:%-18s │\n", port)
	fmt.Printf("│  WS:    ws://0.0.0.0:%-20s │\n", port+"/ws")
	fmt.Printf("│  Schema:http://0.0.0.0:%-18s │\n", port+"/schema")
	fmt.Println("└──────────────────────────────────────────┘")
	fmt.Println()

	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, mux))
}

// =====================================================================
//  Main Entry Point
// =====================================================================

func main() {
	spineFile := "examples/app.spine"
	port := "8080"
	dbPath := "spine.db"

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--db":
			if i+1 < len(args) {
				dbPath = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				spineFile = args[i]
			}
		}
	}

	fmt.Printf("[spine-go] loading manifest '%s'...\n", spineFile)
	schema, err := parseSpine(spineFile)
	if err != nil {
		log.Fatalf("[spine-go] ✗ failed to parse manifest: %v", err)
	}

	fmt.Printf("[spine-go] loaded: %d nodes, %d routes, %d tables\n",
		len(schema.Nodes), len(schema.Routes), len(schema.DbTables))

	reg := buildRegistry(schema)

	hub := newHub()
	go hub.run()

	bus := newBus(reg, dbPath, hub)
	defer bus.db.Close()

	// Enable hot reloading for app.spine
	startHotReloadWatcher(spineFile, bus)

	startServer(bus, hub, port)
}
