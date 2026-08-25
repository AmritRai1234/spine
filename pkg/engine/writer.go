package engine

import (
	"database/sql"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

type dbTask struct {
	query  string
	params []interface{}
}

// numWriteShards is computed at init based on available CPU cores.
// Capped between 4 and 16 to balance parallelism and SQLite WAL contention.
var numWriteShards = computeShardCount()

func computeShardCount() int {
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 16 {
		n = 16
	}
	return n
}

// shardedWriter distributes write tasks across multiple channels.
// Each shard is drained by its own goroutine — transaction preparation
// (building stmts, marshaling params) runs in parallel across cores,
// while SQLite serializes the actual WAL writes.
type shardedWriter struct {
	shards []chan dbTask
	closed uint32 // atomic: 1 = closed
}

func newShardedWriter(bufSize int) *shardedWriter {
	sw := &shardedWriter{
		shards: make([]chan dbTask, numWriteShards),
	}
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
	shard := h % uint32(numWriteShards)
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case sw.shards[shard] <- task:
			return true
		default:
			if attempt < 2 {
				time.Sleep(100 * time.Microsecond)
			}
		}
	}
	return false
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
	for attempt := 0; attempt < 3; attempt++ {
		for i := range sw.shards {
			select {
			case sw.shards[i] <- task:
				return true
			default:
			}
		}
		if attempt < 2 {
			time.Sleep(100 * time.Microsecond)
		}
	}
	return false
}

func (sw *shardedWriter) closeAll() {
	// CAS on closed: only the first caller closes the channels; subsequent
	// calls (e.g. double Engine.Close) are no-ops.
	if !atomic.CompareAndSwapUint32(&sw.closed, 0, 1) {
		return
	}
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

// execWithRetry retries a SQL statement up to 5 times on lock/busy errors.
func execWithRetry(db *sql.DB, query string, params ...interface{}) error {
	_, err := execWithRetryResult(db, query, params...)
	return err
}

// execWithRetryResult is execWithRetry for statements whose sql.Result matters
// (e.g. RowsAffected checks in db.adjust).
func execWithRetryResult(db *sql.DB, query string, params ...interface{}) (sql.Result, error) {
	var err error
	var res sql.Result
	for attempt := 0; attempt < 5; attempt++ {
		res, err = db.Exec(query, params...)
		if err == nil {
			return res, nil
		}
		errStr := err.Error()
		if strings.Contains(errStr, "locked") || strings.Contains(errStr, "busy") {
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			continue
		}
		return nil, err
	}
	return nil, err
}

// isLockBusyErr reports whether err is a SQLite/Turso lock or busy failure
// ("database is locked", "database table is locked", "database is busy").
// These are the only errors safe to retry at the whole-batch level: under WAL
// a busy COMMIT means the transaction was NOT applied, so re-executing the
// batch cannot duplicate rows.
func isLockBusyErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "locked") || strings.Contains(s, "busy")
}

// beginWithRetry begins a transaction, retrying on lock/busy failures with
// exponential backoff. Only Begin is retried here — nothing has executed yet,
// so a retry is always safe.
func beginWithRetry(db *sql.DB) (*sql.Tx, error) {
	var tx *sql.Tx
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		tx, err = db.Begin()
		if err == nil {
			return tx, nil
		}
		if !isLockBusyErr(err) {
			return nil, err
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return nil, err
}

// testCommitHook is a fault-injection seam for the batch writer (test-only).
// Holds a *func() error or nil.
var testCommitHook atomic.Value

// SetCommitFailureHook installs (or clears, with nil) a hook invoked in place
// of tx.Commit inside the batch writer. Test-only fault injection — used by
// the durability suite to prove a failed commit is retried and, when it
// persists, spilled rather than silently dropped.
func SetCommitFailureHook(fn func() error) {
	if fn == nil {
		testCommitHook.Store((*func() error)(nil))
		return
	}
	testCommitHook.Store(&fn)
}

// flushBatchOnce executes tasks in a single transaction, returning the first
// fatal error (Begin/Commit). Per-statement errors are logged and do not abort
// the batch (the tasks are independent writes).
func flushBatchOnce(db *sql.DB, tasks []dbTask) error {
	tx, err := beginWithRetry(db)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() // no-op after a successful Commit

	// Prepared statement cache within this transaction.
	// Reusing stmts eliminates sqlite3_prepare_v2 overhead (31% of CPU).
	stmtCache := make(map[string]*sql.Stmt, 8)
	defer func() {
		for _, s := range stmtCache {
			s.Close()
		}
	}()

	for _, task := range tasks {
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

	if h, _ := testCommitHook.Load().(*func() error); h != nil {
		if err := (*h)(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// flushBatchWithRetry executes tasks in a transaction, retrying the WHOLE
// batch on lock/busy failures. This is safe under WAL: a busy COMMIT means the
// transaction was NOT applied, so re-executing cannot duplicate rows. A
// failed Commit leaves the transaction in an unknown state, so it is never
// retried in place — only the full batch is. Non-busy errors return
// immediately and the caller decides (spill) what to do with the batch.
func flushBatchWithRetry(db *sql.DB, tasks []dbTask) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = flushBatchOnce(db, tasks)
		if lastErr == nil {
			return nil
		}
		if !isLockBusyErr(lastErr) {
			return lastErr
		}
		if attempt < 2 {
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
		}
	}
	return lastErr
}

// deepCopyPayload creates a deep copy of a payload map using recursive structural
// copying. Primitives (strings, numbers, bools) are immutable in Go and safe to
// share across goroutines — only maps and slices need structural duplication.
// This avoids the JSON marshal/unmarshal overhead (~2–5µs per copy) that was the
// most expensive allocation on the Emit hot path for parallel route steps.
func deepCopyPayload(src map[string]interface{}) map[string]interface{} {
	if len(src) == 0 {
		return make(map[string]interface{})
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = deepCopyValue(v)
	}
	return dst
}

// deepCopyValue recursively copies a single value. Maps and slices are structurally
// duplicated; all other types (string, float64, bool, nil, json.Number) are immutable
// and returned as-is with zero allocation.
func deepCopyValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return deepCopyPayload(val)
	case []interface{}:
		cp := make([]interface{}, len(val))
		for i, item := range val {
			cp[i] = deepCopyValue(item)
		}
		return cp
	default:
		return v
	}
}
