package tests

// Write-path semantic regression tests (Batch C):
//   - P1-4: batch writer counts permanently failing statements instead of
//     silently dropping them, and good writes in the same batch survive.
//   - P1-6: db.delete's no-where fallback is deterministic (sorted keys).
//   - P1-8: idempotent emits flush the writer before marking 'completed'
//     (no cached-ok for writes that never committed).
//   - P1-9: saga compensation runs after the writes it undoes have committed.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// TestStmtFailuresAreCountedNotSilent: a permanently failing async write
// (PK collision on _spine_id) must be counted in StmtFailures and must not
// take down the good writes in the same or later batches. Previously such
// statements were logged-and-continued and the tx still committed — silently
// dropping data with no metric.
func TestStmtFailuresAreCountedNotSilent(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "pk.spine", `spine_version: 1
database:
  tables:
    - events
nodes:
  - name: N
    emits:
      - event: PK_COLLIDE
        payload:
          note: string
routes:
  - on: PK_COLLIDE
    steps:
      - action: db.insert
        table: events
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "pk.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	before := eng.Bus.StmtFailures()

	// Good write, bad duplicate, good write.
	for _, p := range []map[string]interface{}{
		{"_spine_id": 1, "note": "first"},
		{"_spine_id": 1, "note": "duplicate"},
		{"_spine_id": 2, "note": "third"},
	} {
		if _, err := eng.Bus.Emit("PK_COLLIDE", p); err != nil {
			t.Fatalf("emit failed: %v", err)
		}
	}

	waitUntil(t, "both good rows committed", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
		return n == 2
	})

	if after := eng.Bus.StmtFailures(); after <= before {
		t.Errorf("expected StmtFailures to increase (before=%d after=%d) — failing statement was not counted", before, after)
	}

	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 2 {
		t.Errorf("expected the two good writes to commit, got %d rows", n)
	}
	var notes []string
	rows, err := eng.Bus.DB().Query(`SELECT note FROM events ORDER BY _spine_id`)
	if err != nil {
		t.Fatalf("query notes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, note)
	}
	if len(notes) != 2 || notes[0] != "first" || notes[1] != "third" {
		t.Errorf("unexpected surviving rows: %v", notes)
	}
}

// TestDeleteFallbackIsDeterministic: db.delete without 'where'/'id' must pick
// its match column deterministically (sorted keys, like db.update). Iterating
// a Go map directly is randomized per call — the same payload could delete by
// a different column (and a different row) on every attempt.
func TestDeleteFallbackIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "del.spine", `spine_version: 1
database:
  tables:
    - items
nodes:
  - name: N
    emits:
      - event: ADD_ITEM
        payload:
          amount: number
          status: string
      - event: DEL_ITEM
        payload:
          amount: number
          status: string
routes:
  - on: ADD_ITEM
    steps:
      - action: db.insert
        table: items
        sync: "true"
  - on: DEL_ITEM
    steps:
      - action: db.delete
        table: items
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "del.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	for _, p := range []map[string]interface{}{
		{"amount": 1, "status": "x"},
		{"amount": 2, "status": "x"},
	} {
		if _, err := eng.Bus.Emit("ADD_ITEM", p); err != nil {
			t.Fatalf("add emit failed: %v", err)
		}
	}

	del := map[string]interface{}{"amount": 1, "status": "x"}
	// Sorted keys pick "amount" first — deletes exactly the amount=1 row.
	if _, err := eng.Bus.Emit("DEL_ITEM", del); err != nil {
		t.Fatalf("delete emit failed: %v", err)
	}
	// Repeating the same delete must be a no-op (amount=1 is already gone) —
	// never delete by "status" (which would take out both rows).
	if _, err := eng.Bus.Emit("DEL_ITEM", del); err != nil {
		t.Fatalf("second delete emit failed: %v", err)
	}

	waitUntil(t, "delete settles", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
		return n == 1
	})

	var amount float64
	if err := eng.Bus.DB().QueryRow(`SELECT amount FROM items`).Scan(&amount); err != nil {
		t.Fatalf("read remaining row: %v", err)
	}
	if amount != 2 {
		t.Errorf("expected the amount=2 row to survive, remaining amount=%v", amount)
	}
}

// TestIdempotentEmitFlushesBeforeCompletion: when an emit carries an
// _idempotency_key, the writer must be flushed before the idempotency row is
// marked 'completed' — otherwise a crash would replay a cached 'ok' for an
// event whose writes never committed.
func TestIdempotentEmitFlushesBeforeCompletion(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "idem.spine", `spine_version: 1
database:
  tables:
    - notes
nodes:
  - name: N
    emits:
      - event: ADD_NOTE
        payload:
          note: string
routes:
  - on: ADD_NOTE
    steps:
      - action: db.insert
        table: notes
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "idem.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	// NOTE: the insert is deliberately NOT sync — the durability fence inside
	// the idempotency path is what makes the row visible immediately.
	payload := map[string]interface{}{"_idempotency_key": "k-1", "note": "hello"}
	if _, err := eng.Bus.Emit("ADD_NOTE", payload); err != nil {
		t.Fatalf("idempotent emit failed: %v", err)
	}

	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		t.Fatalf("count notes: %v", err)
	}
	if n != 1 {
		t.Errorf("expected the write to be durable immediately after the idempotent emit (flush fence), got %d rows", n)
	}

	// Replay with the same key returns the cached result and inserts nothing.
	if _, err := eng.Bus.Emit("ADD_NOTE", payload); err != nil {
		t.Fatalf("replayed emit failed: %v", err)
	}
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		t.Fatalf("count notes: %v", err)
	}
	if n != 1 {
		t.Errorf("replayed idempotent emit must not insert again, got %d rows", n)
	}
}

// TestCompensationRunsAfterOriginalWrite: saga compensation must run AFTER
// the async writes it undoes have committed. Previously both the insert and
// the compensating delete went through the sharded writer as independent
// async tasks, so the delete could commit before the insert — leaving the
// "rolled back" row in the final state.
func TestCompensationRunsAfterOriginalWrite(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "saga.spine", `spine_version: 1
database:
  tables:
    - orders
nodes:
  - name: N
    emits:
      - event: PLACE_ORDER
        payload:
          order_id: string
          total: number
routes:
  - on: PLACE_ORDER
    steps:
      - action: db.insert
        table: orders
        compensate: db.delete
      - action: assert
        condition: "1 == 2"
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "saga.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	if _, err := eng.Bus.Emit("PLACE_ORDER", map[string]interface{}{"order_id": "o-1", "total": 50.0}); err == nil {
		t.Fatal("expected emit to fail (assert always fails), got nil error")
	}

	// The compensating db.delete must have removed the row the insert wrote.
	// (db.delete's no-where fallback matches the sorted-first payload key,
	// order_id, which is the row that was inserted.)
	waitUntil(t, "compensation settles", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&n)
		return n == 0
	})

	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if n != 0 {
		t.Errorf("compensation did not remove the original write — %d rows remain (ordering race?)", n)
	}

	// Give any straggler async writes a moment: if the original insert commits
	// AFTER compensation, it would reappear here.
	time.Sleep(300 * time.Millisecond)
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if n != 0 {
		t.Errorf("original write reappeared after compensation (%d rows) — compensation ran before the write it undoes", n)
	}
}

// TestUnknownDeclaredTypeRejected: a typo'd payload field type ("stringg")
// must fail emits loudly instead of silently disabling validation.
func TestUnknownDeclaredTypeRejected(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "badtype.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: BAD_TYPED
        payload:
          note: stringg
routes:
  - on: BAD_TYPED
    steps:
      - action: db.insert
        table: data
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "badtype.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	_, err = eng.Bus.Emit("BAD_TYPED", map[string]interface{}{"note": "hello"})
	if err == nil {
		t.Fatal("emit with unknown declared type must fail, got nil error")
	}
	if !strings.Contains(err.Error(), "unknown declared type") {
		t.Errorf("expected unknown-declared-type error, got: %v", err)
	}
}

// TestInlineCommentContractEnforced: with the comment-stripping fix, a
// declared `email: string # comment` contract is REAL again — a non-string
// value must be rejected at emit time (previously the comment corrupted the
// type and disabled validation end-to-end).
func TestInlineCommentContractEnforced(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "comment2.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: COMMENT_TYPED
        payload:
          email: string # primary contact
routes:
  - on: COMMENT_TYPED
    steps:
      - action: db.insert
        table: data
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "comment2.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	// Correct type passes.
	if _, err := eng.Bus.Emit("COMMENT_TYPED", map[string]interface{}{"email": "a@b.dev"}); err != nil {
		t.Fatalf("valid emit must pass, got: %v", err)
	}
	// Wrong type must now be rejected (contract enforced).
	if _, err := eng.Bus.Emit("COMMENT_TYPED", map[string]interface{}{"email": 123}); err == nil {
		t.Error("non-string value for a string contract must be rejected, got nil error")
	}
}

// TestBooleanColumnsStoreZeroOne: declared boolean fields bind as 0/1 on
// every backend (the Postgres driver cannot encode a Go bool into the INTEGER
// column sqliteType maps booleans to; SQLite only converted by accident).
func TestBooleanColumnsStoreZeroOne(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "bool.spine", `spine_version: 1
database:
  tables:
    - flags
nodes:
  N:
    emits:
      - event: SET_FLAG
        payload:
          flag: boolean
routes:
  - on: SET_FLAG
    steps:
      - action: db.insert
        table: flags
        sync: "true"
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "bool.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	for _, tc := range []struct {
		val  bool
		want int64
	}{
		{true, 1},
		{false, 0},
	} {
		if _, err := eng.Bus.Emit("SET_FLAG", map[string]interface{}{"flag": tc.val}); err != nil {
			t.Fatalf("emit flag=%v failed: %v", tc.val, err)
		}
		var stored int64
		if err := eng.Bus.DB().QueryRow(`SELECT "flag" FROM flags ORDER BY _spine_id DESC LIMIT 1`).Scan(&stored); err != nil {
			t.Fatalf("read flag: %v", err)
		}
		if stored != tc.want {
			t.Errorf("flag=%v stored as %d, want %d", tc.val, stored, tc.want)
		}
	}
}

// TestCompoundWhereRejected: "a = 1 AND b = 2" must fail loudly instead of
// silently binding "1 AND b = 2" as one parameter and matching nothing.
func TestCompoundWhereRejected(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "compound.spine", `spine_version: 1
database:
  tables:
    - items
nodes:
  N:
    emits:
      - event: UPDATE_ITEMS
        payload:
          status: string
          amount: number
routes:
  - on: UPDATE_ITEMS
    steps:
      - action: db.update
        table: items
        where: "status = 'a' AND amount = 5"
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "compound.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	_, err = eng.Bus.Emit("UPDATE_ITEMS", map[string]interface{}{"status": "a", "amount": 5.0})
	if err == nil {
		t.Fatal("compound where must be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "compound") {
		t.Errorf("expected compound-where error, got: %v", err)
	}
}

// TestQuotedAndValueAllowed: "fish AND chips" INSIDE a quoted value is a plain
// value, not a compound condition.
func TestQuotedAndValueAllowed(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "quotedand.spine", `spine_version: 1
database:
  tables:
    - items
nodes:
  N:
    emits:
      - event: SUM_ITEMS
        payload:
          title: string
routes:
  - on: SUM_ITEMS
    steps:
      - action: db.sum
        table: items
        column: amount
        where: "title = 'fish AND chips'"
        as: total
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "quotedand.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	if _, err := eng.Bus.Emit("SUM_ITEMS", map[string]interface{}{"title": "fish AND chips"}); err != nil {
		t.Fatalf("quoted AND value must be accepted, got: %v", err)
	}
}

// TestWhereMissingFieldMatchesNothing: an unresolvable $event.payload path in
// a where clause means NO ROWS qualify (the documented optional-field
// pattern) — it must never error (breaks optional fields) and never silently
// match '' (matches the wrong rows).
func TestWhereMissingFieldMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, "missing.spine", `spine_version: 1
database:
  tables:
    - items
nodes:
  N:
    emits:
      - event: DEL_ITEMS
        payload:
          note: string
      - event: ADD_ITEMS
        payload:
          note: string
routes:
  - on: ADD_ITEMS
    steps:
      - action: db.insert
        table: items
        sync: "true"
  - on: DEL_ITEMS
    steps:
      - action: db.delete
        table: items
        where: "note = '$event.payload.missing_field'"
`)
	eng, err := spine.NewFromFile(manifest, filepath.Join(dir, "missing.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	// Seed a row that a literal '' match would have deleted.
	if _, err := eng.Bus.Emit("ADD_ITEMS", map[string]interface{}{"note": ""}); err != nil {
		t.Fatalf("seed emit failed: %v", err)
	}

	// The where references a field the payload does not carry: the delete
	// must succeed as a no-op (no rows qualify), NOT delete the '' row.
	if _, err := eng.Bus.Emit("DEL_ITEMS", map[string]interface{}{"note": "x"}); err != nil {
		t.Fatalf("delete with unresolvable where-field must be a no-op, got: %v", err)
	}

	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 1 {
		t.Errorf("expected the '' row to survive (unresolvable where must match nothing), got %d rows", n)
	}
}
