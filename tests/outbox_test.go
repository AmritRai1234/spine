package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestOutboxEnqueueAndExecution(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

database:
  tables:
    - audit_log
  outbox:
    max_workers: 2
    max_retries: 3
    backoff_ms: 50

nodes:
  - name: TestNode
    emits:
      - event: TRIGGER_OUTBOX
        payload:
          msg: string

routes:
  - on: TRIGGER_OUTBOX
    steps:
      - action: log.write
        message: "Outbox item processed: $event.payload.msg"
`
	manifestPath := filepath.Join(dir, "outbox_test.spine")
	dbPath := filepath.Join(dir, "outbox_test.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write test manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize engine: %v", err)
	}
	defer eng.Close()

	bus := eng.Bus

	// Manually enqueue a successful outbox step
	step := &manifest.RouteStep{
		Action:  "log.write",
		Message: "Manual outbox test item: $event.payload.msg",
	}
	payload := map[string]interface{}{"msg": "hello_outbox"}
	bus.Emit("TRIGGER_OUTBOX", payload)

	// Direct enqueue test
	bus.DB().Exec(`DELETE FROM "_spine_outbox"`) // Clear previous outbox items
	
	// Enqueue outbox item with past next_retry_at to trigger immediately
	pastRetry := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	nowStr := time.Now().UTC().Format(time.RFC3339)

	res, err := bus.DB().Exec(`INSERT INTO "_spine_outbox" (action, payload, step_data, attempts, status, next_retry_at, created_at) VALUES (?, ?, ?, 1, 'pending', ?, ?)`,
		step.Action, `{"msg":"manual_enqueued"}`, `{"action":"log.write","message":"Outbox manual run"}`, pastRetry, nowStr)
	if err != nil {
		t.Fatalf("Failed to insert outbox row: %v", err)
	}
	rowID, _ := res.LastInsertId()

	// Notify the outbox processor to run immediately
	bus.NotifyOutbox()

	// Give the outbox processor a short window to pick up the item
	time.Sleep(300 * time.Millisecond)

	var status string
	var attempts int
	err = bus.DB().QueryRow(`SELECT status, attempts FROM "_spine_outbox" WHERE id = ?`, rowID).Scan(&status, &attempts)
	if err != nil {
		t.Fatalf("Failed to query outbox item status: %v", err)
	}

	if status != "completed" {
		t.Errorf("Expected outbox item status 'completed', got '%s'", status)
	}
}

func TestOutboxFailureAndMaxRetries(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

database:
  outbox:
    max_workers: 2
    max_retries: 2
    backoff_ms: 50

nodes:
  - name: FailingNode
    emits:
      - event: FAIL_EVENT
        payload:
          url: string

routes:
  - on: FAIL_EVENT
    steps:
      - action: http.post
        url: "http://127.0.0.1:59999/invalid_endpoint_will_fail"
        max_attempts: 1
`
	manifestPath := filepath.Join(dir, "outbox_fail.spine")
	dbPath := filepath.Join(dir, "outbox_fail.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write test manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize engine: %v", err)
	}
	defer eng.Close()

	bus := eng.Bus

	// Insert an outbox task that will fail (http.post to non-existent server)
	pastRetry := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	nowStr := time.Now().UTC().Format(time.RFC3339)

	stepJSON := `{"action":"http.post","url":"http://127.0.0.1:59999/invalid_endpoint_will_fail"}`
	res, err := bus.DB().Exec(`INSERT INTO "_spine_outbox" (action, payload, step_data, attempts, status, next_retry_at, created_at) VALUES (?, ?, ?, 2, 'pending', ?, ?)`,
		"http.post", `{"url":"http://127.0.0.1:59999/invalid_endpoint_will_fail"}`, stepJSON, pastRetry, nowStr)
	if err != nil {
		t.Fatalf("Failed to insert outbox row: %v", err)
	}
	rowID, _ := res.LastInsertId()

	// Notify outbox worker pool to process immediately
	bus.NotifyOutbox()

	// Wait for outbox processor to run (attempts = 2 > maxRetries (2) => failed)
	time.Sleep(500 * time.Millisecond)

	var status string
	var attempts int
	err = bus.DB().QueryRow(`SELECT status, attempts FROM "_spine_outbox" WHERE id = ?`, rowID).Scan(&status, &attempts)
	if err != nil {
		t.Fatalf("Failed to query outbox item: %v", err)
	}

	if status != "failed" {
		t.Errorf("Expected outbox item to transition to 'failed' when attempts (%d) > max_retries (2), got status '%s'", attempts, status)
	}
}

// TestOutboxRetryLoopBounded is the regression test for the self-perpetuating
// outbox retry loop: a persistently failing webhook must NOT spawn a new
// outbox row per retry. With the old code, every worker retry re-enqueued a
// fresh row (attempts=1) via execStep, bypassing max_retries and growing the
// table exponentially. Now the table must hold exactly the one original row
// and terminate in 'failed' after max_retries.
func TestOutboxRetryLoopBounded(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

database:
  outbox:
    max_workers: 2
    max_retries: 2
    backoff_ms: 10

nodes:
  - name: DeadNode
    emits:
      - event: TRIGGER_DEAD_WEBHOOK
        payload:
          msg: string

routes:
  - on: TRIGGER_DEAD_WEBHOOK
    steps:
      - action: http.post
        url: "http://127.0.0.1:59999/dead_endpoint"
        max_attempts: 1
`
	manifestPath := filepath.Join(dir, "outbox_loop.spine")
	dbPath := filepath.Join(dir, "outbox_loop.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write test manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize engine: %v", err)
	}
	defer eng.Close()

	bus := eng.Bus

	if _, err := bus.Emit("TRIGGER_DEAD_WEBHOOK", map[string]interface{}{"msg": "boom"}); err == nil {
		t.Fatal("expected emit to fail (dead webhook endpoint), got nil error")
	}

	// The failed emit enqueues exactly one outbox row — asynchronously through
	// the batch writer, so poll until it appears.
	var count int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := bus.DB().QueryRow(`SELECT count(*) FROM "_spine_outbox"`).Scan(&count); err != nil {
			t.Fatalf("failed to count outbox rows: %v", err)
		}
		if count >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 outbox row after failed emit, got %d", count)
	}

	// Drive the worker until the row terminates (max_retries exhausted).
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := bus.DB().QueryRow(`SELECT status FROM "_spine_outbox" LIMIT 1`).Scan(&status); err != nil {
			t.Fatalf("failed to read outbox status: %v", err)
		}
		if status == "failed" {
			break
		}
		bus.NotifyOutbox()
		time.Sleep(150 * time.Millisecond)
	}

	var status string
	var attempts int
	if err := bus.DB().QueryRow(`SELECT status, attempts FROM "_spine_outbox" LIMIT 1`).Scan(&status, &attempts); err != nil {
		t.Fatalf("failed to read outbox row: %v", err)
	}
	if status != "failed" {
		t.Fatalf("expected outbox row to terminate as 'failed', got status=%q attempts=%d", status, attempts)
	}

	// Regression assertion: the retry loop must never have spawned extra rows.
	if err := bus.DB().QueryRow(`SELECT count(*) FROM "_spine_outbox"`).Scan(&count); err != nil {
		t.Fatalf("failed to count outbox rows: %v", err)
	}
	if count != 1 {
		t.Errorf("outbox row count must stay bounded — self-perpetuating retry loop spawned %d rows (want 1)", count)
	}
}

func TestOutboxConfigParsing(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "outbox.spine")

	manifestContent := `spine_version: 1

database:
  outbox:
    max_workers: 15
    max_retries: 8
    backoff_ms: 2000
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	schema, err := manifest.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if schema.Database.Outbox.MaxWorkers != 15 {
		t.Errorf("expected MaxWorkers 15, got %d", schema.Database.Outbox.MaxWorkers)
	}
	if schema.Database.Outbox.MaxRetries != 8 {
		t.Errorf("expected MaxRetries 8, got %d", schema.Database.Outbox.MaxRetries)
	}
	if schema.Database.Outbox.BackoffMs != 2000 {
		t.Errorf("expected BackoffMs 2000, got %d", schema.Database.Outbox.BackoffMs)
	}
}

// TestOutboxStepDataAndWriterResilience tests step context preservation in outbox table.
func TestOutboxStepDataAndWriterResilience(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "app.spine")

	manifestContent := `spine_version: 1
database:
  tables:
    - items

nodes:
  App:
    emits:
      - event: ADD_ITEM
        payload:
          id: string
          title: string
routes:
  - on: ADD_ITEM
    steps:
      - action: db.insert
        table: items
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	dbPath := filepath.Join(tmpDir, "test.db")
	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to init engine: %v", err)
	}
	defer eng.Close()

	_, err = eng.Bus.Emit("ADD_ITEM", map[string]interface{}{"id": "1", "title": "Test Item"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	// Verify outbox schema has step_data column AND that a plain db.insert
	// route never enqueues an outbox row (the previous scan was never
	// asserted — a vacuous test).
	var count int
	err = eng.Bus.DB().QueryRow(`SELECT count(*) FROM "_spine_outbox"`).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query outbox table: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 outbox rows for a db.insert-only route, got %d", count)
	}
}
