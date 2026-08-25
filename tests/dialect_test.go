package tests

// Dialect tests: verify per-backend SQL generation through the public API.
// The engine binds one dialect at Bus construction; these tests exercise the
// SQLite/libSQL path end-to-end (anonymous "?" placeholders, AUTOINCREMENT
// surrogate PK, ON CONFLICT upserts). Black-box equivalents of the former
// pkg/engine/dialect_test.go white-box suite.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

func newDialectEngine(t *testing.T) *spine.Engine {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "dialect.db")
	spineFile := filepath.Join(tempDir, "dialect.spine")

	manifest := `spine_version: 1

database:
  tables:
    - items

nodes:
  DialectNode:
    owns_files:
      - test.ts
    emits:
      - event: INSERT_ITEM
        payload:
          title: string
          votes: integer
      - event: UPDATE_ITEM
        payload:
          title: string
          votes: integer
      - event: UPSERT_ITEM
        payload:
          title: string
          votes: integer
      - event: DELETE_ITEM
        payload:
          title: string

routes:
  - on: INSERT_ITEM
    steps:
      - action: db.insert
        table: items

  - on: UPDATE_ITEM
    steps:
      - action: db.update
        table: items
        where: "title = '$event.payload.title'"

  - on: UPSERT_ITEM
    steps:
      - action: db.upsert
        table: items
        key: title

  - on: DELETE_ITEM
    steps:
      - action: db.delete
        table: items
        where: "title = '$event.payload.title'"
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

// TestSQLiteDialectInsertPipeline drives db.insert through a full emit and
// verifies persistence — proving the cached anonymous-placeholder templates
// produce valid executable SQL against the live database.
func TestSQLiteDialectInsertPipeline(t *testing.T) {
	engine := newDialectEngine(t)
	db := engine.Bus.DB()

	if _, err := engine.Bus.Emit("INSERT_ITEM", map[string]interface{}{"title": "x", "votes": 1}); err != nil {
		t.Fatalf("insert emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	var title string
	var votes int
	err := db.QueryRow(`SELECT "title", "votes" FROM "items" WHERE "title" = ?`, "x").Scan(&title, &votes)
	if err != nil {
		t.Fatalf("row query failed: %v", err)
	}
	if title != "x" || votes != 1 {
		t.Errorf("unexpected row: title=%q votes=%d, want x/1", title, votes)
	}
}

// TestSQLiteDialectUpdateWhere verifies the update template's parameterized
// WHERE clause targets exactly the matching row.
func TestSQLiteDialectUpdateWhere(t *testing.T) {
	engine := newDialectEngine(t)

	if _, err := engine.Bus.Emit("INSERT_ITEM", map[string]interface{}{"title": "x", "votes": 1}); err != nil {
		t.Fatalf("seed emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if _, err := engine.Bus.Emit("UPDATE_ITEM", map[string]interface{}{"title": "x", "votes": 2}); err != nil {
		t.Fatalf("update emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	rows, err := engine.Bus.GetTableRows("items", 10, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after update, got %d", len(rows))
	}
	if got := fmt.Sprintf("%v", rows[0]["votes"]); got != "2" {
		t.Errorf("votes after update = %v, want 2", got)
	}
}

// TestSQLiteDialectUpsertConflict verifies the ON CONFLICT clause: re-upserting
// the same conflict key updates instead of duplicating.
func TestSQLiteDialectUpsertConflict(t *testing.T) {
	engine := newDialectEngine(t)

	for i, votes := range []int{3, 7} {
		if _, err := engine.Bus.Emit("UPSERT_ITEM", map[string]interface{}{"title": "x", "votes": votes}); err != nil {
			t.Fatalf("upsert emit #%d failed: %v", i+1, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	rows, err := engine.Bus.GetTableRows("items", 10, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ON CONFLICT failed: expected 1 row, got %d", len(rows))
	}
	if got := fmt.Sprintf("%v", rows[0]["votes"]); got != "7" {
		t.Errorf("votes after conflict-update = %v, want 7", got)
	}
}

// TestSQLiteDialectDeleteWhere verifies the delete template removes only
// matching rows.
func TestSQLiteDialectDeleteWhere(t *testing.T) {
	engine := newDialectEngine(t)
	db := engine.Bus.DB()

	for _, title := range []string{"keep", "drop"} {
		if _, err := engine.Bus.Emit("INSERT_ITEM", map[string]interface{}{"title": title, "votes": 1}); err != nil {
			t.Fatalf("seed emit for %q failed: %v", title, err)
		}
	}
	time.Sleep(100 * time.Millisecond)

	if _, err := engine.Bus.Emit("DELETE_ITEM", map[string]interface{}{"title": "drop"}); err != nil {
		t.Fatalf("delete emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "items" WHERE "title" = ?`, "drop").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 'drop' row deleted, still %d remain", count)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&total); err != nil {
		t.Fatalf("total query failed: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 surviving row, got %d", total)
	}
}

// TestEnsureTableUsesDialectAutoPK inspects the materialized DDL to confirm
// ensureTable appends the dialect's auto-increment surrogate PK.
func TestEnsureTableUsesDialectAutoPK(t *testing.T) {
	engine := newDialectEngine(t)
	db := engine.Bus.DB()

	var sqlText string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pk_check'`).Scan(&sqlText)
	if err == nil {
		t.Fatal("table 'pk_check' should not exist unless explicitly declared")
	}

	// Materialize a fresh table through the write path.
	if _, err := engine.Bus.Emit("INSERT_ITEM", map[string]interface{}{"title": "pk", "votes": 0}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='items'`).Scan(&sqlText); err != nil {
		t.Fatalf("sqlite_master lookup failed: %v", err)
	}
	if !strings.Contains(sqlText, "_spine_id INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Errorf("table DDL missing sqlite auto PK:\n%s", sqlText)
	}
}
