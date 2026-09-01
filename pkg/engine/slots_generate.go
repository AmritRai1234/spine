package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// slots.generate — turns a business schedule (open hours, duration, capacity)
// into bookable slot rows. The booking-side sibling of db.fanout: fanout
// consumes rows (scan-and-emit), generate produces them (compute-and-upsert).
//
// Manifest shape:
//
//	- on: SLOTS_TICK
//	  cron: 86400s
//	  steps:
//	    - action: slots.generate
//	      table: slots
//	      days_ahead: 30
//	      weekdays: "mon-fri"       # or "mon,wed,fri" — optional, default all
//	      open: "09:00"
//	      close: "17:00"
//	      duration_minutes: 30
//	      capacity: 1
//
// Guarantees:
//
//  1. Deterministic row identity: slot id = "sgen-" + sha256(table | start
//     time)[:16]. Re-running the cron produces the SAME ids for the same
//     schedule — the upsert (single-key, ON CONFLICT DO UPDATE) makes
//     re-generation idempotent. No duplicate slots, ever.
//  2. Never touches capacity on EXISTING rows. The upsert's SET clause
//     deliberately EXCLUDES the capacity column: regeneration refreshes
//     identity fields (start/end timestamps) but a slot with live bookings
//     keeps its decremented capacity. This is the acceptance criterion that
//     generation running concurrently with booking traffic is safe by
//     construction — the generator never writes the column a booker claims.
//  3. Schedule-change semantics: rows for times no longer in the schedule
//     are NEVER deleted or deactivated by the generator. A slot with
//     confirmed bookings cannot be silently vanished; closing a day leaves
//     the old slots bookable-but-generated-stale. Cleanup is an explicit
//     business decision (a manifest route using db.delete with a where on
//     generated ids + no bookings), not an implicit side effect of a cron.
//
// Requires spine_version: 3 (follows db.fanout's capability tier).
func (b *Bus) slotsGenerate(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	table := b.sanitizeIdentCached(step.Table)
	if table == "" {
		return fmt.Errorf("slots.generate requires 'table'")
	}
	openStr := step.Config["open"]
	closeStr := step.Config["close"]
	durStr := step.Config["duration_minutes"]
	if openStr == "" || closeStr == "" || durStr == "" {
		return fmt.Errorf("slots.generate requires 'open', 'close' and 'duration_minutes' config")
	}

	daysAhead := 30
	if da := step.Config["days_ahead"]; da != "" {
		if n, err := strconv.Atoi(da); err == nil && n > 0 && n <= 365 {
			daysAhead = n
		}
	}
	duration, err := strconv.Atoi(durStr)
	if err != nil || duration <= 0 {
		return fmt.Errorf("slots.generate invalid 'duration_minutes': %s", durStr)
	}
	capacity := int64(1)
	if c := step.Config["capacity"]; c != "" {
		if n, err := strconv.ParseInt(c, 10, 64); err == nil && n >= 0 {
			capacity = n
		}
	}

	openT, err := time.Parse("15:04", openStr)
	if err != nil {
		return fmt.Errorf("slots.generate invalid 'open' time (use HH:MM): %s", openStr)
	}
	closeT, err := time.Parse("15:04", closeStr)
	if err != nil {
		return fmt.Errorf("slots.generate invalid 'close' time (use HH:MM): %s", closeStr)
	}
	openMin := openT.Hour()*60 + openT.Minute()
	closeMin := closeT.Hour()*60 + closeT.Minute()
	if closeMin <= openMin {
		return fmt.Errorf("slots.generate: 'close' (%s) must be after 'open' (%s)", closeStr, openStr)
	}

	// weekday filter: "mon-fri", "mon,wed,fri", or absent = all days.
	wantDay := [7]bool{true, true, true, true, true, true, true}
	if wd := strings.ToLower(strings.TrimSpace(step.Config["weekdays"])); wd != "" {
		for d := range wantDay {
			wantDay[d] = false
		}
		dayNames := map[string]time.Weekday{
			"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
			"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
			"sat": time.Saturday,
		}
		apply := func(name string) bool {
			d, ok := dayNames[name]
			if !ok {
				return false
			}
			wantDay[int(d)] = true
			return true
		}
		if strings.Contains(wd, "-") {
			parts := strings.SplitN(wd, "-", 2)
			lo, hi := dayNames[strings.TrimSpace(parts[0])], dayNames[strings.TrimSpace(parts[1])]
			if lo == 0 && strings.TrimSpace(parts[0]) != "sun" {
				return fmt.Errorf("slots.generate invalid weekday name: %s", parts[0])
			}
			if hi == 0 && strings.TrimSpace(parts[1]) != "sun" {
				return fmt.Errorf("slots.generate invalid weekday name: %s", parts[1])
			}
			for d := lo; ; d = (d + 1) % 7 {
				wantDay[int(d)] = true
				if d == hi {
					break
				}
			}
		} else {
			for _, name := range strings.Split(wd, ",") {
				if !apply(strings.TrimSpace(name)) {
					return fmt.Errorf("slots.generate invalid weekday name: %s", name)
				}
			}
		}
	}

	// Column set for ensureTable: identity + schedule fields. capacity is
	// INTEGER so db.adjust math works on it; created columns are additive.
	colDefs := []string{
		`"id" TEXT`,
		`"start_time" TEXT`,
		`"end_time" TEXT`,
		`"capacity" INTEGER`,
	}
	if err := b.ensureTable(table, colDefs); err != nil {
		return fmt.Errorf("slots.generate ensure table failed: %w", err)
	}
	// Slot ids are deterministic ("sgen-"+hash) and the upsert keys on id —
	// ON CONFLICT("id") requires a real unique index (Postgres rejects
	// without it, SQLSTATE 42P10; same ordering discipline as dbUpsert).
	b.ensureUniqueIndex(table, "id")

	// Build the full set of slot rows for the window. Deterministic order:
	// day-major, time-minor — so the same schedule always yields the same
	// sequence of (id, start, end) triples.
	now := time.Now().UTC()
	type slotRow struct {
		id, start, end string
	}
	var rows []slotRow
	for day := 0; day <= daysAhead; day++ {
		day0 := time.Date(now.Year(), now.Month(), now.Day()+day, 0, 0, 0, 0, time.UTC)
		if !wantDay[int(day0.Weekday())] {
			continue
		}
		for m := openMin; m+duration <= closeMin; m += duration {
			start := day0.Add(time.Duration(m) * time.Minute)
			end := start.Add(time.Duration(duration) * time.Minute)
			hash := sha256.Sum256([]byte(table + "|" + start.Format(time.RFC3339)))
			rows = append(rows, slotRow{
				id:    "sgen-" + hex.EncodeToString(hash[:])[:16],
				start: start.Format(time.RFC3339),
				end:   end.Format(time.RFC3339),
			})
		}
	}

	// Upsert each slot. Single-key identity (id) → merge semantics: existing
	// rows get their timestamps refreshed. The SET clause deliberately
	// EXCLUDES capacity (guarantee 2): live bookings keep their claims.
	inserted := 0
	for _, r := range rows {
		query := fmt.Sprintf(
			`INSERT INTO "%s" ("id", "start_time", "end_time", "capacity") VALUES (%s, %s, %s, %s) `+
				`ON CONFLICT("id") DO UPDATE SET "start_time" = %s, "end_time" = %s`,
			table, b.ph(1), b.ph(2), b.ph(3), b.ph(4), b.ph(5), b.ph(6))
		if err := execWithRetry(b.db, query, r.id, r.start, r.end, capacity, r.start, r.end); err != nil {
			return fmt.Errorf("slots.generate upsert on '%s' failed for slot %s: %w", table, r.start, err)
		}
		inserted++
	}

	payload["slots_generated"] = inserted
	return nil
}
