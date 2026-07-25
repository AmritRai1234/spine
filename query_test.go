package spine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryAPIAndEventAuditLog(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "query_test.db")
	spineFile := filepath.Join(tempDir, "query_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: USER_REGISTERED
        payload:
          email: string

routes:
  - on: USER_REGISTERED
    steps:
      - action: db.insert
        table: users
        input: "$event.payload"
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Emit 3 events
	for _, email := range []string{"alice@test.dev", "bob@test.dev", "charlie@test.dev"} {
		_, err := engine.Bus.Emit("USER_REGISTERED", map[string]interface{}{"email": email})
		if err != nil {
			t.Fatalf("failed to emit event for %s: %v", email, err)
		}
	}

	// Wait briefly for batch writer
	time.Sleep(50 * time.Millisecond)

	// Test 1: GetTables
	tables, err := engine.Bus.GetTables()
	if err != nil {
		t.Fatalf("GetTables failed: %v", err)
	}
	if len(tables) == 0 {
		t.Errorf("expected at least 1 table, got %d", len(tables))
	}

	// Test 2: GetTableRows
	rows, err := engine.Bus.GetTableRows("users", 10, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows in users table, got %d", len(rows))
	}

	// Test 3: GetEventLogs
	logs, err := engine.Bus.GetEventLogs("USER_REGISTERED", 10, 0)
	if err != nil {
		t.Fatalf("GetEventLogs failed: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 event audit logs, got %d", len(logs))
	}
}
