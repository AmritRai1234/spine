package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

type dbTask struct {
	query  string
	params []interface{}
	// barrier, when non-nil, is a flush fence: the writer loop closes it only
	// after every task submitted BEFORE this one has been committed. Used by
	// flushAndWait for durability fences (idempotency, saga compensation).
	barrier chan struct{}
}

// stmtFailures counts writes dropped because they permanently fail (constraint
// violations, type mismatches, missing columns) — the batch writer's
// never-silent-drop accounting, exposed on /metrics.
var stmtFailures uint64

// writerFlushTimeout bounds how long a durability fence (flushAndWait) will
// wait for the batch writer. Generous: under heavy lock contention a flush
// can take tens of seconds (driver busy_timeout + retry backoff). On timeout
// the caller degrades gracefully (logs, proceeds without the guarantee).
const writerFlushTimeout = 60 * time.Second

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

// statementError marks a task that failed with a permanent, non-transient
// error (constraint violation, type mismatch, missing column). The writer
// drops exactly this task (it cannot succeed as-is), counts it, and re-flushes
// the rest of the batch — the failing write is never silently lost from the
// batch, and good writes in the same batch are never lost with it.
type statementError struct {
	task dbTask
	err  error
}

func (e *statementError) Error() string {
	return fmt.Sprintf("statement failed permanently: %v", e.err)
}

// retryStmtExec executes stmt with params, retrying transient lock/busy
// errors with exponential backoff (a mid-transaction lock is not a reason to
// drop a write).
func retryStmtExec(stmt *sql.Stmt, params []interface{}) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		_, err = stmt.Exec(params...)
		if err == nil {
			return nil
		}
		if !isLockBusyErr(err) {
			return err
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return err
}

// flushBatchOnce executes tasks in a single transaction. Transient lock/busy
// statement errors are retried in place; a permanent statement error aborts
// the WHOLE transaction (nothing is committed) and is returned as a
// *statementError so the caller can drop that one task and re-flush the rest.
// Begin/Commit failures are returned as plain errors for whole-batch retry.
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
				// Some drivers prepare more strictly than they execute; keep
				// the direct-exec fallback. Only when BOTH fail is the task
				// permanently broken (e.g. missing column) — roll back so the
				// other tasks are untouched and report this task.
				if _, execErr := tx.Exec(task.query, task.params...); execErr != nil {
					return &statementError{task: task, err: fmt.Errorf("prepare failed: %v (exec: %v)", err, execErr)}
				}
				continue
			}
			stmtCache[task.query] = stmt
		}
		if err := retryStmtExec(stmt, task.params); err != nil {
			return &statementError{task: task, err: err}
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
//
// A *statementError (a permanently failing task) is handled here, not spilled:
// the transaction was rolled back, so the offending task is dropped, counted
// in stmtFailures, logged loudly, and the REMAINING tasks are re-flushed.
// Spilling a permanently-failing statement would replay it forever.
func flushBatchWithRetry(db *sql.DB, tasks []dbTask) error {
	for len(tasks) > 0 {
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			lastErr = flushBatchOnce(db, tasks)
			if lastErr == nil {
				return nil
			}
			var se *statementError
			if errors.As(lastErr, &se) {
				atomic.AddUint64(&stmtFailures, 1)
				log.Printf("[batch] dropping permanently failing write: %v", se.err)
				remaining := make([]dbTask, 0, len(tasks)-1)
				for _, t := range tasks {
					if !sameTask(t, se.task) {
						remaining = append(remaining, t)
					}
				}
				if len(remaining) == 0 {
					return nil
				}
				tasks = remaining
				break // restart the retry budget on the reduced batch
			}
			if !isLockBusyErr(lastErr) {
				return lastErr
			}
			if attempt < 2 {
				time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			}
		}
		// If the inner loop exhausted its busy-retry budget, lastErr is a
		// lock/busy error — surface it so the caller spills the batch.
		var se *statementError
		if !errors.As(lastErr, &se) {
			return lastErr
		}
	}
	return nil
}

// sameTask reports whether two tasks are identical (query + params).
// Only used on the error path, so reflect.DeepEqual's cost is irrelevant.
func sameTask(a, b dbTask) bool {
	return a.query == b.query && reflect.DeepEqual(a.params, b.params)
}

// flushAndWait submits a flush fence to shard[0] and blocks until every task
// submitted BEFORE the fence has been committed by the batch writer. Returns
// false if the writer is closed or the fence cannot be delivered within
// timeout — callers must then degrade gracefully (log, proceed without the
// durability guarantee).
func (sw *shardedWriter) flushAndWait(timeout time.Duration) bool {
	if atomic.LoadUint32(&sw.closed) != 0 {
		return false
	}
	done := make(chan struct{})
	fence := dbTask{barrier: done}
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case sw.shards[0] <- fence:
			select {
			case <-done:
				return true
			case <-time.After(timeout):
				return false
			}
		default:
			if attempt < 2 {
				time.Sleep(100 * time.Microsecond)
			}
		}
	}
	return false
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
