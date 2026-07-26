package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// ==================== Feature 1: db.upsert ====================

func TestDbUpsert(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "upsert_test.db")
	spineFile := filepath.Join(tempDir, "upsert_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: SYNC_USER
        payload:
          email: string
          name: string

routes:
  - on: SYNC_USER
    steps:
      - action: db.upsert
        table: users
        key: email
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Insert first
	_, err = engine.Bus.Emit("SYNC_USER", map[string]interface{}{"email": "alice@test.dev", "name": "Alice"})
	if err != nil {
		t.Fatalf("first emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify 1 row
	rows, _ := engine.Bus.GetTableRows("users", 100, 0)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after insert, got %d", len(rows))
	}
	if rows[0]["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", rows[0]["name"])
	}

	// Upsert same email with different name
	_, err = engine.Bus.Emit("SYNC_USER", map[string]interface{}{"email": "alice@test.dev", "name": "Alice Updated"})
	if err != nil {
		t.Fatalf("upsert emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Should still be 1 row, with updated name
	rows, _ = engine.Bus.GetTableRows("users", 100, 0)
	if len(rows) != 1 {
		t.Errorf("expected 1 row after upsert, got %d", len(rows))
	}
	if rows[0]["name"] != "Alice Updated" {
		t.Errorf("expected name='Alice Updated', got %v", rows[0]["name"])
	}
}

func TestDbUpsertNewRow(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "upsert_new_test.db")
	spineFile := filepath.Join(tempDir, "upsert_new_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: SYNC_USER
        payload:
          email: string
          name: string

routes:
  - on: SYNC_USER
    steps:
      - action: db.upsert
        table: users
        key: email
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Insert two different users
	engine.Bus.Emit("SYNC_USER", map[string]interface{}{"email": "a@test.dev", "name": "A"})
	engine.Bus.Emit("SYNC_USER", map[string]interface{}{"email": "b@test.dev", "name": "B"})
	time.Sleep(100 * time.Millisecond)

	rows, _ := engine.Bus.GetTableRows("users", 100, 0)
	if len(rows) != 2 {
		t.Errorf("expected 2 rows for different keys, got %d", len(rows))
	}
}

// ==================== Feature 2: set action ====================

func TestSetFieldsBasic(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "set_test.db")
	spineFile := filepath.Join(tempDir, "set_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: CREATE_USER
        payload:
          name: string

routes:
  - on: CREATE_USER
    steps:
      - action: set
        source: web-signup
      - action: db.insert
        table: users
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	_, err = engine.Bus.Emit("CREATE_USER", map[string]interface{}{"name": "Jane"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	rows, _ := engine.Bus.GetTableRows("users", 10, 0)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["source"] != "web-signup" {
		t.Errorf("expected source=web-signup, got %v", rows[0]["source"])
	}
	if rows[0]["name"] != "Jane" {
		t.Errorf("expected name=Jane, got %v", rows[0]["name"])
	}
}

func TestSetFieldsWithVariables(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "set_vars_test.db")
	spineFile := filepath.Join(tempDir, "set_vars_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - records

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: CREATE_RECORD
        payload:
          title: string

routes:
  - on: CREATE_RECORD
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.insert
        table: records
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	_, err = engine.Bus.Emit("CREATE_RECORD", map[string]interface{}{"title": "Test"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	rows, _ := engine.Bus.GetTableRows("records", 10, 0)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// id should be a UUID (36 chars with dashes)
	id, ok := rows[0]["id"].(string)
	if !ok || len(id) < 30 {
		t.Errorf("expected UUID in id field, got %v", rows[0]["id"])
	}
	// created_at should be a timestamp string
	createdAt, ok := rows[0]["created_at"].(string)
	if !ok || len(createdAt) < 10 {
		t.Errorf("expected timestamp in created_at, got %v", rows[0]["created_at"])
	}
}

func TestSetFieldsOverwrite(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "set_overwrite_test.db")
	spineFile := filepath.Join(tempDir, "set_overwrite_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - items

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: CREATE_ITEM
        payload:
          status: string

routes:
  - on: CREATE_ITEM
    steps:
      - action: set
        status: forced-active
      - action: db.insert
        table: items
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Emit with status=pending, but set action should overwrite to forced-active
	_, err = engine.Bus.Emit("CREATE_ITEM", map[string]interface{}{"status": "pending"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	rows, _ := engine.Bus.GetTableRows("items", 10, 0)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["status"] != "forced-active" {
		t.Errorf("expected status=forced-active after set overwrite, got %v", rows[0]["status"])
	}
}

// ==================== Feature 3: Typed columns ====================

func TestTypedColumnsNumber(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "typed_num_test.db")
	spineFile := filepath.Join(tempDir, "typed_num_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - products

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_PRODUCT
        payload:
          name: string
          price: number
          in_stock: boolean

routes:
  - on: ADD_PRODUCT
    steps:
      - action: db.insert
        table: products
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	_, err = engine.Bus.Emit("ADD_PRODUCT", map[string]interface{}{
		"name": "Widget", "price": 19.99, "in_stock": true,
	})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify column types via PRAGMA
	db := engine.Bus.DB()
	pragmaRows, err := db.Query(`PRAGMA table_info("products")`)
	if err != nil {
		t.Fatalf("PRAGMA query failed: %v", err)
	}
	defer pragmaRows.Close()

	colTypes := make(map[string]string)
	for pragmaRows.Next() {
		var cid int
		var name, colType string
		var notnull int
		var dflt interface{}
		var pk int
		if err := pragmaRows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			continue
		}
		colTypes[name] = colType
	}

	if colTypes["name"] != "TEXT" {
		t.Errorf("expected name column type=TEXT, got %s", colTypes["name"])
	}
	if colTypes["price"] != "REAL" {
		t.Errorf("expected price column type=REAL, got %s", colTypes["price"])
	}
	if colTypes["in_stock"] != "INTEGER" {
		t.Errorf("expected in_stock column type=INTEGER, got %s", colTypes["in_stock"])
	}
}

func TestTypedColumnsSorting(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "typed_sort_test.db")
	spineFile := filepath.Join(tempDir, "typed_sort_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - scores

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_SCORE
        payload:
          player: string
          score: number

routes:
  - on: ADD_SCORE
    steps:
      - action: db.insert
        table: scores
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Insert scores that would sort wrong as text: 9 > 100 lexicographically
	for _, s := range []map[string]interface{}{
		{"player": "a", "score": 100.0},
		{"player": "b", "score": 9.0},
		{"player": "c", "score": 50.0},
	} {
		engine.Bus.Emit("ADD_SCORE", s)
	}
	time.Sleep(100 * time.Millisecond)

	// Query sorted by score descending — with REAL type, 100 > 50 > 9
	db := engine.Bus.DB()
	rows, err := db.Query(`SELECT player, score FROM "scores" ORDER BY score DESC`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	var players []string
	for rows.Next() {
		var player string
		var score float64
		rows.Scan(&player, &score)
		players = append(players, player)
	}

	if len(players) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(players))
	}
	// With numeric sorting: a(100), c(50), b(9)
	if players[0] != "a" || players[1] != "c" || players[2] != "b" {
		t.Errorf("expected numeric sort order [a,c,b], got %v", players)
	}
}

func TestTypedColumnsDefaultText(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "typed_default_test.db")
	spineFile := filepath.Join(tempDir, "typed_default_test.spine")

	// No payload types declared — all columns should default to TEXT
	manifest := `spine_version: 1
database:
  tables:
    - logs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_LOG

routes:
  - on: ADD_LOG
    steps:
      - action: db.insert
        table: logs
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	_, err = engine.Bus.Emit("ADD_LOG", map[string]interface{}{"message": "hello", "level": "info"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	db := engine.Bus.DB()
	pragmaRows, err := db.Query(`PRAGMA table_info("logs")`)
	if err != nil {
		t.Fatalf("PRAGMA query failed: %v", err)
	}
	defer pragmaRows.Close()

	for pragmaRows.Next() {
		var cid int
		var name, colType string
		var notnull int
		var dflt interface{}
		var pk int
		pragmaRows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk)
		if name != "_spine_id" && colType != "TEXT" {
			t.Errorf("expected column %s to be TEXT (no type declared), got %s", name, colType)
		}
	}
}
