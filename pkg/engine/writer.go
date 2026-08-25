package engine

import (
	"database/sql"
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
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		_, err = db.Exec(query, params...)
		if err == nil {
			return nil
		}
		errStr := err.Error()
		if strings.Contains(errStr, "locked") || strings.Contains(errStr, "busy") {
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			continue
		}
		return err
	}
	return err
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
