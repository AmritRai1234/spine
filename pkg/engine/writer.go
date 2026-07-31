package engine

import (
	"database/sql"
	"strings"
	"sync/atomic"
	"time"
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
