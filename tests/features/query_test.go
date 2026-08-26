package features

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
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

	engine, err := spine.NewFromFile(spineFile, dbPath)
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

	// Wait for batch writer (poll — fixed sleeps are flaky under load)
	waitForTableRows(t, engine, "users", 3)

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

func TestQueryWhereFiltering(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "where_test.db")
	spineFile := filepath.Join(tempDir, "where_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - clips

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_CLIP
        payload:
          project_id: string
          title: string

routes:
  - on: ADD_CLIP
    steps:
      - action: db.insert
        table: clips
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Insert clips across two projects
	clips := []map[string]interface{}{
		{"project_id": "proj_a", "title": "clip1"},
		{"project_id": "proj_a", "title": "clip2"},
		{"project_id": "proj_b", "title": "clip3"},
		{"project_id": "proj_a", "title": "clip4"},
		{"project_id": "proj_b", "title": "clip5"},
	}
	for _, c := range clips {
		if _, err := engine.Bus.Emit("ADD_CLIP", c); err != nil {
			t.Fatalf("failed to emit: %v", err)
		}
	}

	// Wait for batch writer flush (poll)
	waitForTableRows(t, engine, "clips", 5)

	// QueryWhere for project_a — should return 3 rows
	rows, err := engine.Bus.QueryWhere("clips", "project_id", "proj_a", 100, 0)
	if err != nil {
		t.Fatalf("QueryWhere failed: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows for proj_a, got %d", len(rows))
	}
	for _, r := range rows {
		if r["project_id"] != "proj_a" {
			t.Errorf("expected project_id=proj_a, got %v", r["project_id"])
		}
	}

	// QueryWhere for project_b — should return 2 rows
	rows, err = engine.Bus.QueryWhere("clips", "project_id", "proj_b", 100, 0)
	if err != nil {
		t.Fatalf("QueryWhere failed: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows for proj_b, got %d", len(rows))
	}

	// QueryWhere for non-existent value — should return 0 rows
	rows, err = engine.Bus.QueryWhere("clips", "project_id", "proj_z", 100, 0)
	if err != nil {
		t.Fatalf("QueryWhere failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for proj_z, got %d", len(rows))
	}
}

func TestQueryWhereHTTPEndpoint(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "where_http_test.db")
	spineFile := filepath.Join(tempDir, "where_http_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - tasks

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_TASK
        payload:
          status: string
          name: string

routes:
  - on: ADD_TASK
    steps:
      - action: db.insert
        table: tasks
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Insert tasks with different statuses
	for _, task := range []map[string]interface{}{
		{"status": "active", "name": "task1"},
		{"status": "done", "name": "task2"},
		{"status": "active", "name": "task3"},
	} {
		engine.Bus.Emit("ADD_TASK", task)
	}
	waitForTableRows(t, engine, "tasks", 3)

	handler := engine.HTTPHandler()

	// Test filtered request
	req := httptest.NewRequest("GET", "/tables/tasks?where=status:active", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("expected 2 active tasks, got %d", count)
	}

	// Test unfiltered request still works
	req2 := httptest.NewRequest("GET", "/tables/tasks", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	var resp2 map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&resp2)
	count2 := int(resp2["count"].(float64))
	if count2 != 3 {
		t.Errorf("expected 3 total tasks, got %d", count2)
	}
}
