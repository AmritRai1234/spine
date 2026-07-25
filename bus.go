package spine

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"
)

type dbTask struct {
	query  string
	params []interface{}
}

// Bus is the core event dispatch engine. It validates payloads,
// executes route steps, persists to SQLite, and broadcasts state
// changes over WebSocket.
type Bus struct {
	registry unsafe.Pointer // *Registry, swapped atomically
	db       *sql.DB
	hub      *Hub

	// Performance: cache known tables + prepared insert statements
	tableMu    sync.RWMutex
	knownTable map[string]bool
	stmtMu     sync.RWMutex
	stmtCache  map[string]*sql.Stmt

	// High-throughput batch writer
	writeChan chan dbTask
	wg        sync.WaitGroup
}

// NewBus creates a Bus wired to a Registry, SQLite database, and WS hub.
func NewBus(reg *Registry, dbPath string, hub *Hub) (*Bus, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("cannot open sqlite '%s': %w", dbPath, err)
	}

	// Tune connection pool for concurrent goroutines
	db.SetMaxOpenConns(1)    // SQLite only supports 1 writer
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // Keep alive forever

	// Apply performance pragmas
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=268435456",
	}
	for _, p := range pragmas {
		db.Exec(p)
	}

	bus := &Bus{
		db:         db,
		hub:        hub,
		knownTable: make(map[string]bool),
		stmtCache:  make(map[string]*sql.Stmt),
		writeChan:  make(chan dbTask, 100000),
	}
	atomic.StorePointer(&bus.registry, unsafe.Pointer(reg))
	bus.startBatchWriter()
	return bus, nil
}

func (b *Bus) startBatchWriter() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()

		batch := make([]dbTask, 0, 500)

		flush := func() {
			if len(batch) == 0 {
				return
			}
			tx, err := b.db.Begin()
			if err != nil {
				// Fallback execute directly
				for _, task := range batch {
					b.db.Exec(task.query, task.params...)
				}
				batch = batch[:0]
				return
			}

			for _, task := range batch {
				stmt, err := tx.Prepare(task.query)
				if err != nil {
					tx.Exec(task.query, task.params...)
				} else {
					stmt.Exec(task.params...)
					stmt.Close()
				}
			}
			_ = tx.Commit()
			batch = batch[:0]
		}

		for {
			select {
			case task, ok := <-b.writeChan:
				if !ok {
					flush()
					return
				}
				batch = append(batch, task)
				if len(batch) >= 500 {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
}

// Close shuts down batch writer, prepared statements, and the database connection.
func (b *Bus) Close() error {
	close(b.writeChan)
	b.wg.Wait()

	b.stmtMu.Lock()
	for _, stmt := range b.stmtCache {
		stmt.Close()
	}
	b.stmtMu.Unlock()
	return b.db.Close()
}

// UpdateRegistry atomically swaps the registry (used for hot-reload).
func (b *Bus) UpdateRegistry(newReg *Registry) {
	atomic.StorePointer(&b.registry, unsafe.Pointer(newReg))
}

// GetRegistry returns the current registry. Lock-free.
func (b *Bus) GetRegistry() *Registry {
	return (*Registry)(atomic.LoadPointer(&b.registry))
}

// Emit dispatches an event: validates the payload, runs route steps,
// persists to SQLite, and broadcasts any emitted states over WS.
func (b *Bus) Emit(event string, payload map[string]interface{}) (map[string]interface{}, error) {
	return b.EmitWithDepth(event, payload, 0)
}

// EmitWithDepth handles event dispatching with a recursion depth guard for event chaining.
func (b *Bus) EmitWithDepth(event string, payload map[string]interface{}, depth int) (map[string]interface{}, error) {
	if depth > 10 {
		return nil, fmt.Errorf("event chaining max depth (10) exceeded on event '%s'", event)
	}

	reg := b.GetRegistry()

	// Validate payload only on initial emission (depth 0)
	if depth == 0 {
		if err := reg.ValidatePayload(event, payload); err != nil {
			return nil, fmt.Errorf("validation error: %w", err)
		}
	}

	routes, ok := reg.GetRoutes(event)
	if !ok || len(routes) == 0 {
		return map[string]interface{}{
			"status":         "no_route",
			"event":          event,
			"routes_matched": 0,
		}, nil
	}

	var emittedStates []string
	for _, route := range routes {
		for _, step := range route.Steps {
			if err := b.execStep(&step, event, payload); err != nil {
				return nil, fmt.Errorf("step execution failed (action=%s, table=%s): %w", step.Action, step.Table, err)
			}
		}

		if route.EmitState != "" {
			b.hub.BroadcastState(route.EmitState, event, payload)
			emittedStates = append(emittedStates, route.EmitState)

			// Event Chaining: trigger routes matching the emitted state
			if _, hasChained := reg.GetRoutes(route.EmitState); hasChained {
				chainedRes, err := b.EmitWithDepth(route.EmitState, payload, depth+1)
				if err != nil {
					return nil, fmt.Errorf("chained event '%s' failed: %w", route.EmitState, err)
				}
				if chainedStates, ok := chainedRes["emitted_states"].([]string); ok {
					emittedStates = append(emittedStates, chainedStates...)
				}
			}
		}
	}

	return map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
		"emitted_states": emittedStates,
	}, nil
}

func (b *Bus) execStep(step *RouteStep, eventName string, payload map[string]interface{}) error {
	switch step.Action {
	case "db.insert":
		if step.Table != "" {
			return b.dbInsert(step.Table, eventName, payload)
		}
	case "db.update":
		if step.Table != "" {
			return b.dbUpdate(step.Table, eventName, payload)
		}
	case "db.delete":
		if step.Table != "" {
			return b.dbDelete(step.Table, step.Where, eventName, payload)
		}
	case "http.post":
		return b.httpPost(step, eventName, payload)
	case "log.write":
		return b.logWrite(step, eventName, payload)
	}
	return nil
}

// sanitizeIdent strips anything that isn't alphanumeric or underscore.
func sanitizeIdent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ensureTable creates the table only if we haven't seen it before.
func (b *Bus) ensureTable(table string, colDefs []string) error {
	b.tableMu.RLock()
	known := b.knownTable[table]
	b.tableMu.RUnlock()

	if known {
		return nil
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	b.tableMu.Lock()
	b.knownTable[table] = true
	b.tableMu.Unlock()

	return nil
}

func (b *Bus) dbInsert(table string, eventName string, payload map[string]interface{}) error {
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

		// Support template values in string fields
		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
			values = append(values, ResolveVariables(strVal, eventName, payload))
		} else {
			values = append(values, fmt.Sprintf("%v", v))
		}
	}

	if err := b.ensureTable(table, colDefs); err != nil {
		return err
	}

	insertSQL := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		table, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))

	select {
	case b.writeChan <- dbTask{query: insertSQL, params: values}:
	default:
		if _, err := b.db.Exec(insertSQL, values...); err != nil {
			return fmt.Errorf("insert failed: %w", err)
		}
	}

	return nil
}

func (b *Bus) dbUpdate(table string, eventName string, payload map[string]interface{}) error {
	if len(payload) < 2 {
		return nil
	}

	table = sanitizeIdent(table)

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
			if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
				params = append(params, ResolveVariables(strVal, eventName, payload))
			} else {
				params = append(params, fmt.Sprintf("%v", v))
			}
		}
	}
	params = append(params, fmt.Sprintf("%v", payload[whereKey]))

	if err := b.ensureTable(table, colDefs); err != nil {
		return err
	}

	updateSQL := fmt.Sprintf(`UPDATE "%s" SET %s WHERE "%s" = ?`,
		table, strings.Join(setClauses, ", "), sanitizeIdent(whereKey))

	select {
	case b.writeChan <- dbTask{query: updateSQL, params: params}:
	default:
		if _, err := b.db.Exec(updateSQL, params...); err != nil {
			return fmt.Errorf("update failed: %w", err)
		}
	}

	return nil
}

func (b *Bus) dbDelete(table string, whereExpr string, eventName string, payload map[string]interface{}) error {
	table = sanitizeIdent(table)

	if whereExpr == "" {
		if idVal, ok := payload["id"]; ok {
			whereExpr = fmt.Sprintf("id = '%v'", idVal)
		} else {
			return fmt.Errorf("db.delete requires 'where' condition or 'id' in payload")
		}
	} else {
		whereExpr = ResolveVariables(whereExpr, eventName, payload)
	}

	deleteSQL := fmt.Sprintf(`DELETE FROM "%s" WHERE %s`, table, whereExpr)

	select {
	case b.writeChan <- dbTask{query: deleteSQL}:
	default:
		if _, err := b.db.Exec(deleteSQL); err != nil {
			return fmt.Errorf("delete failed: %w", err)
		}
	}

	return nil
}

func (b *Bus) httpPost(step *RouteStep, eventName string, payload map[string]interface{}) error {
	targetURL := ResolveVariables(step.URL, eventName, payload)
	if targetURL == "" {
		return fmt.Errorf("http.post step missing 'url'")
	}

	var bodyBytes []byte
	if step.Input != "" && step.Input != "$event.payload" {
		resolvedInput := ResolveVariables(step.Input, eventName, payload)
		bodyBytes = []byte(resolvedInput)
	} else {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("http.post failed to marshal payload: %w", err)
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(targetURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("http.post request to '%s' failed: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http.post to '%s' returned status %d", targetURL, resp.StatusCode)
	}

	return nil
}

func (b *Bus) logWrite(step *RouteStep, eventName string, payload map[string]interface{}) error {
	msg := step.Message
	if msg == "" {
		msg = "event: $event.name payload: $event.payload"
	}
	resolvedMsg := ResolveVariables(msg, eventName, payload)
	log.Printf("[SPINE LOG] %s", resolvedMsg)
	return nil
}
