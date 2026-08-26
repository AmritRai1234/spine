package engine

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// db.fanout — generalizes subscriptions.sweep into a config-driven scan-and-
// emit action. On a cron route:
//
//	- on: BILLING_TICK
//	  cron: 86400s
//	  steps:
//	    - action: db.fanout
//	      table: subscriptions
//	      where: "next_charge_date <= $now"
//	      emit_event: SUBSCRIPTION_DUE
//	      due_column: next_charge_date
//	      interval_column: interval_months
//	      batch_size: 1000        # optional, default 1000
//
// it scans `table` for rows matching `where` (a parameterized single-column
// comparison, same grammar as db.sum/db.adjust), and for EACH matching row:
//
//  1. builds a deterministic per-row idempotency key — sha256 over
//     table|rowid|stored due value — stamped as _idempotency_key on the
//     emitted payload, so the engine's durable _spine_idem claim protocol
//     makes re-running the same scan (cron double-fire, crash mid-batch,
//     restart) a no-op for rows already processed.
//  2. ADVANCES the due column forward by the row's own interval column
//     BEFORE emitting (same ruling as subscriptions.sweep): advancing only
//     after downstream success would leave the due date unchanged on a failed
//     charge, so the identical key would be claimed again next tick — a
//     silent permanent block on retry. Business-level failure (declined card)
//     is the manifest route's concern via on_failure/compensate.
//  3. emits `emit_event` carrying the full row as $event.payload. A fresh
//     Emit from an action runs at depth 0, so the claim protocol applies.
//
// Batching uses keyset pagination (_spine_id > cursor ORDER BY _spine_id LIMIT
// n) — stable under concurrent writes and O(1) memory regardless of table
// size. If the UPDATE fails for a row it is skipped (left due); the next scan
// picks it up again because the old idempotency key was never spent.
func (b *Bus) dbFanout(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	table := b.sanitizeIdentCached(step.Table)
	emitEvent := step.Config["emit_event"]
	dueCol := b.sanitizeIdentCached(step.Config["due_column"])
	intervalCol := b.sanitizeIdentCached(step.Config["interval_column"])
	whereExpr := step.Where

	if table == "" || emitEvent == "" || whereExpr == "" ||
		dueCol == "" || intervalCol == "" {
		return fmt.Errorf("db.fanout requires 'table', 'where', 'emit_event', 'due_column' and 'interval_column'")
	}

	batchSize := 1000
	if bs := step.Config["batch_size"]; bs != "" {
		if n, err := strconv.Atoi(bs); err == nil && n > 0 {
			batchSize = n
		}
	}

	// Resolve the WHERE clause once up front: single-column comparison with a
	// resolvable right-hand side ($now, literal, or event field) — same grammar
	// as db.sum. Anything else is rejected loudly rather than silently scanning
	// the whole table.
	col, op, threshold, err := parseWhereCondition(whereExpr, eventName, payload)
	if err != nil {
		return fmt.Errorf("db.fanout: invalid 'where': %w", err)
	}
	safeWhereCol := b.sanitizeIdentCached(col)
	if safeWhereCol == "" {
		return fmt.Errorf("db.fanout: invalid where column")
	}

	fired := 0

	cursor := int64(0)
	for {
		query := fmt.Sprintf(
			`SELECT "_spine_id", "%s", "%s", "%s" FROM "%s" WHERE "%s" %s %s AND "_spine_id" > %s ORDER BY "_spine_id" LIMIT %s`,
			dueCol, intervalCol, safeWhereCol, table, safeWhereCol, op, b.ph(1), b.ph(2), b.ph(3))
		rows, err := b.db.Query(query, threshold, cursor, batchSize)
		if err != nil {
			return fmt.Errorf("db.fanout scan on '%s' failed: %w", table, err)
		}

		type row struct {
			spineID  int64
			due      string
			interval int
			match    interface{}
		}
		var batch []row
		for rows.Next() {
			var r row
			var intervalVal interface{}
			if err := rows.Scan(&r.spineID, &r.due, &intervalVal, &r.match); err != nil {
				log.Printf("[fanout] %s: skipping malformed row: %v", table, err)
				continue
			}
			r.interval, _ = strconv.Atoi(fmt.Sprintf("%v", intervalVal))
			batch = append(batch, r)
			cursor = r.spineID // keyset cursor always advances
		}
		rows.Close()
		if rows.Err() != nil {
			return fmt.Errorf("db.fanout: iterating results failed: %w", rows.Err())
		}

		for _, r := range batch {
			// Idempotency key hashes ONLY stored values (table, rowid, the
			// row's own due column) — never the dynamic $now used to find it.
			h := sha256.Sum256([]byte(fmt.Sprintf("db.fanout|%s|%d|%v", table, r.spineID, r.match)))
			idemKey := "fanout-" + hex.EncodeToString(h[:16])

			// Advance first, then emit — see doc comment for the ordering
			// rationale. Failure to advance skips emission; row stays due for
			// the next scan.
			nextDue, ok := advanceDue(r.due, r.interval, time.Now().UTC())
			if !ok {
				log.Printf("[fanout] %s row %d: unparseable %s %q — skipping", table, r.spineID, dueCol, r.due)
				continue
			}
			if _, err := b.db.Exec(
				fmt.Sprintf(`UPDATE "%s" SET "%s" = %s WHERE "_spine_id" = %s`, table, dueCol, b.ph(1), b.ph(2)),
				nextDue.Format(time.RFC3339), r.spineID); err != nil {
				log.Printf("[fanout] %s row %d: advancing %s failed: %v", table, r.spineID, dueCol, err)
				continue
			}

			rowPayload, err := b.loadRow(table, r.spineID)
			if err != nil {
				log.Printf("[fanout] %s row %d: loading full row failed: %v", table, r.spineID, err)
				continue
			}
			rowPayload["_idempotency_key"] = idemKey

			if _, err := b.Emit(emitEvent, rowPayload); err != nil {
				// Most common cause here is an idempotency conflict: this exact
				// row+due combination was already fired by another concurrent
				// fanout (or a replayed scan). That is success, not failure.
				if strings.Contains(err.Error(), "idempotency") &&
					!strings.Contains(err.Error(), "corrupted") {
					continue
				}
				log.Printf("[fanout] %s row %d: emit of '%s' failed: %v", table, r.spineID, emitEvent, err)
			} else {
				fired++
			}
		}

		if len(batch) < batchSize {
			break // last page
		}
	}

	payload["fanned_out"] = fired
	return nil
}

// advanceDue parses a stored due timestamp (RFC3339 preferred, space-separated
// fallback like subscriptions.sweep) and rolls it forward in `months` steps
// until it lands strictly AFTER `now` — the catch-up behaviour inherited
// explicitly from subscriptions.sweep. Without this, a row missed during a
// long outage (e.g. 90 days overdue on a monthly cycle) stays due after ONE
// monthly hop (−60 days ≤ now) and re-fires every subsequent scan — the exact
// stampede this must prevent. One outage collapsed into one fire per row.
func advanceDue(due string, months int, now time.Time) (time.Time, bool) {
	if months < 1 {
		months = 1
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(due))
	if err != nil {
		if t2, err2 := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(due)); err2 == nil {
			t = t2.UTC()
		} else {
			return time.Time{}, false
		}
	}
	next := rollMonthly(t, months)
	for !next.After(now) { // catch-up: never stop inside the due window
		next = rollMonthly(next, months)
	}
	return next, true
}

// loadRow reads every column of a table row into a flat payload map.
func (b *Bus) loadRow(table string, spineID int64) (map[string]interface{}, error) {
	rows, err := b.db.Query(fmt.Sprintf(`SELECT * FROM "%s" WHERE "_spine_id" = %s`, table, b.ph(1)), spineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		switch v := vals[i].(type) {
		case nil:
			out[c] = nil
		case []byte:
			out[c] = string(v)
		default:
			out[c] = v
		}
	}
	return out, nil
}
