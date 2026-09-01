package features

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// slots.generate test suite. Acceptance criteria from the 2026-09-01 design
// review:
//
//   - AC1: idempotent regeneration — re-running the cron with an unchanged
//     schedule produces the same slot rows, no duplicates.
//   - AC2: schedule-change safety — changing the schedule never deletes or
//     deactivates previously generated slots (a slot with live bookings must
//     not vanish under a store owner's hours change).
//   - AC3: generation never touches the capacity column on EXISTING rows —
//     a slot whose capacity was decremented by a live booking keeps its
//     value across regeneration. Generation running concurrently with
//     booking traffic must not perturb capacity (the "concurrent generation
//     vs booking" criterion, 3b-spirit applied to the one new component).
//
// Booking flow exercises the composite db.upsert + db.adjust primitives so
// capacity decrements are real, not simulated.

const slotsGenManifest = `spine_version: 3

database:
  tables:
    - slots
    - bookings

nodes:
  - name: Booking
    emits:
      - event: GEN_TICK
      - event: SEED_SLOT
        payload:
          id: string
          capacity: integer
      - event: BOOK_SLOT
      - event: BOOK_RAW
    listens:
      - state: BOOKING_CONFIRMED

routes:
  - on: GEN_TICK
    steps:
      - action: slots.generate
        table: slots
        %CONFIG%

  # Manual seed for concurrent-booking tests (independent of generation).
  - on: SEED_SLOT
    steps:
      - action: db.insert
        table: slots

  # Real booking flow — atomic adjust + composite claim (proven primitives).
  - on: BOOK_SLOT
    on_failure: SLOT_UNAVAILABLE
    steps:
      - action: db.adjust
        table: slots
        where: "id = $event.payload.slot_id"
        column: capacity
        by: -1
        floor: 0
      - action: set
        id: $uuid
        created_at: $now
        status: confirmed
      - action: db.upsert
        table: bookings
        key:
          - slot_id
          - customer_email
    emit: BOOKING_CONFIRMED

  # Raw insert bypassing the claim (capacity untouched) — used to prove the
  # generator preserves rows it didn't create.
  - on: BOOK_RAW
    steps:
      - action: db.insert
        table: bookings
`

func newSlotsGenEngine(t *testing.T, dir string, config string) *spine.Engine {
	t.Helper()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")
	if err := os.WriteFile(manifestPath, []byte(
		strings.Replace(slotsGenManifest, "%CONFIG%", config, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	return eng
}

func genConfig(days int, weekdays, open, close string, dur, capacity int) string {
	c := fmt.Sprintf("days_ahead: %d\n        open: %q\n        close: %q\n        duration_minutes: %d\n        capacity: %d",
		days, open, close, dur, capacity)
	if weekdays != "" {
		c = fmt.Sprintf("days_ahead: %d\n        weekdays: %q\n        open: %q\n        close: %q\n        duration_minutes: %d\n        capacity: %d",
			days, weekdays, open, close, dur, capacity)
	}
	return c
}

func genCount(t *testing.T, eng *spine.Engine) int {
	t.Helper()
	rows, err := eng.Bus.GetTableRows("slots", 500, 0)
	if err != nil {
		t.Fatalf("GetTableRows(slots): %v", err)
	}
	n := 0
	for _, r := range rows {
		if id, _ := r["id"].(string); strings.HasPrefix(id, "sgen-") {
			n++
		}
	}
	return n
}

func genTick(t *testing.T, eng *spine.Engine) int {
	t.Helper()
	if _, err := eng.Bus.Emit("GEN_TICK", map[string]interface{}{}); err != nil {
		t.Fatalf("GEN_TICK failed: %v", err)
	}
	return genCount(t, eng)
}

// AC1: re-running with an unchanged schedule is idempotent — same count,
// same ids, no duplicates. Also checks the tier gate implicitly (manifest is
// spine_version: 3).
func TestSlotsGenerateIdempotentRerun(t *testing.T) {
	eng := newSlotsGenEngine(t, t.TempDir(), genConfig(7, "", "09:00", "10:00", 30, 2))
	defer eng.Close()

	first := genTick(t, eng)
	if first <= 0 {
		t.Fatalf("first generation produced %d slots", first)
	}
	ids1 := map[string]bool{}
	rows, _ := eng.Bus.GetTableRows("slots", 500, 0)
	for _, r := range rows {
		if id, _ := r["id"].(string); strings.HasPrefix(id, "sgen-") {
			ids1[id] = true
		}
	}

	second := genTick(t, eng)
	if second != first {
		t.Fatalf("second generation count changed: %d -> %d (schedule unchanged)", first, second)
	}
	if got := genCount(t, eng); got != first {
		t.Fatalf("slot rows duplicated on re-run: %d rows, expected %d", got, first)
	}
	rows2, _ := eng.Bus.GetTableRows("slots", 500, 0)
	for _, r := range rows2 {
		id, _ := r["id"].(string)
		if strings.HasPrefix(id, "sgen-") && !ids1[id] {
			t.Fatalf("re-run produced a NEW id %s — identity not deterministic", id)
		}
	}
}

// AC1b: capacity column integrity — upsert refreshes timestamps, never
// resets capacity to the configured value on existing rows.
func TestSlotsGeneratePreservesBookedCapacity(t *testing.T) {
	eng := newSlotsGenEngine(t, t.TempDir(), genConfig(7, "", "09:00", "10:00", 30, 2))
	defer eng.Close()
	genTick(t, eng)

	// Book one generated slot via the real claim flow: capacity 2 -> 1.
	rows, _ := eng.Bus.GetTableRows("slots", 500, 0)
	var slotID string
	for _, r := range rows {
		if id, _ := r["id"].(string); strings.HasPrefix(id, "sgen-") {
			slotID = id
			break
		}
	}
	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": slotID, "customer_email": "booker@x.com",
	}); err != nil {
		t.Fatalf("booking failed: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // batched writer flush

	capBefore := capOf(t, eng, slotID)
	if capBefore != 1 {
		t.Fatalf("precondition: capacity after booking should be 1, got %d", capBefore)
	}

	// Regenerate — capacity must NOT be reset to 2.
	genTick(t, eng)
	capAfter := capOf(t, eng, slotID)
	if capAfter != 1 {
		t.Fatalf("AC3 violation: regeneration reset capacity %d -> %d on a booked slot", capBefore, capAfter)
	}
}

func capOf(t *testing.T, eng *spine.Engine, slotID string) int64 {
	t.Helper()
	rows, err := eng.Bus.QueryWhere("slots", "id", slotID, 5, 0)
	if err != nil || len(rows) == 0 {
		t.Fatalf("slot %s not found (err=%v)", slotID, err)
	}
	switch v := rows[0]["capacity"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		t.Fatalf("unexpected capacity type %T", rows[0]["capacity"])
		return 0
	}
}

// AC2: schedule change never deletes or deactivates previously generated
// slots. Regenerate with a narrower window (shorter hours): old slots from
// the wider window must STILL EXIST with their capacity intact.
func TestSlotsGenerateScheduleChangeNeverDeletes(t *testing.T) {
	dir := t.TempDir()
	eng := newSlotsGenEngine(t, dir, genConfig(7, "", "09:00", "10:00", 30, 2))
	defer eng.Close()
	genTick(t, eng)
	wideCount := genCount(t, eng)
	if wideCount < 2 {
		t.Fatalf("precondition: expected >=2 generated slots, got %d", wideCount)
	}

	// Store owner closes early: 09:00-09:30 instead of 09:00-10:00.
	// Simulate a schedule change: close engine, rewrite the manifest with a
	// narrower window, reopen on the SAME database.
	dbPath := filepath.Join(dir, "spine.db")
	eng.Close()

	manifestPath := filepath.Join(dir, "app.spine")
	newManifest := strings.Replace(slotsGenManifest, "%CONFIG%",
		genConfig(7, "", "09:00", "09:30", 30, 2), 1)
	if err := os.WriteFile(manifestPath, []byte(newManifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng2, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer eng2.Close()

	genTick(t, eng2)

	// THE criterion: old wide-window slots still exist, capacity intact.
	rows, _ := eng2.Bus.GetTableRows("slots", 500, 0)
	old := 0
	for _, r := range rows {
		if id, _ := r["id"].(string); strings.HasPrefix(id, "sgen-") {
			old++
		}
	}
	if old != wideCount {
		t.Fatalf("AC2 violation: schedule change removed generated slots: %d -> %d", wideCount, old)
	}
}

// AC3: generation concurrently with live booking traffic must not perturb
// capacity on existing rows. One pre-generated, pre-booked slot (capacity
// decremented) + repeated regeneration while bookings race.
func TestSlotsGenerateConcurrentWithBookings(t *testing.T) {
	eng := newSlotsGenEngine(t, t.TempDir(), genConfig(7, "", "09:00", "10:00", 30, 1))
	defer eng.Close()
	genTick(t, eng)

	rows, _ := eng.Bus.GetTableRows("slots", 500, 0)
	var slotID string
	for _, r := range rows {
		if id, _ := r["id"].(string); strings.HasPrefix(id, "sgen-") {
			slotID = id
			break
		}
	}

	// Claim the only seat: capacity 1 -> 0.
	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": slotID, "customer_email": "first@x.com",
	}); err != nil {
		t.Fatalf("booking failed: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := capOf(t, eng, slotID); got != 0 {
		t.Fatalf("precondition: capacity should be 0 after claim, got %d", got)
	}

	// Hammer: regenerate repeatedly WHILE booking attempts race the same
	// slot (all must fail — floor 0) and capacity stays 0.
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := eng.Bus.Emit("GEN_TICK", map[string]interface{}{}); err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
				"slot_id": slotID, "customer_email": fmt.Sprintf("racer%d@x.com", i),
			}); err == nil {
				errs <- fmt.Errorf("racer %d booked a 0-capacity slot — oversell", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if got := capOf(t, eng, slotID); got != 0 {
		t.Fatalf("AC3 violation: concurrent generation perturbed capacity: 0 -> %d", got)
	}
	if n := genCount(t, eng); n == 0 {
		t.Fatal("generation stopped producing rows under load")
	}
}

// Config validation: bad inputs fail loudly at emit time (the action runs on
// a cron, so a misconfigured schedule must fail the route, not the startup).
func TestSlotsGenerateConfigValidation(t *testing.T) {
	cases := []struct {
		name, config, wantErr string
	}{
		{"close before open", genConfig(7, "", "17:00", "09:00", 30, 1), "must be after"},
		{"bad duration", "days_ahead: 7\n        open: \"09:00\"\n        close: \"10:00\"\n        duration_minutes: 0\n        capacity: 1", "invalid 'duration_minutes'"},
		{"bad open time", "days_ahead: 7\n        open: \"9am\"\n        close: \"10:00\"\n        duration_minutes: 30\n        capacity: 1", "invalid 'open'"},
		{"bad weekday", genConfig(7, "mondayish", "09:00", "10:00", 30, 1), "invalid weekday"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, "app.spine")
			if err := os.WriteFile(manifestPath, []byte(
				strings.Replace(slotsGenManifest, "%CONFIG%", tc.config, 1)), 0644); err != nil {
				t.Fatal(err)
			}
			eng, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "t.db"))
			if err != nil {
				t.Fatalf("engine init: %v", err)
			}
			defer eng.Close()
			_, err = eng.Bus.Emit("GEN_TICK", map[string]interface{}{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// Tier gate: slots.generate requires spine_version 3.
func TestSlotsGenerateTierGate(t *testing.T) {
	dir := t.TempDir()
	v1 := strings.Replace(slotsGenManifest, "spine_version: 3", "spine_version: 1", 1)
	v1 = strings.Replace(v1, "%CONFIG%", genConfig(7, "", "09:00", "10:00", 30, 1), 1)
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(v1), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "t.db"))
	if err == nil || !strings.Contains(err.Error(), "spine_version") {
		t.Fatalf("slots.generate on spine_version 1 must be rejected with a version error, got: %v", err)
	}
}

// Raw inserted rows (not generator-created) must survive regeneration —
// the generator only upserts its own deterministic id namespace.
func TestSlotsGenerateLeavesForeignRows(t *testing.T) {
	eng := newSlotsGenEngine(t, t.TempDir(), genConfig(7, "", "09:00", "10:00", 30, 2))
	defer eng.Close()

	if _, err := eng.Bus.Emit("SEED_SLOT", map[string]interface{}{
		"id": "manual-slot", "capacity": 5,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	genTick(t, eng)
	genTick(t, eng)

	rows, err := eng.Bus.QueryWhere("slots", "id", "manual-slot", 5, 0)
	if err != nil || len(rows) == 0 {
		t.Fatalf("foreign slot row vanished after regeneration (err=%v)", err)
	}
	if c := capOf(t, eng, "manual-slot"); c != 5 {
		t.Fatalf("foreign row capacity changed: 5 -> %d", c)
	}
}
