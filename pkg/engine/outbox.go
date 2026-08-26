package engine

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// initOutboxTable creates the _spine_outbox table for persistent webhook retries.
// Returns an error so startup fails fast when the database is unwritable.
func (b *Bus) initOutboxTable() error {
	query := `CREATE TABLE IF NOT EXISTS "_spine_outbox" (
		id ` + b.dialect.autoIncPK + `,
		action TEXT NOT NULL,
		payload TEXT NOT NULL,
		step_data TEXT DEFAULT '',
		attempts INTEGER DEFAULT 0,
		status TEXT DEFAULT 'pending',
		next_retry_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`
	if _, err := b.db.Exec(query); err != nil {
		return err
	}
	if _, err := b.db.Exec(`CREATE INDEX IF NOT EXISTS "idx_spine_outbox_status" ON "_spine_outbox"("status", "next_retry_at")`); err != nil {
		return err
	}
	// Ensure step_data column exists on upgraded schemas. A duplicate-column
	// error is tolerated (SQLite: "duplicate column name"; PostgreSQL:
	// SQLSTATE 42701 "column ... already exists" — there is no portable
	// ADD COLUMN IF NOT EXISTS).
	if _, err := b.db.Exec(`ALTER TABLE "_spine_outbox" ADD COLUMN step_data TEXT DEFAULT ''`); err != nil {
		msg := err.Error()
		if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
			return err
		}
	}
	return nil
}

// EnqueueOutboxStep enqueues a persistent retry task into _spine_outbox table with full RouteStep context.
func (b *Bus) EnqueueOutboxStep(step *manifest.RouteStep, action string, payload map[string]interface{}, backoffMs int) {
	b.enqueueOutboxStep(step, action, payload, backoffMs)
}

// NotifyOutbox signals the background outbox worker pool to process pending retries immediately.
func (b *Bus) NotifyOutbox() {
	select {
	case b.outboxNotify <- struct{}{}:
	default:
	}
}

// enqueueOutbox persistent retry task into _spine_outbox table.
func (b *Bus) enqueueOutbox(action string, payload map[string]interface{}, backoffMs int) {
	b.enqueueOutboxStep(nil, action, payload, backoffMs)
}

// enqueueOutboxStep persistent retry task into _spine_outbox table with full RouteStep context.
// Signals the processor to wake up immediately via outboxNotify.
func (b *Bus) enqueueOutboxStep(step *manifest.RouteStep, action string, payload map[string]interface{}, backoffMs int) {
	if backoffMs <= 0 {
		if reg := b.GetRegistry(); reg != nil && reg.GetSchema() != nil && reg.GetSchema().Database.Outbox.BackoffMs > 0 {
			backoffMs = reg.GetSchema().Database.Outbox.BackoffMs
		} else {
			backoffMs = 1000
		}
	}

	if action == "" && step != nil {
		action = step.Action
	}

	payloadBytes, _ := json.Marshal(payload)
	var stepDataStr string
	if step != nil {
		stepBytes, _ := json.Marshal(step)
		stepDataStr = string(stepBytes)
	}

	now := time.Now().UTC()
	nextRetry := now.Add(time.Duration(backoffMs) * time.Millisecond).Format(time.RFC3339)

	insertSQL := `INSERT INTO "_spine_outbox" (action, payload, step_data, attempts, status, next_retry_at, created_at) VALUES (` + b.ph(1) + `, ` + b.ph(2) + `, ` + b.ph(3) + `, 1, 'pending', ` + b.ph(4) + `, ` + b.ph(5) + `)`
	params := []interface{}{action, string(payloadBytes), stepDataStr, nextRetry, now.Format(time.RFC3339)}

	if !b.writer.submitAny(dbTask{query: insertSQL, params: params}) {
		// All shards saturated — fall back to a synchronous write so the
		// outbox task is never silently lost.
		if err := execWithRetry(b.db, insertSQL, params...); err != nil {
			log.Printf("[outbox] enqueue fallback failed (outbox task lost): %v", err)
		}
	}

	// Notify the processor to wake up immediately
	select {
	case b.outboxNotify <- struct{}{}:
	default:
	}
}

// outboxRetention is how long terminal (failed/completed) outbox rows are kept
// before the periodic sweep deletes them, bounding table growth over time.
const outboxRetention = 7 * 24 * time.Hour

// processOutboxQueue processes pending outbox retry tasks surviving server restarts.
// Uses a bounded worker pool configured via database.outbox.max_workers in the manifest.
func (b *Bus) processOutboxQueue() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Crash recovery: a previous process may have died mid-execution, leaving
	// rows stuck in 'processing'. No worker is running yet, so every such row
	// can safely return to 'pending' and be retried.
	if _, err := b.db.Exec(`UPDATE "_spine_outbox" SET status = 'pending' WHERE status = 'processing'`); err != nil {
		log.Printf("[outbox] reset stale 'processing' rows failed: %v", err)
	}

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

		rows, err := b.db.Query(`SELECT id, action, payload, COALESCE(step_data, ''), attempts FROM "_spine_outbox" WHERE status = 'pending' AND next_retry_at <= `+b.ph(1)+` ORDER BY id ASC LIMIT 50`, nowStr)
		if err != nil {
			log.Printf("[outbox] select pending failed: %v", err)
			return
		}

		type outboxTask struct {
			id       int64
			action   string
			payload  string
			stepData string
			attempts int
		}
		var pending []outboxTask
		for rows.Next() {
			var task outboxTask
			if err := rows.Scan(&task.id, &task.action, &task.payload, &task.stepData, &task.attempts); err == nil {
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

				// Claim the row before executing so concurrent ticks (or
				// multiple bus instances on the same DB) cannot double-deliver.
				// Only 'pending' rows can be claimed.
				res, err := b.db.Exec(`UPDATE "_spine_outbox" SET status = 'processing' WHERE id = `+b.ph(1)+` AND status = 'pending'`, t.id)
				if err != nil {
					log.Printf("[outbox] claim row %d failed: %v", t.id, err)
					return
				}
				if n, _ := res.RowsAffected(); n == 0 {
					return // already claimed by another worker
				}

				var payload map[string]interface{}
				_ = json.Unmarshal([]byte(t.payload), &payload)

				step := &manifest.RouteStep{
					Action: t.action,
				}
				if t.stepData != "" {
					_ = json.Unmarshal([]byte(t.stepData), step)
				}

				// execStepNoEnqueue: a failed retry must NEVER enqueue a fresh
				// outbox row (that would bypass maxRetries and grow the table
				// forever). Retries update attempts on THIS row only.
				err = b.execStepNoEnqueue(step, "_spine_outbox_retry", payload)
				if err == nil {
					if uerr := execWithRetry(b.db, `UPDATE "_spine_outbox" SET status = 'completed' WHERE id = `+b.ph(1), t.id); uerr != nil {
						log.Printf("[outbox] mark row %d completed failed: %v", t.id, uerr)
					}
					return
				}

				nextAttempts := t.attempts + 1
				if nextAttempts > maxRetries {
					if uerr := execWithRetry(b.db, `UPDATE "_spine_outbox" SET status = 'failed', attempts = `+b.ph(1)+` WHERE id = `+b.ph(2), nextAttempts, t.id); uerr != nil {
						log.Printf("[outbox] mark row %d failed: %v", t.id, uerr)
					}
					return
				}
				multiplier := 1 << (nextAttempts - 1)
				if multiplier > 32 {
					multiplier = 32
				}
				nextRetry := time.Now().UTC().Add(time.Duration(backoffMs*multiplier) * time.Millisecond).Format(time.RFC3339)
				if uerr := execWithRetry(b.db, `UPDATE "_spine_outbox" SET attempts = `+b.ph(1)+`, status = 'pending', next_retry_at = `+b.ph(2)+` WHERE id = `+b.ph(3), nextAttempts, nextRetry, t.id); uerr != nil {
					log.Printf("[outbox] reschedule row %d failed: %v", t.id, uerr)
				}
			}(task)
		}
		wg.Wait()
	}

	var lastPurge time.Time
	for {
		select {
		case <-b.stopCh:
			return
		case <-b.outboxNotify:
			process()
		case <-ticker.C:
			process()
			// Periodic sweep: bound table growth by dropping terminal rows
			// older than outboxRetention.
			if time.Since(lastPurge) >= time.Minute {
				lastPurge = time.Now()
				cutoff := time.Now().UTC().Add(-outboxRetention).Format(time.RFC3339)
				if _, err := b.db.Exec(`DELETE FROM "_spine_outbox" WHERE status IN ('failed', 'completed') AND created_at < `+b.ph(1), cutoff); err != nil {
					log.Printf("[outbox] purge terminal rows failed: %v", err)
				}
			}
		}
	}
}
