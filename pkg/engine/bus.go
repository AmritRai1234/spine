package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	_ "turso.tech/database/tursogo"
)

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
	registry atomic.Pointer[manifest.Registry] // *manifest.Registry, swapped atomically
	db       *sql.DB
	hub      *Hub

	// Performance: lock-free known tables + identifier cache + SQL template cache
	knownTable     sync.Map
	identCache     sync.Map // sanitizeIdent result cache
	insertSQLCache sync.Map // "table|col1,col2" → *sqlTemplate
	updateSQLCache sync.Map // "table|whereKey|col1,col2" → *sqlTemplate
	upsertSQLCache sync.Map // "table|conflictKey|col1,col2" → *sqlTemplate
	uniqueIdx      sync.Map // "table|conflictCol" → ensured unique index marker

	// Dialect-specific SQL fragments (placeholder style, auto-PK, table list)
	dialect  *dialect
	auditSQL string

	// In-memory RAM State Cache — lock-free sync.Map eliminates RWMutex convoys
	// under concurrent parallel-route Emit calls
	stateCache sync.Map // stateName string -> map[string]interface{}

	// High-throughput sharded batch writer & Adaptive Optimizer
	writer        *shardedWriter
	wg            sync.WaitGroup
	optimizer     *AdaptiveOptimizer
	customActions sync.Map

	// Notification channel for outbox processor immediate wakeup
	outboxNotify chan struct{}

	// Background workers stop signal
	stopCh chan struct{}

	// Write-path durability counters (exposed on /metrics). All atomic.
	commitFailures uint64 // batch commits that failed after retries (spilled)
	spillWrites    uint64 // writes durably retained in _spine_write_spill
	lostWrites     uint64 // writes dropped because even the spill insert failed
	droppedAudit   uint64 // audit rows dropped due to shard saturation

	// Cron worker state — owned by the single cron goroutine, no lock needed.
	cronLast    map[string]int64 // route key -> last fire (unix seconds)
	cronRunning map[string]bool  // route key -> a fire is in progress

	// Prebuilt dialect-aware SQL used by the durability machinery.
	spillSQL      string // INSERT into _spine_write_spill
	idemInsertSQL string // idempotent INSERT OR IGNORE / ON CONFLICT DO NOTHING
}

// NewBus creates a Bus wired to a Registry, SQLite/Turso/Postgres database, and WS hub.
// The backend is chosen from the DSN: postgres:// or postgresql:// selects
// PostgreSQL (pgx), libsql://, turso://, turso: or *.turso selects Turso,
// anything else is a local SQLite file path.
func NewBus(reg *manifest.Registry, dbPath string, hub *Hub) (*Bus, error) {
	driver := "sqlite3"
	connStr := dbPath
	d := &sqliteDialect // Turso/libSQL is wire-compatible with SQLite syntax

	switch {
	case strings.HasPrefix(dbPath, "postgres://") || strings.HasPrefix(dbPath, "postgresql://"):
		driver = "pgx"
		d = &postgresDialect
	case strings.HasPrefix(dbPath, "libsql://") || strings.HasPrefix(dbPath, "turso://") || strings.HasPrefix(dbPath, "turso:") || strings.HasSuffix(dbPath, ".turso"):
		driver = "turso"
	default:
		connStr = dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_busy_timeout=30000"
	}

	var db *sql.DB
	if driver == "pgx" {
		// QueryExecModeExec (unnamed prepared statements) instead of pgx's default
		// statement cache: spine evolves schemas at runtime (ALTER TABLE ... ADD
		// COLUMN), which invalidates cached SELECT * plans and makes every pooled
		// connection fail with "cached plan must not change result type" (0A000).
		cfg, pcfgErr := pgx.ParseConfig(connStr)
		if pcfgErr != nil {
			return nil, fmt.Errorf("cannot parse postgres DSN '%s': %w", dbPath, pcfgErr)
		}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
		db = stdlib.OpenDB(*cfg)
	} else {
		var openErr error
		db, openErr = sql.Open(driver, connStr)
		if openErr != nil {
			return nil, fmt.Errorf("cannot open database '%s' using driver '%s': %w", dbPath, driver, openErr)
		}
	}

	// Tune connection pool for concurrent readers and single batch writer.
	// The turso driver serializes writes behind one WAL write lock and waits out
	// its 5s busy timeout under contention — a large write pool causes a lock
	// convoy (measured: 100 conns = 3.6K writes/s with multi-second stalls,
	// 10 conns = 18K writes/s with 2ms p99). Cap it and let the sharded
	// batch writer absorb concurrency instead.
	maxConns := 20
	if driver == "turso" {
		maxConns = 4
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(0) // Keep alive forever

	// Apply performance pragmas for local engines
	if driver == "sqlite3" {
		pragmas := []struct {
			sql  string
			name string
		}{
			{"PRAGMA journal_mode=WAL", "journal_mode=WAL"},
			{"PRAGMA synchronous=NORMAL", "synchronous=NORMAL"},
			{"PRAGMA cache_size=-64000", "cache_size=-64000"},
			{"PRAGMA temp_store=MEMORY", "temp_store=MEMORY"},
			{"PRAGMA mmap_size=0", "mmap_size=0"},
			{"PRAGMA page_size=8192", "page_size=8192"},
			{"PRAGMA wal_autocheckpoint=10000", "wal_autocheckpoint=10000"},
		}
		for _, p := range pragmas {
			if _, err := db.Exec(p.sql); err != nil {
				if p.name == "journal_mode=WAL" {
					log.Printf("[spine] ⚠ WARNING: journal_mode=WAL failed (%v) — continuing degraded (NFS/read-only FS?)", err)
				} else {
					return nil, fmt.Errorf("SQLite pragma %s failed: %w", p.name, err)
				}
			}
		}
	}

	bus := &Bus{
		db:           db,
		hub:          hub,
		writer:       newShardedWriter(500000),
		optimizer:    NewAdaptiveOptimizer(),
		outboxNotify: make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
		dialect:      d,
		cronLast:     make(map[string]int64),
		cronRunning:  make(map[string]bool),
	}
	bus.auditSQL = `INSERT INTO "_spine_events" (event_name, payload, emitted_states, created_at) VALUES (` +
		d.placeholder(1) + `, ` + d.placeholder(2) + `, ` + d.placeholder(3) + `, ` + d.placeholder(4) + `)`
	bus.spillSQL = `INSERT INTO "_spine_write_spill" (query, params_json, status, created_at) VALUES (` +
		d.placeholder(1) + `, ` + d.placeholder(2) + `, 'pending', ` + d.placeholder(3) + `)`
	bus.idemInsertSQL = d.idemInsertPrefix + ` "_spine_idem" (key, status, result_json, created_at) VALUES (` +
		d.placeholder(1) + `, 'running', NULL, ` + d.placeholder(2) + `)` + d.idemConflictSuffix
	bus.registry.Store(reg)
	bus.startBatchWriter()
	if err := bus.initEventTable(); err != nil {
		return nil, fmt.Errorf("startup init event table failed: %w", err)
	}
	if err := bus.initOutboxTable(); err != nil {
		return nil, fmt.Errorf("startup init outbox table failed: %w", err)
	}
	if err := bus.initSpillTable(); err != nil {
		return nil, fmt.Errorf("startup init spill table failed: %w", err)
	}
	bus.startSpillDrainer()
	bus.wg.Add(1)
	go func() {
		defer bus.wg.Done()
		bus.processOutboxQueue()
	}()

	// Pre-create tables declared in manifest (including imported sub-manifests)
	if err := bus.EnsureTables(reg.GetSchema().DbTables); err != nil {
		return nil, fmt.Errorf("startup ensure tables failed: %w", err)
	}

	// Adaptive WAL Checkpointing worker (Year 1 Performance):
	// Runs passive/truncate WAL checkpoints during low traffic windows (RPS < 10) to avoid write stalls
	if driver == "sqlite3" {
		bus.startAdaptiveCheckpointing()
	}

	// Scheduled Cron Worker (Year 5 Feature):
	// Triggers routes matching cron: "interval_sec" declarations on schedule
	bus.startScheduledCronWorker()

	// Idempotency table + evictor: durable _spine_idem table with SQL-based cleanup
	if err := bus.initIdempotencyTable(); err != nil {
		return nil, fmt.Errorf("startup init idempotency table failed: %w", err)
	}
	bus.startIdempotencyEvictor()

	return bus, nil
}

// EnsureTables pre-creates tables declared in a schema. Existing tables are
// untouched; missing tables are created. Used at startup and after hot-reload
// so newly declared tables are immediately queryable.
func (b *Bus) EnsureTables(tables []string) error {
	for _, tbl := range tables {
		if err := b.ensureTable(tbl, []string{"created_at TEXT"}); err != nil {
			return fmt.Errorf("ensure table '%s' failed: %w", tbl, err)
		}
	}
	return nil
}

func (b *Bus) startAdaptiveCheckpointing() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				// Check if we are in low-traffic mode (RPS < 10)
				if b.optimizer != nil && b.optimizer.GetRPS() < 10.0 {
					// Run PASSIVE checkpoint (does not block active concurrent writers)
					_, _ = b.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")
				}
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

		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				reg := b.GetRegistry()
				if reg == nil {
					continue
				}

				for i, route := range reg.GetSchema().Routes {
					if route.Cron == "" {
						continue
					}
					// Parse interval (seconds or duration string like '1s', '5s', '1m')
					var intervalSec int = 60
					if dur, err := time.ParseDuration(route.Cron); err == nil {
						intervalSec = int(dur.Seconds())
					} else if sec, err := strconv.Atoi(route.Cron); err == nil {
						intervalSec = sec
					}
					if intervalSec <= 0 {
						continue
					}

					key := fmt.Sprintf("%s\x00%d", route.OnEvent, i)
					now := time.Now()
					nowSec := now.Unix()

					// Phase alignment + once-per-interval guard: prevents
					// double-fires within an interval (e.g. after hot-reload
					// or a slow previous run).
					if nowSec%int64(intervalSec) != 0 {
						continue
					}
					if last, ok := b.cronLast[key]; ok && nowSec-last < int64(intervalSec) {
						continue
					}
					if b.cronRunning[key] {
						continue // overlap guard: previous fire still running
					}

					b.cronLast[key] = nowSec
					b.cronRunning[key] = true
					payload := map[string]interface{}{
						"scheduled_at": now.Format(time.RFC3339),
						"_cron":        route.Cron,
					}
					if _, err := b.Emit(route.OnEvent, payload); err != nil {
						log.Printf("[cron] route '%s' fire failed: %v", route.OnEvent, err)
					}
					delete(b.cronRunning, key)
				}
			}
		}
	}()
}

// initIdempotencyTable creates the _spine_idem table for durable idempotency.
// Created during startup so a missing/unwritable DB aborts startup.
func (b *Bus) initIdempotencyTable() error {
	_, err := b.db.Exec(`CREATE TABLE IF NOT EXISTS "_spine_idem" (
		key TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		result_json TEXT,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("cannot create _spine_idem table: %w", err)
	}
	return nil
}

// startIdempotencyEvictor runs a background sweep every 60 seconds,
// deleting completed _spine_idem entries older than 5 minutes.
// Prevents unbounded growth under long-running deployments.
func (b *Bus) startIdempotencyEvictor() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		const ttl = 5 * time.Minute

		for {
			select {
			case <-b.stopCh:
				return
			case now := <-ticker.C:
				cutoff := now.Add(-ttl).UTC().Format(time.RFC3339)
				_, _ = b.db.Exec(`DELETE FROM "_spine_idem" WHERE status = 'completed' AND created_at < `+b.ph(1), cutoff)
			}
		}
	}()
}

// GetOptimizer returns the active latency optimizer.
func (b *Bus) GetOptimizer() *AdaptiveOptimizer {
	return b.optimizer
}

// GetState retrieves a cached state payload from RAM in sub-microsecond time.
// Lock-free via sync.Map — zero contention under concurrent reads.
func (b *Bus) GetState(stateName string) (map[string]interface{}, bool) {
	val, ok := b.stateCache.Load(stateName)
	if !ok {
		return nil, false
	}
	return val.(map[string]interface{}), true
}

// SetState caches the state payload in RAM. Lock-free via sync.Map.
func (b *Bus) SetState(stateName string, payload map[string]interface{}) {
	b.stateCache.Store(stateName, payload)
}

// startBatchWriter starts a single writer goroutine that drains all shards.
// A single writer avoids SQLite WAL lock contention from concurrent transactions,
// while the sharded input channels still eliminate producer contention.
// Batch accumulation: drains all shards non-blocking before flushing, avoiding
// the old flush-per-receive pattern that created micro-flushes of 1-2 items.
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
			if err := flushBatchWithRetry(b.db, batch); err != nil {
				// Never silently drop: retain the batch durably via the spill
				// table and let the spill drainer replay it once the DB recovers.
				b.spillBatch(batch, err)
			}
			batch = batch[:0]
		}

		// drainAllShards does a non-blocking round-robin drain of all shard channels,
		// accumulating up to targetSize tasks before returning.
		drainAllShards := func(targetSize int) {
			for i := range b.writer.shards {
				for len(batch) < targetSize {
					select {
					case t, ok := <-b.writer.shards[i]:
						if !ok {
							break
						}
						batch = append(batch, t)
						continue
					default:
					}
					break
				}
				if len(batch) >= targetSize {
					return
				}
			}
		}

		// Block on shard[0] to avoid busy-wait, then drain all shards
		for {
			select {
			case <-b.stopCh:
				// Drain all shards before exit
				for i := range b.writer.shards {
					for {
						select {
						case t, ok := <-b.writer.shards[i]:
							if !ok {
								break
							}
							batch = append(batch, t)
							continue
						default:
						}
						break
					}
				}
				flush()
				return
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
				// Accumulate from all shards before flushing
				drainAllShards(targetBatchSize)
				flush()

				// Dynamically adjust ticker when optimizer changes mode
				newInterval := b.optimizer.GetFlushInterval()
				if newInterval != curInterval {
					ticker.Reset(newInterval)
					curInterval = newInterval
				}
			case <-ticker.C:
				// Periodic flush — drain all shards
				drainAllShards(b.optimizer.GetBatchSize())
				flush()
			}
		}
	}()
}

// Close shuts down batch writer and the database connection.
func (b *Bus) Close() error {
	select {
	case <-b.stopCh:
	default:
		close(b.stopCh)
	}

	if b.optimizer != nil {
		b.optimizer.Close()
	}
	b.writer.closeAll()
	b.wg.Wait()
	return b.db.Close()
}

// UpdateRegistry atomically swaps the registry (used for hot-reload).
func (b *Bus) UpdateRegistry(newReg *manifest.Registry) {
	b.registry.Store(newReg)
}

// GetRegistry returns the current registry. Lock-free.
func (b *Bus) GetRegistry() *manifest.Registry {
	return b.registry.Load()
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

	// Idempotency: DB-backed claim protocol (depth == 0 only; chained emissions
	// share the parent key by design and must NOT be deduped).
	var idempotencyKey string
	var claimedKey string // non-empty when this call owns the claim
	if depth == 0 {
		if ik, ok := payload["_idempotency_key"].(string); ok && ik != "" {
			idempotencyKey = ik
			now := time.Now().UTC().Format(time.RFC3339)

			res, err := b.db.Exec(b.idemInsertSQL, idempotencyKey, now)
			if err != nil {
				return nil, fmt.Errorf("idempotency claim failed: %w", err)
			}
			rows, _ := res.RowsAffected()
			if rows == 0 {
				// Key exists — read current status
				var status string
				var resultJSON sql.NullString
				var createdAt string
				err = b.db.QueryRow(`SELECT status, result_json, created_at FROM "_spine_idem" WHERE key = `+b.ph(1), idempotencyKey).Scan(&status, &resultJSON, &createdAt)
				if err != nil {
					return nil, fmt.Errorf("idempotency check failed: %w", err)
				}

				createdTime, parseErr := time.Parse(time.RFC3339, createdAt)
				if parseErr != nil {
					return nil, fmt.Errorf("idempotency: invalid created_at '%s': %w", createdAt, parseErr)
				}

				switch {
				case status == "completed":
					if resultJSON.Valid && resultJSON.String != "" {
						var cachedResult map[string]interface{}
						if jsonErr := json.Unmarshal([]byte(resultJSON.String), &cachedResult); jsonErr == nil {
							cachedResult["idempotent_hit"] = true
							return cachedResult, nil
						}
					}
					return nil, fmt.Errorf("idempotency: corrupted completed result for key '%s'", idempotencyKey)
				case status == "running" && time.Since(createdTime) < 5*time.Minute:
					return nil, fmt.Errorf("idempotency conflict: request with key '%s' is already in-flight", idempotencyKey)
				case status == "running":
					// Stale (>=5 min) — guarded steal
					oldCreated := createdAt
					now2 := time.Now().UTC().Format(time.RFC3339)
					updRes, updErr := b.db.Exec(`UPDATE "_spine_idem" SET status = 'running', created_at = `+b.ph(1)+` WHERE key = `+b.ph(2)+` AND created_at = `+b.ph(3), now2, idempotencyKey, oldCreated)
					if updErr != nil {
						return nil, fmt.Errorf("idempotency steal failed: %w", updErr)
					}
					stolen, _ := updRes.RowsAffected()
					if stolen == 0 {
						// Someone else stole it — re-read final state
						var finalStatus string
						var finalResult sql.NullString
						_ = b.db.QueryRow(`SELECT status, result_json FROM "_spine_idem" WHERE key = `+b.ph(1), idempotencyKey).Scan(&finalStatus, &finalResult)
						if finalStatus == "completed" && finalResult.Valid && finalResult.String != "" {
							var cachedResult map[string]interface{}
							if json.Unmarshal([]byte(finalResult.String), &cachedResult) == nil {
								cachedResult["idempotent_hit"] = true
								return cachedResult, nil
							}
						}
						return nil, fmt.Errorf("idempotency conflict: key '%s' stolen by another request", idempotencyKey)
					}
					claimedKey = idempotencyKey
				default:
					return nil, fmt.Errorf("idempotency: unknown status '%s' for key '%s'", status, idempotencyKey)
				}
			} else {
				claimedKey = idempotencyKey
			}
		}
	}

	// Deferred idempotency cleanup: on success store result; on error or panic delete claim.
	var execSuccess bool
	if claimedKey != "" {
		defer func() {
			if r := recover(); r != nil {
				_, _ = b.db.Exec(`DELETE FROM "_spine_idem" WHERE key = `+b.ph(1), claimedKey)
				panic(r)
			}
			if execSuccess {
				// resultJSON is serialized in the success block below
			} else {
				_, _ = b.db.Exec(`DELETE FROM "_spine_idem" WHERE key = `+b.ph(1), claimedKey)
			}
		}()
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

	// Lazy origPayload: only capture on failure (see handleRouteFailure).
	// Most emits succeed, so this avoids a wasted allocation on the hot path.
	var origPayload map[string]interface{}

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
				// Deep-copy payload for each goroutine to prevent concurrent map write races.
				// Uses JSON round-trip to safely copy nested maps and slices.
				stepPayload := deepCopyPayload(payload)
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
					// Lazy origPayload: capture now since we actually need it
					if origPayload == nil {
						origPayload = make(map[string]interface{}, len(payload))
						for k, v := range payload {
							origPayload[k] = v
						}
					}
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
						// Lazy origPayload: capture now since we actually need it
						if origPayload == nil {
							origPayload = make(map[string]interface{}, len(payload))
							for k, v := range payload {
								origPayload[k] = v
							}
						}
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
		if claimedKey != "" {
			resultJSON, _ := json.Marshal(res)
			_, _ = b.db.Exec(`UPDATE "_spine_idem" SET status = 'completed', result_json = `+b.ph(1)+` WHERE key = `+b.ph(2), string(resultJSON), idempotencyKey)
			execSuccess = true
		}
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
	if step.Action == "http.post" || step.Action == "notify.webhook" {
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

		if err := b.dispatchAction(&compStep, eventName, payload); err != nil {
			log.Printf("[compensation] step '%s' compensating action '%s' failed: %v", step.Action, compStep.Action, err)
		}
	}
}
