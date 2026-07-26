package engine

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
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
	if backoffMs <= 0 {
		if reg := b.GetRegistry(); reg != nil && reg.GetSchema() != nil && reg.GetSchema().Database.Outbox.BackoffMs > 0 {
			backoffMs = reg.GetSchema().Database.Outbox.BackoffMs
		} else {
			backoffMs = 1000
		}
	}

	payloadBytes, _ := json.Marshal(payload)
	now := time.Now().UTC()
	nextRetry := now.Add(time.Duration(backoffMs) * time.Millisecond).Format(time.RFC3339)

	insertSQL := `INSERT INTO "_spine_outbox" (action, payload, attempts, status, next_retry_at, created_at) VALUES (?, ?, 1, 'pending', ?, ?)`
	params := []interface{}{action, string(payloadBytes), nextRetry, now.Format(time.RFC3339)}

	b.writer.submitAny(dbTask{query: insertSQL, params: params})

	// Notify the processor to wake up immediately
	select {
	case b.outboxNotify <- struct{}{}:
	default:
	}
}

// processOutboxQueue processes pending outbox retry tasks surviving server restarts.
// Uses a bounded worker pool configured via database.outbox.max_workers in the manifest.
func (b *Bus) processOutboxQueue() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	getOutboxConfig := func() (int, int, int) {
		maxWorkers := 10
		maxRetries := 5
		backoffMs := 1000

		if reg := b.GetRegistry(); reg != nil && reg.GetSchema() != nil {
			cfg := reg.GetSchema().Database.Outbox
			if cfg.MaxWorkers > 0 {
				maxWorkers = cfg.MaxWorkers
			}
			if cfg.MaxRetries > 0 {
				maxRetries = cfg.MaxRetries
			}
			if cfg.BackoffMs > 0 {
				backoffMs = cfg.BackoffMs
			}
		}
		return maxWorkers, maxRetries, backoffMs
	}

	process := func() {
		maxWorkers, maxRetries, backoffMs := getOutboxConfig()
		nowStr := time.Now().UTC().Format(time.RFC3339)

		rows, err := b.db.Query(`SELECT id, action, payload, attempts FROM "_spine_outbox" WHERE status = 'pending' AND next_retry_at <= ? ORDER BY id ASC LIMIT 50`, nowStr)
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

		if len(pending) == 0 {
			return
		}

		sem := make(chan struct{}, maxWorkers)
		var wg sync.WaitGroup

		for _, task := range pending {
			sem <- struct{}{}
			wg.Add(1)

			go func(t outboxTask) {
				defer func() {
					<-sem
					wg.Done()
				}()

				var payload map[string]interface{}
				_ = json.Unmarshal([]byte(t.payload), &payload)

				step := &manifest.RouteStep{
					Action: t.action,
				}

				err := b.execStep(step, "_spine_outbox_retry", payload)
				if err == nil {
					b.db.Exec(`UPDATE "_spine_outbox" SET status = 'completed' WHERE id = ?`, t.id)
				} else {
					nextAttempts := t.attempts + 1
					if nextAttempts > maxRetries {
						b.db.Exec(`UPDATE "_spine_outbox" SET status = 'failed', attempts = ? WHERE id = ?`, nextAttempts, t.id)
					} else {
						multiplier := 1 << (nextAttempts - 1)
						if multiplier > 32 {
							multiplier = 32
						}
						nextRetry := time.Now().UTC().Add(time.Duration(backoffMs*multiplier) * time.Millisecond).Format(time.RFC3339)
						b.db.Exec(`UPDATE "_spine_outbox" SET attempts = ?, next_retry_at = ? WHERE id = ?`, nextAttempts, nextRetry, t.id)
					}
				}
			}(task)
		}
		wg.Wait()
	}

	for {
		select {
		case <-b.optimizer.stopCh:
			return
		case <-b.outboxNotify:
			process()
		case <-ticker.C:
			process()
		}
	}
}
