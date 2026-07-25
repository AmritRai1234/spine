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
	_ "turso.tech/database/tursogo"
)

type dbTask struct {
	query  string
	params []interface{}
}

// Bus is the core event dispatch engine. It validates payloads,
// executes route steps, persists to SQLite/Turso, and broadcasts state
// changes over WebSocket.
type Bus struct {
	registry unsafe.Pointer // *Registry, swapped atomically
	db       *sql.DB
	hub      *Hub

	// Performance: lock-free known tables + prepared insert statements
	knownTable sync.Map
	stmtMu     sync.RWMutex
	stmtCache  map[string]*sql.Stmt

	// In-memory RAM State Cache
	stateMu    sync.RWMutex
	stateCache map[string]map[string]interface{}

	// High-throughput batch writer & Adaptive Optimizer
	writeChan chan dbTask
	wg        sync.WaitGroup
	optimizer *AdaptiveOptimizer
}

// NewBus creates a Bus wired to a Registry, SQLite/Turso database, and WS hub.
func NewBus(reg *Registry, dbPath string, hub *Hub) (*Bus, error) {
	driver := "sqlite3"
	connStr := dbPath

	if strings.HasPrefix(dbPath, "libsql://") || strings.HasPrefix(dbPath, "turso://") || strings.HasPrefix(dbPath, "turso:") {
		driver = "turso"
	} else if strings.HasSuffix(dbPath, ".turso") {
		driver = "turso"
	} else {
		connStr = dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_busy_timeout=30000"
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("cannot open database '%s' using driver '%s': %w", dbPath, driver, err)
	}

	// Tune connection pool for concurrent readers and single batch writer
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(0) // Keep alive forever

	// Apply performance pragmas for local engines
	if driver == "sqlite3" {
		pragmas := []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=NORMAL",
			"PRAGMA cache_size=-64000",
			"PRAGMA temp_store=MEMORY",
			"PRAGMA mmap_size=268435456",
			"PRAGMA wal_autocheckpoint=10000",
		}
		for _, p := range pragmas {
			db.Exec(p)
		}
	}

	bus := &Bus{
		db:         db,
		hub:        hub,
		stmtCache:  make(map[string]*sql.Stmt),
		stateCache: make(map[string]map[string]interface{}),
		writeChan:  make(chan dbTask, 500000),
		optimizer:  NewAdaptiveOptimizer(),
	}
	atomic.StorePointer(&bus.registry, unsafe.Pointer(reg))
	bus.startBatchWriter()
	bus.initEventTable()
	return bus, nil
}

// GetOptimizer returns the active latency optimizer.
func (b *Bus) GetOptimizer() *AdaptiveOptimizer {
	return b.optimizer
}

// GetState retrieves a cached state payload from RAM in sub-microsecond time.
func (b *Bus) GetState(stateName string) (map[string]interface{}, bool) {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	val, ok := b.stateCache[stateName]
	return val, ok
}

// SetState caches the state payload in RAM.
func (b *Bus) SetState(stateName string, payload map[string]interface{}) {
	b.stateMu.Lock()
	b.stateCache[stateName] = payload
	b.stateMu.Unlock()
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
				if _, err := tx.Exec(task.query, task.params...); err != nil {
					// Silent ignore or fallback
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
				targetBatchSize := b.optimizer.GetBatchSize()
				// Opportunistic non-blocking drain loop
				for len(batch) < targetBatchSize {
					select {
					case t, ok := <-b.writeChan:
						if !ok {
							flush()
							return
						}
						batch = append(batch, t)
					default:
						goto FLUSH
					}
				}
			FLUSH:
				flush()
			case <-ticker.C:
				flush()
			}
		}
	}()
}

// Close shuts down batch writer, prepared statements, and the database connection.
func (b *Bus) Close() error {
	if b.optimizer != nil {
		b.optimizer.Close()
	}
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

	b.optimizer.RecordRequest()

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
		if route.IfCondition != "" && !EvaluateCondition(route.IfCondition, event, payload) {
			continue
		}

		if route.Parallel {
			var wg sync.WaitGroup
			var errMu sync.Mutex
			var stepErr error
			for i := range route.Steps {
				wg.Add(1)
				go func(s *RouteStep) {
					defer wg.Done()
					if err := b.execStep(s, event, payload); err != nil {
						errMu.Lock()
						if stepErr == nil {
							stepErr = err
						}
						errMu.Unlock()
					}
				}(&route.Steps[i])
			}
			wg.Wait()
			if stepErr != nil {
				return nil, fmt.Errorf("parallel step execution failed: %w", stepErr)
			}
		} else {
			for _, step := range route.Steps {
				if err := b.execStep(&step, event, payload); err != nil {
					return nil, fmt.Errorf("step execution failed (action=%s, table=%s): %w", step.Action, step.Table, err)
				}
			}
		}

		if route.EmitState != "" {
			b.SetState(route.EmitState, payload)
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

	if depth == 0 {
		b.logEventAudit(event, payload, emittedStates)
	}

	return map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
		"emitted_states": emittedStates,
	}, nil
}

func (b *Bus) execStep(step *RouteStep, eventName string, payload map[string]interface{}) error {
	if step.IfCondition != "" && !EvaluateCondition(step.IfCondition, eventName, payload) {
		return nil
	}

	attempts := step.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = b.dispatchAction(step, eventName, payload)
		if lastErr == nil {
			return nil
		}
		if attempt < attempts && step.BackoffMs > 0 {
			time.Sleep(time.Duration(step.BackoffMs) * time.Millisecond)
		}
	}
	return fmt.Errorf("action %s failed after %d attempts: %w", step.Action, attempts, lastErr)
}

func (b *Bus) dispatchAction(step *RouteStep, eventName string, payload map[string]interface{}) error {
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

// ensureTable creates the table and auto-generates column indexes if not seen before.
func (b *Bus) ensureTable(table string, colDefs []string) error {
	if _, ok := b.knownTable.Load(table); ok {
		return nil
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	// Auto-index key lookup columns (e.g., email, user_id, project_id, status)
	for _, colDef := range colDefs {
		parts := strings.Fields(colDef)
		if len(parts) > 0 {
			colName := strings.Trim(parts[0], `"`)
			if strings.HasSuffix(colName, "_id") || colName == "email" || colName == "status" || colName == "state" {
				idxSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "idx_%s_%s" ON "%s"("%s")`,
					table, colName, table, colName)
				_, _ = b.db.Exec(idxSQL)
			}
		}
	}

	b.knownTable.Store(table, true)
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
			values = append(values, v)
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
		// Channel buffer backup queue
		go func(t dbTask) {
			b.writeChan <- t
		}(dbTask{query: insertSQL, params: values})
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
				params = append(params, v)
			}
		}
	}
	params = append(params, payload[whereKey])

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
