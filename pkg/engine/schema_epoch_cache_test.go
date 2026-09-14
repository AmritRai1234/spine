package engine

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TDD RED for the schema-epoch column cache.
//
// Invariant under test: a cached column list for a table is NEVER served
// after the schema has evolved, because every evolution site bumps the
// schema epoch BEFORE returning, and tableColumns keys its cache on the
// epoch. Concretely:
//
//  1. Read columns → cache entry (epoch N) holds the pre-evolution list.
//  2. A dbInsert on a NEW column evolves the schema (ensureTable bumps
//     epoch to N+1 and must happen BEFORE the insert commits).
//  3. Read columns again → must reflect the NEW schema, never the stale
//     cached list.
//
// With a plain (non-epoch) cache, step 3 returns the old list — the exact
// stale-projection bug class this whole fix exists to close. This test
// fails on a naive cache, passes on the epoch cache, and also pins the
// "cache hit is transparent" path (same list back on repeat reads).

func newEpochTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	content := `spine_version: 1
database:
  tables:
    - orders
nodes:
  N:
    emits:
      - event: NEW_ORDER
        payload:
          email: string
routes:
  - on: NEW_ORDER
    steps:
      - action: db.insert
        table: orders
        sync: "true"
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewFromFile(manifestPath, filepath.Join(dir, "epoch.db"))
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestSchemaEpochCacheInvalidation(t *testing.T) {
	eng := newEpochTestEngine(t)
	bus := eng.Bus

	// Pre-create the table with base columns only (EnsureTables shape).
	if err := bus.EnsureTables([]string{"orders"}); err != nil {
		t.Fatal(err)
	}

	// Prime the cache at the pre-evolution schema.
	cols1, err := bus.tableColumns("orders")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols1 {
		if c == "email" {
			t.Fatalf("precondition failed: email already present: %v", cols1)
		}
	}

	// Cache hit path: same list, no error.
	colsAgain, err := bus.tableColumns("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(colsAgain) != len(cols1) {
		t.Fatalf("cache hit changed the list: %v vs %v", cols1, colsAgain)
	}

	// Evolve the schema the way dbInsert does (first-insert ALTER path).
	if _, err := bus.Emit("NEW_ORDER", map[string]interface{}{"email": "x@y.z"}); err != nil {
		t.Fatal(err)
	}

	// Post-evolution read MUST see the new column. A naive cache fails here.
	cols2, err := bus.tableColumns("orders")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cols2 {
		if c == "email" {
			found = true
		}
	}
	if !found {
		t.Fatalf("STALE CACHE: schema evolved but tableColumns still returns pre-evolution list: %v", cols2)
	}
}

// The epoch must be bumped by ensureTable even when called from other
// write paths (db.adjust, analytics schema, slots) — not only the
// db.insert site. Direct-probe variant: ensureTable with a brand-new
// column on an existing table.
func TestSchemaEpochBumpOnDirectEnsure(t *testing.T) {
	eng := newEpochTestEngine(t)
	bus := eng.Bus

	if err := bus.EnsureTables([]string{"orders"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.tableColumns("orders"); err != nil {
		t.Fatal(err)
	}

	if err := bus.ensureTable("orders", []string{`"votes" INTEGER`}); err != nil {
		t.Fatal(err)
	}

	cols, err := bus.tableColumns("orders")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cols {
		if c == "votes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("STALE CACHE after direct ensureTable evolution: %v", cols)
	}
}

// Epoch monotonically increases — the cache key must never repeat.
func TestSchemaEpochMonotonic(t *testing.T) {
	eng := newEpochTestEngine(t)
	bus := eng.Bus

	e1 := atomic.LoadUint64(&bus.schemaEpochCtr)
	if err := bus.ensureTable("orders", []string{`"a" TEXT`}); err != nil {
		t.Fatal(err)
	}
	e2 := atomic.LoadUint64(&bus.schemaEpochCtr)
	if err := bus.ensureTable("orders", []string{`"b" TEXT`}); err != nil {
		t.Fatal(err)
	}
	e3 := atomic.LoadUint64(&bus.schemaEpochCtr)

	if !(e1 < e2 && e2 < e3) {
		t.Fatalf("epoch not strictly increasing: %d, %d, %d", e1, e2, e3)
	}
}
