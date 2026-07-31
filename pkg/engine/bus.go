package engine

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/AmritRai1234/spine/pkg/manifest"
	_ "github.com/mattn/go-sqlite3"
	_ "turso.tech/database/tursogo"
)

// Pool for emitted states slices to reduce allocs on the hot path
var statesPool = sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 4)
		return &s
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
	knownTable     sync.Map
	identCache     sync.Map // sanitizeIdent result cache
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

	// Idempotency key cache (Year 3 Distributed Reliability):
	// Prevents duplicate execution when the same idempotency key is re-emitted
	idempotencyCache sync.Map // idempotencyKey string -> map[string]interface{} (cached result)
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

	// Adaptive WAL Checkpointing worker (Year 1 Performance):
	// Runs passive/truncate WAL checkpoints during low traffic windows (RPS < 10) to avoid write stalls
	if driver == "sqlite3" {
		bus.startAdaptiveCheckpointing()
	}

	// Scheduled Cron Worker (Year 5 Feature):
	// Triggers routes matching cron: "interval_sec" declarations on schedule
	bus.startScheduledCronWorker()

	return bus, nil
}

func (b *Bus) startAdaptiveCheckpointing() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			// Check if we are in low-traffic mode (RPS < 10)
			if b.optimizer != nil && b.optimizer.GetRPS() < 10.0 {
				// Run PASSIVE checkpoint (does not block active concurrent writers)
				_, _ = b.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")
			}
		}
	}()
}

func (b *Bus) startScheduledCronWorker() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			reg := b.GetRegistry()
			if reg == nil {
				continue
			}

			for _, route := range reg.GetSchema().Routes {
				if route.Cron != "" {
					// Parse interval (seconds or duration string like '1s', '5s', '1m')
					var intervalSec int = 60
					if dur, err := time.ParseDuration(route.Cron); err == nil {
						intervalSec = int(dur.Seconds())
					} else if sec, err := strconv.Atoi(route.Cron); err == nil {
						intervalSec = sec
					}

					if intervalSec > 0 && time.Now().Unix()%int64(intervalSec) == 0 {
						payload := map[string]interface{}{
							"scheduled_at": time.Now().Format(time.RFC3339),
							"_cron":        route.Cron,
						}
						_, _ = b.Emit(route.OnEvent, payload)
					}
				}
			}
		}
	}()
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

	// Idempotency Key Dedup Check (Year 3 Feature)
	var idempotencyKey string
	if ik, ok := payload["_idempotency_key"].(string); ok && ik != "" {
		idempotencyKey = ik
		if cached, found := b.idempotencyCache.Load(idempotencyKey); found {
			res := cached.(map[string]interface{})
			resCopy := make(map[string]interface{}, len(res))
			for k, v := range res {
				resCopy[k] = v
			}
			resCopy["idempotent_hit"] = true
			return resCopy, nil
		}
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
			var succeededSteps []manifest.RouteStep
			for i, step := range route.Steps {
				if err := b.execStep(&step, event, payload); err != nil {
					execErr := fmt.Errorf("step execution failed (action=%s, table=%s): %w", step.Action, step.Table, err)
					// Saga Compensation: rollback all succeeded steps in reverse order
					b.rollbackCompensation(succeededSteps, event, payload)

					onFailure := step.OnFailure
					if onFailure == "" {
						onFailure = route.OnFailure
					}
					if onFailure != "" {
						return b.handleRouteFailure(onFailure, event, payload, origPayload, &step, i, execErr, depth, &emittedStates)
					}
					return nil, execErr
				}
				succeededSteps = append(succeededSteps, step)
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

	res := map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
		"emitted_states": resStates,
	}

	if idempotencyKey != "" {
		b.idempotencyCache.Store(idempotencyKey, res)
	}

	return res, nil
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
	if step.Action == "http.post" {
		b.enqueueOutboxStep(step, step.Action, payload, step.BackoffMs)
	}
	return fmt.Errorf("action %s failed after %d attempts: %w", step.Action, attempts, lastErr)
}

// rollbackCompensation executes compensation actions for previously succeeded steps in reverse order (Saga pattern).
func (b *Bus) rollbackCompensation(succeededSteps []manifest.RouteStep, eventName string, payload map[string]interface{}) {
	for i := len(succeededSteps) - 1; i >= 0; i-- {
		step := succeededSteps[i]
		if step.Compensate == "" {
			continue
		}
		compStep := step
		compStep.Action = step.Compensate

		// If config contains compensate_url, substitute for URL
		if compURL, ok := step.Config["compensate_url"]; ok {
			compStep.URL = compURL
		}
		// If config contains compensate_where, substitute for Where
		if compWhere, ok := step.Config["compensate_where"]; ok {
			compStep.Where = compWhere
		}

		_ = b.dispatchAction(&compStep, eventName, payload)
	}
}
