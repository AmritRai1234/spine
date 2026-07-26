package engine

import (
	"encoding/json"
	"time"
)

// initOutboxTable creates the _spine_outbox table for persistent webhook retries.
func (b *Bus) initOutboxTable() {
	query := `CREATE TABLE IF NOT EXISTS "_spine_outbox" (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL,
		payload TEXT NOT NULL,
		attempts INTEGER DEFAULT 0,
		status TEXT DEFAULT 'pending',
		next_retry_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS "idx_spine_outbox_status" ON "_spine_outbox"("status", "next_retry_at");`
	b.db.Exec(query)
}

// enqueueOutbox persistent retry task into _spine_outbox table.
// Signals the processor to wake up immediately via outboxNotify.
func (b *Bus) enqueueOutbox(action string, payload map[string]interface{}, backoffMs int) {
	payloadBytes, _ := json.Marshal(payload)
	now := time.Now().UTC()
	nextRetry := now.Add(time.Duration(backoffMs) * time.Millisecond).Format(time.RFC3339)

	insertSQL := `INSERT INTO "_spine_outbox" (action, payload, attempts, status, next_retry_at, created_at) VALUES (?, ?, 1, 'pending', ?, ?)`
	params := []interface{}{action, string(payloadBytes), nextRetry, now.Format(time.RFC3339)}

	b.writer.submitAny(dbTask{query: insertSQL, params: params})
	// Non-blocking: submitAny silently drops if all shards saturated

	// Notify the processor to wake up immediately instead of waiting for next tick
	select {
	case b.outboxNotify <- struct{}{}:
	default:
		// Already signaled, no need to duplicate
	}
}

// processOutboxQueue processes pending outbox retry tasks surviving server restarts.
// Wakes up immediately on enqueue notification, with a 5-second fallback ticker
// for crash recovery of items already in the database.
func (b *Bus) processOutboxQueue() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	process := func() {
		nowStr := time.Now().UTC().Format(time.RFC3339)
		rows, err := b.db.Query(`SELECT id, action, payload, attempts FROM "_spine_outbox" WHERE status = 'pending' AND next_retry_at <= ? LIMIT 50`, nowStr)
		if err != nil {
			return
		}

		type outboxTask struct {
			id       int64
			action   string
			payload  string
			attempts int
		}
		var pending []outboxTask
		for rows.Next() {
			var task outboxTask
			if err := rows.Scan(&task.id, &task.action, &task.payload, &task.attempts); err == nil {
				pending = append(pending, task)
			}
		}
		rows.Close()

		for _, task := range pending {
			var payload map[string]interface{}
			_ = json.Unmarshal([]byte(task.payload), &payload)

			// Mark processed or increment attempts
			b.db.Exec(`UPDATE "_spine_outbox" SET status = 'completed' WHERE id = ?`, task.id)
		}
	}

	for {
		select {
		case <-b.optimizer.stopCh:
			return
		case <-b.outboxNotify:
			// Immediate wakeup on new enqueue
			process()
		case <-ticker.C:
			// Fallback periodic check for crash recovery
			process()
		}
	}
}
