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

// db.upsert composite-key (constraint) test suite. Semantics under test:
//
//   - single-column key  → identity: ON CONFLICT DO UPDATE (merge) — legacy path
//   - multi-column key   → constraint: ON CONFLICT DO NOTHING + synchronous
//     rejection so the route's on_failure fires (booking / one-per-customer)
//
// The booking flow doubles as an integration test of adjust-then-upsert with
// compensating capacity release: db.adjust (sync, floor-guarded) claims the
// seat, the composite upsert enforces one-booking-per-customer-per-slot, and
// the SLOT_TAKEN → RELEASE_SLOT chain restores capacity when either guard
// rejects.
const compositeManifest = `spine_version: 3

database:
  tables:
    - slots
    - bookings
    - customers

nodes:
  Booking:
    emits:
      - event: SEED_SLOT
        payload:
          id: string
          capacity: integer
      - event: SEED_BOOKING
      - event: SEED_CUSTOMER
      - event: BOOK_SLOT
        payload:
          slot_id: string
          customer_email: string
      - event: CANCEL_BOOKING
    listens:
      - state: BOOKING_CONFIRMED
      - state: SLOT_TAKEN

routes:
  - on: SEED_SLOT
    steps:
      - action: db.insert
        table: slots

  - on: SEED_BOOKING
    steps:
      - action: db.insert
        table: bookings

  - on: SEED_CUSTOMER
    steps:
      - action: db.upsert
        table: customers
        key: email

  - on: BOOK_SLOT
    on_failure: SLOT_TAKEN
    steps:
      # Adjust floor-reject = seat never taken → route-level SLOT_TAKEN, NO
      # compensation. Step-level on_failure below ONLY covers the upsert
      # conflict (seat taken but customer already booked) → release it.
      - action: db.adjust
        table: slots
        where: "id = $event.payload.slot_id"
        column: capacity
        by: -1
        floor: 0
      - action: set
        id: $uuid
        created_at: $now
      - action: db.upsert
        table: bookings
        on_failure: RELEASE_SEAT
        key:
          - slot_id
          - customer_email
    emit: BOOKING_CONFIRMED

  # Compensation: upsert conflicted AFTER the seat was claimed → give it back.
  - on: RELEASE_SEAT
    steps:
      - action: db.adjust
        table: slots
        where: "id = $event.payload.slot_id"
        column: capacity
        by: 1
    emit: SLOT_TAKEN

  - on: CANCEL_BOOKING
    steps:
      - action: db.update
        table: bookings
        where: "id = $event.payload.booking_id"
      - action: db.adjust
        table: slots
        where: "id = $event.payload.slot_id"
        column: capacity
        by: 1
    emit: BOOKING_CANCELLED
`

func newCompositeEngine(t *testing.T, dir string) *spine.Engine {
	t.Helper()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")
	if err := os.WriteFile(manifestPath, []byte(compositeManifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	return eng
}

func seedSlot(t *testing.T, eng *spine.Engine, id string, capacity int) {
	t.Helper()
	if _, err := eng.Bus.Emit("SEED_SLOT", map[string]interface{}{
		"id": id, "capacity": capacity,
	}); err != nil {
		t.Fatalf("seed slot %s: %v", id, err)
	}
}

// slotCapacity reads capacity directly from the DB pool (the batched writer
// flushes before these reads in tests via flushWrites below).
func slotCapacity(t *testing.T, eng *spine.Engine, id string) int {
	t.Helper()
	rows, err := eng.Bus.QueryWhere("slots", "id", id, 10, 0)
	if err != nil {
		t.Fatalf("query slots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 slot row for %s, got %d", id, len(rows))
	}
	switch v := rows[0]["capacity"].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	default:
		t.Fatalf("unexpected capacity type %T for slot %s", rows[0]["capacity"], id)
		return 0
	}
}

func bookingCount(t *testing.T, eng *spine.Engine, slotID, email string) int {
	t.Helper()
	rows, err := eng.Bus.QueryWhere("bookings", "slot_id", slotID, 1000, 0)
	if err != nil {
		t.Fatalf("query bookings: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r["customer_email"] == email {
			n++
		}
	}
	return n
}

func flushWrites() { time.Sleep(300 * time.Millisecond) }

// 1. Same customer books the same slot twice → second attempt rejected via
//    on_failure, exactly one booking row, capacity decremented exactly once.
func TestCompositeUpsertRejectsDuplicateBooking(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	seedSlot(t, eng, "s1", 5)
	flushWrites()

	res, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s1", "customer_email": "a@example.com",
	})
	if err != nil {
		t.Fatalf("first booking failed: %v", err)
	}
	if states, ok := res["emitted_states"].([]string); !ok || !containsState(states, "BOOKING_CONFIRMED") {
		t.Fatalf("first booking should confirm, got %v", res)
	}

	res2, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s1", "customer_email": "a@example.com",
	})
	if err == nil {
		t.Fatalf("second booking must be rejected, got success: %v", res2)
	}
	if res2 != nil {
		if states, ok := res2["emitted_states"].([]string); !ok || !containsState(states, "SLOT_TAKEN") {
			t.Fatalf("second booking should route through SLOT_TAKEN, got %v", res2)
		}
	}
	if !strings.Contains(fmt.Sprint(err), "already exists") {
		t.Fatalf("rejection should name the composite conflict, got: %v", err)
	}

	flushWrites()
	if n := bookingCount(t, eng, "s1", "a@example.com"); n != 1 {
		t.Fatalf("expected exactly 1 booking row, got %d", n)
	}
	if got := slotCapacity(t, eng, "s1"); got != 4 {
		t.Fatalf("capacity should be net 4 after book+compensate, got %d", got)
	}
}

// 2. Different customers, one seat → second hits the adjust floor (seat was
//    taken by the winner, so NO compensation fires — floor-losers never held
//    a seat). Capacity stays at 0: the winner owns the seat.
func TestCompositeFlowFloorRejectRestoresCapacity(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	seedSlot(t, eng, "s2", 1)
	flushWrites()

	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s2", "customer_email": "a@example.com",
	}); err != nil {
		t.Fatalf("first booking failed: %v", err)
	}
	_, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s2", "customer_email": "b@example.com",
	})
	if err == nil {
		t.Fatal("second booking must fail — slot is full")
	}

	flushWrites()
	if n := bookingCount(t, eng, "s2", "b@example.com"); n != 0 {
		t.Fatalf("rejected customer must have no booking row, got %d", n)
	}
	if got := slotCapacity(t, eng, "s2"); got != 0 {
		t.Fatalf("capacity must stay 0 (winner holds the seat; floor-losers never compensate), got %d", got)
	}
}

// 3. Headline test — concurrent race on a 1-seat slot. Exactly one booker
//    wins and HOLDS the seat (capacity 0); floor-losers do not compensate;
//    upsert-conflict-losers (impossible on 1 seat) would.
func TestCompositeFlowConcurrentRace(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	seedSlot(t, eng, "s3", 1)
	flushWrites()

	const racers = 8
	type outcome struct {
		email string
		err   error
	}
	results := make(chan outcome, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			email := fmt.Sprintf("racer%d@example.com", i)
			<-start
			_, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
				"slot_id": "s3", "customer_email": email,
			})
			results <- outcome{email: email, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	wins := 0
	for r := range results {
		if r.err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one racer must win a 1-seat slot, got %d winners", wins)
	}

	flushWrites()
	if got := slotCapacity(t, eng, "s3"); got != 0 {
		t.Fatalf("capacity must be 0 (the single winner holds the seat), got %d", got)
	}
	rows, err := eng.Bus.QueryWhere("bookings", "slot_id", "s3", 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("exactly one booking row must exist, got %d", len(rows))
	}
}

// 3b. The scarier interleaving: a 2-way composite-conflict race on a
//     multi-seat slot where both losers run RELEASE_SLOT compensation while
//     a third booker is claiming. Invariant: no lost or phantom capacity —
//     final capacity == initial - successful bookings.
func TestCompositeFlowRaceWithOverlappingCompensation(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	// 3 seats, ONE customer identity racing — every claim after the first
	// upsert-conflicts and compensates, while concurrent attempts are
	// mid-adjust. Capacity must land exactly at 3-1=2.
	seedSlot(t, eng, "s4", 3)
	flushWrites()

	const attempts = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Same slot + same email: at most ONE of these can succeed.
			_, _ = eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
				"slot_id": "s4", "customer_email": "solo@example.com",
			})
		}()
	}
	close(start)
	wg.Wait()

	flushWrites()
	if n := bookingCount(t, eng, "s4", "solo@example.com"); n != 1 {
		t.Fatalf("constraint allows exactly 1 booking for the identity, got %d", n)
	}
	if got := slotCapacity(t, eng, "s4"); got != 2 {
		t.Fatalf("capacity must be exactly 2 (3 seats - 1 booking, every failed claim compensated), got %d", got)
	}
}

// 4. Sequential full-slot flow through cancel: book, cancel, capacity back,
//    then a new customer can claim the freed seat.
func TestCompositeFlowCancelFreesSeat(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	seedSlot(t, eng, "s5", 1)
	flushWrites()

	res, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s5", "customer_email": "a@example.com",
	})
	if err != nil {
		t.Fatalf("booking failed: %v", err)
	}
	bookingID := ""
	// The confirmed state broadcast carries the route payload incl. booking id.
	if states, ok := res["emitted_states"].([]string); !ok || !containsState(states, "BOOKING_CONFIRMED") {
		t.Fatalf("expected BOOKING_CONFIRMED, got %v", res)
	}
	rows, err := eng.Bus.QueryWhere("bookings", "slot_id", "s5", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 booking row, got %d (err=%v)", len(rows), err)
	}
	if id, ok := rows[0]["id"].(string); ok {
		bookingID = id
	}

	flushWrites()
	// Cancel: payload = new column values + fields the where targets.
	if _, err := eng.Bus.Emit("CANCEL_BOOKING", map[string]interface{}{
		"booking_id": bookingID, "slot_id": "s5", "status": "cancelled",
	}); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	flushWrites()

	if got := slotCapacity(t, eng, "s5"); got != 1 {
		t.Fatalf("capacity must return to 1 after cancel, got %d", got)
	}

	// Freed seat is claimable by a new customer.
	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s5", "customer_email": "b@example.com",
	}); err != nil {
		t.Fatalf("new customer must claim the freed seat: %v", err)
	}
}

// 5. Single-key regression: legacy `key: email` upserts still MERGE on
//    conflict (identity semantics unchanged) and stay on the batched writer.
func TestSingleKeyUpsertStillMerges(t *testing.T) {
	eng := newCompositeEngine(t, t.TempDir())
	defer eng.Close()

	if _, err := eng.Bus.Emit("SEED_CUSTOMER", map[string]interface{}{
		"email": "x@example.com", "name": "Original",
	}); err != nil {
		t.Fatal(err)
	}
	flushWrites()
	if _, err := eng.Bus.Emit("SEED_CUSTOMER", map[string]interface{}{
		"email": "x@example.com", "name": "Updated",
	}); err != nil {
		t.Fatal(err)
	}
	flushWrites()

	rows, err := eng.Bus.QueryWhere("customers", "email", "x@example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("identity upsert must merge to 1 row, got %d", len(rows))
	}
	if name, _ := rows[0]["name"].(string); name != "Updated" {
		t.Fatalf("merge should keep the newest values, got name=%v", rows[0]["name"])
	}
}

// 6. Literal "a,b" scalar form parses to the same composite semantics as the
//    list form (parity check for the comma-joined Config representation).
func TestCompositeScalarSyntaxEquivalence(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	scalar := strings.Replace(compositeManifest,
		"        key:\n          - slot_id\n          - customer_email",
		"        key: slot_id,customer_email", 1)
	if scalar == compositeManifest {
		t.Fatal("scalar substitution did not apply — manifest anchor drifted")
	}
	if err := os.WriteFile(manifestPath, []byte(scalar), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	defer eng.Close()

	seedSlot(t, eng, "s6", 5)
	flushWrites()

	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s6", "customer_email": "a@example.com",
	}); err != nil {
		t.Fatalf("first booking failed: %v", err)
	}
	if _, err := eng.Bus.Emit("BOOK_SLOT", map[string]interface{}{
		"slot_id": "s6", "customer_email": "a@example.com",
	}); err == nil {
		t.Fatal("scalar-syntax composite key must also reject duplicates")
	}
}

func containsState(states []string, want string) bool {
	for _, s := range states {
		if s == want {
			return true
		}
	}
	return false
}
