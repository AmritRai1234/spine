package engine

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/AmritRai1234/spine/pkg/manifest"
	_ "github.com/mattn/go-sqlite3"
	_ "turso.tech/database/tursogo"
)

type dbTask struct {
	query  string
	params []interface{}
}

// numWriteShards controls how many input channels the batch writer drains.
// Sharding eliminates producer contention on a single channel under high concurrency.
const numWriteShards = 8

// shardedWriter distributes write tasks across multiple channels to reduce contention,
// while a single goroutine drains all shards (SQLite serializes writes anyway).
type shardedWriter struct {
	shards [numWriteShards]chan dbTask
	closed uint32 // atomic: 1 = closed
}

func newShardedWriter(bufSize int) *shardedWriter {
	sw := &shardedWriter{}
	perShard := bufSize / numWriteShards
	if perShard < 1024 {
		perShard = 1024
	}
	for i := range sw.shards {
		sw.shards[i] = make(chan dbTask, perShard)
	}
	return sw
}

// submit routes a task to a shard based on the table name hash.
// Returns false if the shard is full or the writer is closed.
func (sw *shardedWriter) submit(table string, task dbTask) (ok bool) {
	if atomic.LoadUint32(&sw.closed) != 0 {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			ok = false // send on closed channel
		}
	}()
	h := fnvHash(table)
	shard := h % numWriteShards
	select {
	case sw.shards[shard] <- task:
		return true
	default:
		return false
	}
}

// submitAny routes a task to the least-loaded shard (for non-table tasks like audit).
// Returns false if all shards are full or the writer is closed.
func (sw *shardedWriter) submitAny(task dbTask) (ok bool) {
	if atomic.LoadUint32(&sw.closed) != 0 {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	// Try each shard starting from 0, pick first with space
	for i := range sw.shards {
		select {
		case sw.shards[i] <- task:
			return true
		default:
		}
	}
	return false
}

func (sw *shardedWriter) closeAll() {
	atomic.StoreUint32(&sw.closed, 1)
	for i := range sw.shards {
		close(sw.shards[i])
	}
}

// fnvHash is a fast non-cryptographic hash for shard routing.
func fnvHash(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// Pool for emitted states slices to reduce allocs on the hot path
var statesPool = sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 4)
		return &s
	},
}

// sqlTemplate holds a pre-built SQL string and the deterministic column order.
// Cached per table+columns fingerprint to eliminate per-call string building.
type sqlTemplate struct {
	sql      string   // e.g. INSERT INTO "x" ("a", "b") VALUES (?, ?)
	colOrder []string // sanitized column names in sorted order
	colDefs  []string // column definitions for ensureTable
}

// Shared HTTP client for webhook steps — enables TCP/TLS connection reuse
var sharedHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Bus is the core event dispatch engine. It validates payloads,
// executes route steps, persists to SQLite/Turso, and broadcasts state
// changes over WebSocket.
type Bus struct {
	registry unsafe.Pointer // *manifest.Registry, swapped atomically
	db       *sql.DB
	hub      *Hub

	// Performance: lock-free known tables + identifier cache + SQL template cache
	knownTable    sync.Map
	identCache    sync.Map // sanitizeIdent result cache
	insertSQLCache sync.Map // "table|col1,col2" → *sqlTemplate
	updateSQLCache sync.Map // "table|whereKey|col1,col2" → *sqlTemplate
	upsertSQLCache sync.Map // "table|conflictKey|col1,col2" → *sqlTemplate

	// In-memory RAM State Cache
	stateMu    sync.RWMutex
	stateCache map[string]map[string]interface{}

	// High-throughput sharded batch writer & Adaptive Optimizer
	writer        *shardedWriter
	wg            sync.WaitGroup
	optimizer     *AdaptiveOptimizer
	customActions sync.Map

	// Notification channel for outbox processor immediate wakeup
	outboxNotify chan struct{}
}

// NewBus creates a Bus wired to a Registry, SQLite/Turso database, and WS hub.
func NewBus(reg *manifest.Registry, dbPath string, hub *Hub) (*Bus, error) {
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
			"PRAGMA mmap_size=0",              // Regular I/O — avoids TLB shootdown & page faults on write-heavy workloads
			"PRAGMA page_size=8192",           // 8KB pages — better I/O alignment for modern SSDs
			"PRAGMA wal_autocheckpoint=10000",
		}
		for _, p := range pragmas {
			db.Exec(p)
		}
	}

	bus := &Bus{
		db:           db,
		hub:          hub,
		stateCache:   make(map[string]map[string]interface{}),
		writer:       newShardedWriter(500000),
		optimizer:    NewAdaptiveOptimizer(),
		outboxNotify: make(chan struct{}, 1),
	}
	atomic.StorePointer(&bus.registry, unsafe.Pointer(reg))
	bus.startBatchWriter()
	bus.initEventTable()
	bus.initOutboxTable()
	go bus.processOutboxQueue()

	// Pre-create tables declared in manifest (including imported sub-manifests)
	for _, tbl := range reg.GetSchema().DbTables {
		_ = bus.ensureTable(tbl, []string{"created_at TEXT"})
	}

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
		curInterval := b.optimizer.GetFlushInterval()
		ticker := time.NewTicker(curInterval)
		defer ticker.Stop()

		batch := make([]dbTask, 0, 1000)

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

			// Prepared statement cache within this transaction.
			// Reusing stmts eliminates sqlite3_prepare_v2 overhead (31% of CPU).
			stmtCache := make(map[string]*sql.Stmt, 8)
			defer func() {
				for _, s := range stmtCache {
					s.Close()
				}
			}()

			for _, task := range batch {
				stmt, ok := stmtCache[task.query]
				if !ok {
					stmt, err = tx.Prepare(task.query)
					if err != nil {
						// Fallback: direct exec
						if _, execErr := tx.Exec(task.query, task.params...); execErr != nil {
							log.Printf("[batch] exec error: %v", execErr)
						}
						continue
					}
					stmtCache[task.query] = stmt
				}
				if _, err := stmt.Exec(task.params...); err != nil {
					log.Printf("[batch] exec error: %v", err)
				}
			}
			_ = tx.Commit()
			batch = batch[:0]
		}

		// drainShards does a non-blocking round-robin drain of all shard channels
		drainShards := func(targetSize int) bool {
			drained := false
			for i := range b.writer.shards {
				for len(batch) < targetSize {
					select {
					case t, ok := <-b.writer.shards[i]:
						if !ok {
							// This shard is closed, move to next
							break
						}
						batch = append(batch, t)
						drained = true
						continue
					default:
					}
					break // default or closed: move to next shard
				}
				if len(batch) >= targetSize {
					return true
				}
			}
			return drained
		}

		// Block on shard[0] to avoid busy-wait, then drain all shards
		for {
			select {
			case task, ok := <-b.writer.shards[0]:
				if !ok {
					// Channel closed — drain remaining shards and exit
					for i := 1; i < numWriteShards; i++ {
						for t := range b.writer.shards[i] {
							batch = append(batch, t)
						}
					}
					flush()
					return
				}
				batch = append(batch, task)
				targetBatchSize := b.optimizer.GetBatchSize()
				// Drain all shards opportunistically
				drainShards(targetBatchSize)
				flush()

				// Dynamically adjust ticker when optimizer changes mode
				newInterval := b.optimizer.GetFlushInterval()
				if newInterval != curInterval {
					ticker.Reset(newInterval)
					curInterval = newInterval
				}
			case <-ticker.C:
				// Periodic flush — drain all shards
				drainShards(b.optimizer.GetBatchSize())
				flush()
			}
		}
	}()
}

// Close shuts down batch writer and the database connection.
func (b *Bus) Close() error {
	if b.optimizer != nil {
		b.optimizer.Close()
	}
	b.writer.closeAll()
	b.wg.Wait()
	return b.db.Close()
}

// UpdateRegistry atomically swaps the registry (used for hot-reload).
func (b *Bus) UpdateRegistry(newReg *manifest.Registry) {
	atomic.StorePointer(&b.registry, unsafe.Pointer(newReg))
}

// GetRegistry returns the current registry. Lock-free.
func (b *Bus) GetRegistry() *manifest.Registry {
	return (*manifest.Registry)(atomic.LoadPointer(&b.registry))
}

// DB returns the underlying *sql.DB connection for advanced queries.
// Shares the same connection pool and WAL visibility as Spine's internal writes.
func (b *Bus) DB() *sql.DB {
	return b.db
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

	// Capture initial trigger payload copy to guarantee preservation in on_failure handlers
	origPayload := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		origPayload[k] = v
	}

	// Pool the emittedStates slice to reduce GC pressure on high-throughput paths
	statesPtr := statesPool.Get().(*[]string)
	emittedStates := (*statesPtr)[:0]
	defer func() {
		*statesPtr = emittedStates[:0]
		statesPool.Put(statesPtr)
	}()

	for _, route := range routes {
		if route.IfCondition != "" && !EvaluateCondition(route.IfCondition, event, payload) {
			continue
		}

		if route.Parallel {
			var wg sync.WaitGroup
			var errMu sync.Mutex
			var stepErr error
			var failedStep *manifest.RouteStep
			var failedIdx int

			for i := range route.Steps {
				wg.Add(1)
				// Deep-copy payload map for each goroutine to prevent concurrent map write races
				stepPayload := make(map[string]interface{}, len(payload))
				for k, v := range payload {
					stepPayload[k] = v
				}
				idx := i
				go func(s *manifest.RouteStep, p map[string]interface{}, stepIndex int) {
					defer wg.Done()
					if err := b.execStep(s, event, p); err != nil {
						errMu.Lock()
						if stepErr == nil {
							stepErr = err
							failedStep = s
							failedIdx = stepIndex
						}
						errMu.Unlock()
					}
				}(&route.Steps[i], stepPayload, idx)
			}
			wg.Wait()
			if stepErr != nil {
				onFailure := route.OnFailure
				if failedStep != nil && failedStep.OnFailure != "" {
					onFailure = failedStep.OnFailure
				}
				if onFailure != "" {
					return b.handleRouteFailure(onFailure, event, payload, origPayload, failedStep, failedIdx, stepErr, depth, &emittedStates)
				}
				return nil, fmt.Errorf("parallel step execution failed: %w", stepErr)
			}
		} else {
			for i, step := range route.Steps {
				if err := b.execStep(&step, event, payload); err != nil {
					execErr := fmt.Errorf("step execution failed (action=%s, table=%s): %w", step.Action, step.Table, err)
					onFailure := step.OnFailure
					if onFailure == "" {
						onFailure = route.OnFailure
					}
					if onFailure != "" {
						return b.handleRouteFailure(onFailure, event, payload, origPayload, &step, i, execErr, depth, &emittedStates)
					}
					return nil, execErr
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

	// Copy emittedStates before returning (original goes back to pool)
	resStates := make([]string, len(emittedStates))
	copy(resStates, emittedStates)

	return map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
		"emitted_states": resStates,
	}, nil
}

func (b *Bus) handleRouteFailure(onFailureState string, event string, currentPayload map[string]interface{}, origPayload map[string]interface{}, step *manifest.RouteStep, stepIdx int, stepErr error, depth int, emittedStates *[]string) (map[string]interface{}, error) {
	errPayload := make(map[string]interface{}, len(currentPayload)+len(origPayload)+5)
	// Preserve all keys from current payload state
	for k, v := range currentPayload {
		errPayload[k] = v
	}
	// Restore any original keys that might have been lost/deleted during failed step execution
	for k, v := range origPayload {
		if _, exists := errPayload[k]; !exists {
			errPayload[k] = v
		}
	}
	errPayload["error"] = stepErr.Error()
	errPayload["failed_event"] = event
	if step != nil {
		errPayload["failed_action"] = step.Action
	}

	// Build immutable _error_context object
	origCopy := make(map[string]interface{}, len(origPayload))
	for k, v := range origPayload {
		origCopy[k] = v
	}
	errCtx := map[string]interface{}{
		"original_payload": origCopy,
		"failed_event":     event,
		"step_index":       stepIdx,
		"error":            stepErr.Error(),
		"timestamp":        time.Now().UTC().Format(time.RFC3339Nano),
	}
	if step != nil {
		errCtx["failed_action"] = step.Action
		if step.Table != "" {
			errCtx["failed_table"] = step.Table
		}
	}
	errPayload["_error_context"] = errCtx

	b.SetState(onFailureState, errPayload)
	b.hub.BroadcastState(onFailureState, event, errPayload)
	*emittedStates = append(*emittedStates, onFailureState)

	reg := b.GetRegistry()
	if _, hasChained := reg.GetRoutes(onFailureState); hasChained {
		chainedRes, _ := b.EmitWithDepth(onFailureState, errPayload, depth+1)
		if chainedRes != nil {
			if chainedStates, ok := chainedRes["emitted_states"].([]string); ok {
				*emittedStates = append(*emittedStates, chainedStates...)
			}
		}
	}

	resStates := make([]string, len(*emittedStates))
	copy(resStates, *emittedStates)

	return map[string]interface{}{
		"status":         "error",
		"error":          stepErr.Error(),
		"event":          event,
		"emitted_states": resStates,
	}, stepErr
}

func (b *Bus) execStep(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
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

// ActionFunc represents a custom Go plugin action handler function.
type ActionFunc func(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error

// RegisterAction registers a custom Go action handler plugin.
func (b *Bus) RegisterAction(name string, fn ActionFunc) {
	b.customActions.Store(name, fn)
}

func (b *Bus) dispatchAction(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	switch step.Action {
	case "db.insert":
		if step.Table != "" {
			return b.dbInsert(step.Table, eventName, payload)
		}
	case "db.update":
		if step.Table != "" {
			return b.dbUpdate(step.Table, eventName, payload)
		}
	case "db.upsert":
		if step.Table != "" {
			key := step.Config["key"]
			if key == "" {
				key = "id"
			}
			return b.dbUpsert(step.Table, key, eventName, payload)
		}
	case "db.delete":
		if step.Table != "" {
			return b.dbDelete(step.Table, step.Where, eventName, payload)
		}
	case "set":
		return b.setFields(step, eventName, payload)
	case "http.post":
		return b.httpPost(step, eventName, payload)
	case "log.write":
		return b.logWrite(step, eventName, payload)
	case "queue.publish":
		return b.queuePublish(step, eventName, payload)
	default:
		if val, ok := b.customActions.Load(step.Action); ok {
			if fn, isFn := val.(ActionFunc); isFn {
				return fn(step, eventName, payload)
			}
		}
	}
	return nil
}

func (b *Bus) queuePublish(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	topic := step.Table
	if topic == "" {
		topic = eventName
	}
	b.hub.BroadcastState(topic, eventName, payload)
	return nil
}

// sanitizeIdent strips anything that isn't alphanumeric or underscore.
// Results are cached in identCache sync.Map for repeated column/table names.
func (b *Bus) sanitizeIdentCached(s string) string {
	if cached, ok := b.identCache.Load(s); ok {
		return cached.(string)
	}
	result := sanitizeIdent(s)
	b.identCache.Store(s, result)
	return result
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

// ensureTable creates the table and auto-generates column indexes or adds missing columns.
// Uses knownTable sync.Map to skip SQL when the same column set has been seen before.
func (b *Bus) ensureTable(table string, colDefs []string) error {
	// Build a fingerprint of the column definitions for this call
	colKey := table + "|" + strings.Join(colDefs, ",")

	// Fast path: this exact table+columns combo already ensured — skip all SQL
	if _, known := b.knownTable.Load(colKey); known {
		return nil
	}

	// Use _spine_id as the internal auto-increment PK to avoid colliding
	// with user payload fields named "id" (which would cause datatype mismatch).
	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (_spine_id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	for _, colDef := range colDefs {
		parts := strings.Fields(colDef)
		if len(parts) > 0 {
			colName := strings.Trim(parts[0], `"`)
			if colName != "_spine_id" {
				alterSQL := fmt.Sprintf(`ALTER TABLE "%s" ADD COLUMN %s`, table, colDef)
				_, _ = b.db.Exec(alterSQL)
			}

			if strings.HasSuffix(colName, "_id") || colName == "id" || colName == "email" || colName == "status" || colName == "state" {
				idxSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "idx_%s_%s" ON "%s"("%s")`,
					table, colName, table, colName)
				_, _ = b.db.Exec(idxSQL)
			}
		}
	}

	b.knownTable.Store(colKey, true)
	return nil
}

func normalizeParam(v interface{}, eventName string, payload map[string]interface{}) interface{} {
	if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
		return ResolveVariables(strVal, eventName, payload)
	}
	switch val := v.(type) {
	case map[string]interface{}, []interface{}, []string, map[string]string:
		bytes, err := json.Marshal(val)
		if err == nil {
			return string(bytes)
		}
	}
	return v
}

func (b *Bus) dbInsert(table string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n == 0 {
		return nil
	}

	table = b.sanitizeIdentCached(table)

	// Deterministic key ordering: sort payload keys for stable SQL + caching
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build cache fingerprint from sorted sanitized column names
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.insertSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions from manifest field types
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		// Ensure table exists (only on first encounter of this fingerprint)
		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Build SQL string once
		var sb strings.Builder
		sb.Grow(64 + len(table) + n*12)
		sb.WriteString(`INSERT INTO "`)
		sb.WriteString(table)
		sb.WriteString(`" (`)
		for i, safe := range sanitizedKeys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteByte('"')
		}
		sb.WriteString(") VALUES (")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('?')
		}
		sb.WriteByte(')')

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.insertSQLCache.Store(fingerprint, tmpl)
	}

	// Build values in deterministic column order
	values := make([]interface{}, n)
	for i, k := range keys {
		values[i] = normalizeParam(payload[k], eventName, payload)
	}



	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: values}) {
		// All shards full — async overflow
		go func(t dbTask) {
			b.writer.submit(table, t)
		}(dbTask{query: tmpl.sql, params: values})
	}

	return nil
}

func (b *Bus) dbUpdate(table string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n < 2 {
		return nil
	}

	table = b.sanitizeIdentCached(table)

	// Deterministic where-key: use "id" if present, otherwise alphabetically first key
	whereKey := ""
	if _, ok := payload["id"]; ok {
		whereKey = "id"
	}

	// Deterministic key ordering: sort payload keys for stable SQL + caching
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// If no "id", use alphabetically first key as deterministic where-key
	if whereKey == "" {
		whereKey = keys[0]
	}

	safeWhereKey := b.sanitizeIdentCached(whereKey)

	// Build cache fingerprint from table + whereKey + sorted sanitized column names
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + len(safeWhereKey) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	fpBuf.WriteString(safeWhereKey)
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.updateSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions from manifest field types
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		// Ensure table exists (only on first encounter of this fingerprint)
		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Build SQL string once: UPDATE "table" SET "col1" = ?, "col2" = ? WHERE "whereKey" = ?
		var sb strings.Builder
		sb.Grow(64 + len(table) + n*12)
		sb.WriteString(`UPDATE "`)
		sb.WriteString(table)
		sb.WriteString(`" SET `)
		first := true
		for _, safe := range sanitizedKeys {
			if safe == safeWhereKey {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteString(`" = ?`)
			first = false
		}
		sb.WriteString(` WHERE "`)
		sb.WriteString(safeWhereKey)
		sb.WriteString(`" = ?`)

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.updateSQLCache.Store(fingerprint, tmpl)
	}

	// Build params in deterministic column order (SET values first, then WHERE value)
	params := make([]interface{}, 0, n)
	for _, k := range keys {
		if k == whereKey {
			continue
		}
		params = append(params, normalizeParam(payload[k], eventName, payload))
	}
	params = append(params, normalizeParam(payload[whereKey], eventName, payload))



	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: params}) {
		if _, err := b.db.Exec(tmpl.sql, params...); err != nil {
			return fmt.Errorf("update failed: %w", err)
		}
	}

	return nil
}

func (b *Bus) dbDelete(table string, whereExpr string, eventName string, payload map[string]interface{}) error {
	table = b.sanitizeIdentCached(table)

	var deleteSQL string
	var params []interface{}

	if whereExpr == "" {
		if idVal, ok := payload["id"]; ok {
			// Parameterized query prevents SQL injection via payload values
			deleteSQL = fmt.Sprintf(`DELETE FROM "%s" WHERE "id" = ?`, table)
			params = []interface{}{idVal}
		} else {
			return fmt.Errorf("db.delete requires 'where' condition or 'id' in payload")
		}
	} else {
		whereExpr = ResolveVariables(whereExpr, eventName, payload)
		deleteSQL = fmt.Sprintf(`DELETE FROM "%s" WHERE %s`, table, whereExpr)
	}

	if !b.writer.submit(table, dbTask{query: deleteSQL, params: params}) {
		if _, err := b.db.Exec(deleteSQL, params...); err != nil {
			return fmt.Errorf("delete failed: %w", err)
		}
	}

	return nil
}

func (b *Bus) httpPost(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
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

	resp, err := sharedHTTPClient.Post(targetURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("http.post request to '%s' failed: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http.post to '%s' returned status %d", targetURL, resp.StatusCode)
	}

	return nil
}

func (b *Bus) logWrite(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	msg := step.Message
	if msg == "" {
		msg = "event: $event.name payload: $event.payload"
	}
	resolvedMsg := ResolveVariables(msg, eventName, payload)
	log.Printf("[SPINE LOG] %s", resolvedMsg)
	return nil
}

// sqliteType maps a manifest field type to the corresponding SQLite column type.
func sqliteType(fieldType string) string {
	switch strings.ToLower(fieldType) {
	case "number", "float":
		return "REAL"
	case "int", "integer":
		return "INTEGER"
	case "boolean", "bool":
		return "INTEGER"
	default:
		return "TEXT"
	}
}

// setFields merges key-value pairs from step.Config into the event payload.
// Values are resolved through ResolveVariables, so $uuid, $now, $event.payload.X work.
func (b *Bus) setFields(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	for key, val := range step.Config {
		resolved := ResolveVariables(val, eventName, payload)
		payload[key] = resolved
	}
	return nil
}

func (b *Bus) dbUpsert(table string, conflictKey string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n == 0 {
		return nil
	}

	if _, exists := payload[conflictKey]; !exists {
		return fmt.Errorf("db.upsert failed: conflict key '%s' not present in payload", conflictKey)
	}

	table = b.sanitizeIdentCached(table)
	safeConflictKey := b.sanitizeIdentCached(conflictKey)

	// Deterministic key ordering
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build cache fingerprint
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + len(safeConflictKey) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	fpBuf.WriteString(safeConflictKey)
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.upsertSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Create unique index on conflict key if not exists
		idxSQL := fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS "uq_%s_%s" ON "%s"("%s")`,
			table, safeConflictKey, table, safeConflictKey)
		_, _ = b.db.Exec(idxSQL)

		// Build: INSERT INTO "t" ("a","b") VALUES (?,?) ON CONFLICT("key") DO UPDATE SET "a"=excluded."a"
		var sb strings.Builder
		sb.Grow(128 + len(table) + n*24)
		sb.WriteString(`INSERT INTO "`)
		sb.WriteString(table)
		sb.WriteString(`" (`)
		for i, safe := range sanitizedKeys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteByte('"')
		}
		sb.WriteString(") VALUES (")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('?')
		}
		sb.WriteString(`) ON CONFLICT("`)
		sb.WriteString(safeConflictKey)
		sb.WriteString(`") DO UPDATE SET `)
		first := true
		for _, safe := range sanitizedKeys {
			if safe == safeConflictKey {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteString(`" = excluded."`)
			sb.WriteString(safe)
			sb.WriteByte('"')
			first = false
		}

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.upsertSQLCache.Store(fingerprint, tmpl)
	}

	// Build values in deterministic column order
	values := make([]interface{}, n)
	for i, k := range keys {
		values[i] = normalizeParam(payload[k], eventName, payload)
	}



	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: values}) {
		go func(t dbTask) {
			b.writer.submit(table, t)
		}(dbTask{query: tmpl.sql, params: values})
	}

	return nil
}
