package spine

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// Bus is the core event dispatch engine. It validates payloads,
// executes route steps, persists to SQLite, and broadcasts state
// changes over WebSocket.
type Bus struct {
	regMu    sync.RWMutex
	registry *Registry
	db       *sql.DB
	hub      *Hub

	// Performance: cache known tables + prepared insert statements
	tableMu    sync.RWMutex
	knownTable map[string]bool
	stmtMu     sync.RWMutex
	stmtCache  map[string]*sql.Stmt
}

// NewBus creates a Bus wired to a Registry, SQLite database, and WS hub.
func NewBus(reg *Registry, dbPath string, hub *Hub) (*Bus, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("cannot open sqlite '%s': %w", dbPath, err)
	}

	// Tune connection pool for concurrent goroutines
	db.SetMaxOpenConns(1)  // SQLite only supports 1 writer
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

	return &Bus{
		registry:   reg,
		db:         db,
		hub:        hub,
		knownTable: make(map[string]bool),
		stmtCache:  make(map[string]*sql.Stmt),
	}, nil
}

// Close shuts down prepared statements and the database connection.
func (b *Bus) Close() error {
	b.stmtMu.Lock()
	for _, stmt := range b.stmtCache {
		stmt.Close()
	}
	b.stmtMu.Unlock()
	return b.db.Close()
}

// UpdateRegistry atomically swaps the registry (used for hot-reload).
func (b *Bus) UpdateRegistry(newReg *Registry) {
	b.regMu.Lock()
	defer b.regMu.Unlock()
	b.registry = newReg
}

// GetRegistry returns the current registry.
func (b *Bus) GetRegistry() *Registry {
	b.regMu.RLock()
	defer b.regMu.RUnlock()
	return b.registry
}

// Emit dispatches an event: validates the payload, runs route steps,
// persists to SQLite, and broadcasts any emitted states over WS.
func (b *Bus) Emit(event string, payload map[string]interface{}) (map[string]interface{}, error) {
	reg := b.GetRegistry()

	if err := reg.ValidatePayload(event, payload); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
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

	if err := b.ensureTable(table, colDefs); err != nil {
		return err
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

	if err := b.ensureTable(table, colDefs); err != nil {
		return err
	}

	updateSQL := fmt.Sprintf(`UPDATE "%s" SET %s WHERE "%s" = ?`,
		table, strings.Join(setClauses, ", "), sanitizeIdent(whereKey))
	if _, err := b.db.Exec(updateSQL, params...); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	return nil
}
