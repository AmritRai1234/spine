package tests

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// ---- Feature 1: QueryWhere filtering ----

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

	// Wait for batch writer flush
	time.Sleep(100 * time.Millisecond)

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
	time.Sleep(100 * time.Millisecond)

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

// ---- Feature 2: DB Accessor ----

func TestDBAccessor(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "db_accessor_test.db")
	spineFile := filepath.Join(tempDir, "db_accessor_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - items

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: ADD_ITEM
        payload:
          name: string

routes:
  - on: ADD_ITEM
    steps:
      - action: db.insert
        table: items
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// DB() should return non-nil
	db := engine.Bus.DB()
	if db == nil {
		t.Fatal("Bus.DB() returned nil")
	}

	// Insert via Spine, then query via raw DB()
	engine.Bus.Emit("ADD_ITEM", map[string]interface{}{"name": "widget"})
	time.Sleep(100 * time.Millisecond)

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&count)
	if err != nil {
		t.Fatalf("raw DB query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row via DB(), got %d", count)
	}

	// Verify same data visible via GetTableRows
	rows, _ := engine.Bus.GetTableRows("items", 10, 0)
	if len(rows) != count {
		t.Errorf("DB() and GetTableRows disagree: DB()=%d, GetTableRows=%d", count, len(rows))
	}
}

// ---- Feature 3: RouteStep Config Map ----

func TestRouteStepConfigParsing(t *testing.T) {
	tempDir := t.TempDir()
	spineFile := filepath.Join(tempDir, "config_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - jobs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: RUN_PIPELINE
        payload:
          project_id: string

routes:
  - on: RUN_PIPELINE
    steps:
      - action: pipeline.run
        channel: fast-lane
        priority: high
        timeout_sec: 30
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	schema, err := spine.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if len(schema.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(schema.Routes))
	}
	step := schema.Routes[0].Steps[0]

	if step.Action != "pipeline.run" {
		t.Errorf("expected action=pipeline.run, got %s", step.Action)
	}
	if step.Config == nil {
		t.Fatal("expected Config map to be populated, got nil")
	}
	if step.Config["channel"] != "fast-lane" {
		t.Errorf("expected Config[channel]=fast-lane, got %s", step.Config["channel"])
	}
	if step.Config["priority"] != "high" {
		t.Errorf("expected Config[priority]=high, got %s", step.Config["priority"])
	}
	if step.Config["timeout_sec"] != "30" {
		t.Errorf("expected Config[timeout_sec]=30, got %s", step.Config["timeout_sec"])
	}
}

func TestRouteStepConfigInAction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "config_action_test.db")
	spineFile := filepath.Join(tempDir, "config_action_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - jobs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: RUN_JOB
        payload:
          job_name: string

routes:
  - on: RUN_JOB
    steps:
      - action: custom.job
        queue: priority
        retries: 5
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Register custom action that reads Config
	var receivedQueue, receivedRetries string
	engine.Bus.RegisterAction("custom.job", func(step *spine.RouteStep, eventName string, payload map[string]interface{}) error {
		receivedQueue = step.Config["queue"]
		receivedRetries = step.Config["retries"]
		return nil
	})

	_, err = engine.Bus.Emit("RUN_JOB", map[string]interface{}{"job_name": "test-job"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	if receivedQueue != "priority" {
		t.Errorf("expected queue=priority, got %s", receivedQueue)
	}
	if receivedRetries != "5" {
		t.Errorf("expected retries=5, got %s", receivedRetries)
	}
}

// ---- Backwards Compatibility: known keys should NOT leak into Config ----

func TestRouteStepKnownKeysNotInConfig(t *testing.T) {
	tempDir := t.TempDir()
	spineFile := filepath.Join(tempDir, "known_keys_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - logs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: LOG_EVENT
        payload:
          msg: string

routes:
  - on: LOG_EVENT
    steps:
      - action: db.insert
        table: logs
        input: "$event.payload"
        where: "id = 1"
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	schema, err := spine.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	step := schema.Routes[0].Steps[0]

	// Known keys should be in their struct fields
	if step.Table != "logs" {
		t.Errorf("expected Table=logs, got %s", step.Table)
	}
	if step.Input != "$event.payload" {
		t.Errorf("expected Input=$event.payload, got %s", step.Input)
	}
	if step.Where != "id = 1" {
		t.Errorf("expected Where='id = 1', got %s", step.Where)
	}

	// Config should be nil — no unknown keys
	if step.Config != nil {
		t.Errorf("expected Config to be nil for known-keys-only step, got %v", step.Config)
	}
}
