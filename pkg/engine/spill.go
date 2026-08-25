package engine

import (
	"encoding/json"
	"log"
	"sync/atomic"
	"time"
)

// initSpillTable creates the _spine_write_spill table used as the durable
// fallback for batch-writer failures. When a batch cannot be committed after
// retries, its tasks are retained here (never silently dropped) and replayed
// by the spill drainer once the writer recovers.
func (b *Bus) initSpillTable() error {
	query := `CREATE TABLE IF NOT EXISTS "_spine_write_spill" (
		id ` + b.dialect.autoIncPK + `,
		query TEXT NOT NULL,
		params_json TEXT NOT NULL,
		status TEXT DEFAULT 'pending',
		created_at TEXT NOT NULL
	)`
	if _, err := b.db.Exec(query); err != nil {
		return err
	}
	_, err := b.db.Exec(`CREATE INDEX IF NOT EXISTS "idx_spine_spill_status" ON "_spine_write_spill"("status", "id")`)
	return err
}

// startSpillDrainer replays pending _spine_write_spill rows back through the
// batch writer every 5 seconds. Rows are deleted only after a successful
// submit; if the writer later fails again the row re-spills (at-least-once).
func (b *Bus) startSpillDrainer() {
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
				b.drainSpill()
			}
		}
	}()
}

// CommitFailures returns the number of batch commits that failed after
// retries and were spilled to _spine_write_spill.
func (b *Bus) CommitFailures() uint64 {
	return atomic.LoadUint64(&b.commitFailures)
}

// SpillWrites returns the number of writes durably retained in
// _spine_write_spill after flush failures.
func (b *Bus) SpillWrites() uint64 {
	return atomic.LoadUint64(&b.spillWrites)
}

// LostWrites returns the number of writes dropped because even the spill
// insert failed (database fully unavailable).
func (b *Bus) LostWrites() uint64 {
	return atomic.LoadUint64(&b.lostWrites)
}

// DroppedAudit returns the number of audit rows dropped due to shard
// saturation (audit is best-effort).
func (b *Bus) DroppedAudit() uint64 {
	return atomic.LoadUint64(&b.droppedAudit)
}

// drainSpill submits up to 100 pending spill rows back to the writer.
func (b *Bus) drainSpill() {
	rows, err := b.db.Query(`SELECT id, query, params_json FROM "_spine_write_spill" WHERE status = 'pending' ORDER BY id ASC LIMIT 100`)
	if err != nil {
		return
	}

	type spillTask struct {
		id         int64
		query      string
		paramsJSON string
	}
	var tasks []spillTask
	for rows.Next() {
		var t spillTask
		if err := rows.Scan(&t.id, &t.query, &t.paramsJSON); err == nil {
			tasks = append(tasks, t)
		}
	}
	rows.Close()

	for _, t := range tasks {
		var params []interface{}
		if err := json.Unmarshal([]byte(t.paramsJSON), &params); err != nil {
			// Corrupt row — mark failed so it stops blocking the queue.
			_, _ = b.db.Exec(`UPDATE "_spine_write_spill" SET status = 'failed' WHERE id = `+b.ph(1), t.id)
			continue
		}
		if b.writer.submitAny(dbTask{query: t.query, params: params}) {
			// Intent is back in the writer pipeline; drop the durable copy.
			// A later flush failure re-spills it as a fresh row (at-least-once).
			_, _ = b.db.Exec(`DELETE FROM "_spine_write_spill" WHERE id = `+b.ph(1), t.id)
		}
	}
}

// spillBatch durably retains a batch that the writer could not commit after
// retries. Each task is inserted into _spine_write_spill synchronously; if
// even that fails the write is counted as lost and logged at CRITICAL.
func (b *Bus) spillBatch(tasks []dbTask, cause error) {
	atomic.AddUint64(&b.commitFailures, 1)
	log.Printf("[batch] flush failed after retries: %v — spilling %d writes to _spine_write_spill", cause, len(tasks))

	now := time.Now().UTC().Format(time.RFC3339)
	for _, t := range tasks {
		paramsJSON, err := json.Marshal(t.params)
		if err != nil {
			paramsJSON = []byte("[]")
		}
		if err := execWithRetry(b.db, b.spillSQL, t.query, string(paramsJSON), now); err != nil {
			atomic.AddUint64(&b.lostWrites, 1)
			log.Printf("[batch] CRITICAL: spill insert failed — write lost (%v): %v", t.query, err)
			continue
		}
		atomic.AddUint64(&b.spillWrites, 1)
	}
}
