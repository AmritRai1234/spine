package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
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
