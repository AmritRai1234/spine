package tests

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// TestAccessEnvDotVarExpansion verifies that access keys accept the documented
// "$env.VAR" syntax in addition to the legacy "$VAR" syntax.
func TestAccessEnvDotVarExpansion(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

access:
  - role: env_user
    key: "$env.TEST_SPINE_DOTENV_KEY"

nodes:
  - name: api
    emits:
      - event: PING

routes:
  - on: PING
    steps:
      - action: log.write
        message: "pong"
`
	os.Setenv("TEST_SPINE_DOTENV_KEY", "sk_dotenv_value")
	defer os.Unsetenv("TEST_SPINE_DOTENV_KEY")

	manifestPath := filepath.Join(dir, "dotenv.spine")
	dbPath := filepath.Join(dir, "dotenv.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	handler := eng.HTTPHandler()

	// Should authenticate with the env var value
	rr := doEmit(handler, "sk_dotenv_value", "PING", map[string]interface{}{})
	if rr.Code != 200 {
		t.Errorf("$env.VAR key emit: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Should fail with wrong key
	rr = doEmit(handler, "wrong_key", "PING", map[string]interface{}{})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Wrong key with $env.VAR expansion: expected 401, got %d", rr.Code)
	}
}

// TestDbSumAction verifies db.sum aggregates a column and injects the result
// into the payload, including the empty-table zero case.
func TestDbSumAction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "sum_test.db")
	spineFile := filepath.Join(tempDir, "sum_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - expenses

nodes:
  TestNode:
    emits:
      - event: ADD_EXPENSE
        payload:
          category: string
          amount: number
      - event: CALC_TOTAL
      - event: CALC_FOOD

routes:
  - on: ADD_EXPENSE
    steps:
      - action: db.insert
        table: expenses

  - on: CALC_TOTAL
    steps:
      - action: db.sum
        table: expenses
        column: amount
        as: total
    emit: TOTAL_READY

  - on: CALC_FOOD
    steps:
      - action: db.sum
        table: expenses
        column: amount
        where: "category = 'food'"
        as: food_total
    emit: FOOD_READY
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Empty table sums to 0, not NULL
	if _, err := engine.Bus.Emit("CALC_TOTAL", map[string]interface{}{}); err != nil {
		t.Fatalf("CALC_TOTAL emit failed: %v", err)
	}
	state, ok := engine.Bus.GetState("TOTAL_READY")
	if !ok {
		t.Fatal("expected TOTAL_READY state in cache")
	}
	if total, _ := state["total"].(float64); total != 0 {
		t.Errorf("empty table sum: expected 0, got %v", state["total"])
	}

	// Insert expenses (batch writer is async — wait for flush)
	for _, e := range []map[string]interface{}{
		{"category": "food", "amount": 10.5},
		{"category": "food", "amount": 20.0},
		{"category": "office", "amount": 30.0},
	} {
		if _, err := engine.Bus.Emit("ADD_EXPENSE", e); err != nil {
			t.Fatalf("ADD_EXPENSE emit failed: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)

	if _, err := engine.Bus.Emit("CALC_TOTAL", map[string]interface{}{}); err != nil {
		t.Fatalf("CALC_TOTAL emit failed: %v", err)
	}
	state, ok = engine.Bus.GetState("TOTAL_READY")
	if !ok {
		t.Fatal("expected TOTAL_READY state in cache")
	}
	if total, _ := state["total"].(float64); total != 60.5 {
		t.Errorf("total sum: expected 60.5, got %v", state["total"])
	}

	// Filtered sum via parameterized where
	if _, err := engine.Bus.Emit("CALC_FOOD", map[string]interface{}{}); err != nil {
		t.Fatalf("CALC_FOOD emit failed: %v", err)
	}
	state, ok = engine.Bus.GetState("FOOD_READY")
	if !ok {
		t.Fatal("expected FOOD_READY state in cache")
	}
	if total, _ := state["food_total"].(float64); total != 30.5 {
		t.Errorf("food sum: expected 30.5, got %v", state["food_total"])
	}
}

// TestDbUpdateWithWhere verifies db.update honors an explicit where: clause
// and that template-interpolated values are parameterized (no SQL injection).
func TestDbUpdateWithWhere(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "update_where_test.db")
	spineFile := filepath.Join(tempDir, "update_where_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    emits:
      - event: ADD_USER
        payload:
          email: string
          name: string
      - event: RENAME_USER
        payload:
          email: string
          name: string

routes:
  - on: ADD_USER
    steps:
      - action: db.insert
        table: users

  - on: RENAME_USER
    steps:
      - action: db.update
        table: users
        where: "email = '$event.payload.email'"
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	engine.Bus.Emit("ADD_USER", map[string]interface{}{"email": "a@test.dev", "name": "alice"})
	engine.Bus.Emit("ADD_USER", map[string]interface{}{"email": "b@test.dev", "name": "bob"})
	time.Sleep(300 * time.Millisecond)

	// Update only alice's row via explicit where
	if _, err := engine.Bus.Emit("RENAME_USER", map[string]interface{}{"email": "a@test.dev", "name": "alice2"}); err != nil {
		t.Fatalf("RENAME_USER emit failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	rows, err := engine.Bus.GetTableRows("users", 10, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	names := map[string]string{}
	for _, r := range rows {
		email, _ := r["email"].(string)
		name, _ := r["name"].(string)
		names[email] = name
	}
	if names["a@test.dev"] != "alice2" {
		t.Errorf("expected alice2 after where update, got %q", names["a@test.dev"])
	}
	if names["b@test.dev"] != "bob" {
		t.Errorf("bob's row must be untouched, got %q", names["b@test.dev"])
	}
}

// TestDbUpdateIdFallback verifies the original db.update behavior (match on
// payload "id", no where clause) still works after the where: support change.
func TestDbUpdateIdFallback(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "update_fallback_test.db")
	spineFile := filepath.Join(tempDir, "update_fallback_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - items

nodes:
  TestNode:
    emits:
      - event: ADD_ITEM
        payload:
          id: string
          qty: number
      - event: UPDATE_ITEM
        payload:
          id: string
          qty: number

routes:
  - on: ADD_ITEM
    steps:
      - action: db.insert
        table: items

  - on: UPDATE_ITEM
    steps:
      - action: db.update
        table: items
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	engine.Bus.Emit("ADD_ITEM", map[string]interface{}{"id": "i1", "qty": 1.0})
	time.Sleep(300 * time.Millisecond)

	if _, err := engine.Bus.Emit("UPDATE_ITEM", map[string]interface{}{"id": "i1", "qty": 42.0}); err != nil {
		t.Fatalf("UPDATE_ITEM emit failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	rows, err := engine.Bus.GetTableRows("items", 10, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (update, not insert), got %d", len(rows))
	}
	if qty, _ := rows[0]["qty"].(float64); qty != 42.0 {
		t.Errorf("expected qty 42 after id-fallback update, got %v", rows[0]["qty"])
	}
}
