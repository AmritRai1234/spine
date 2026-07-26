package tests

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// TestV23ParallelRouteThreadSafety verifies parallel route steps run concurrently without data races or slice corruption.
func TestV23ParallelRouteThreadSafety(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "parallel.spine")
	dbPath := filepath.Join(tmpDir, "parallel.db")

	manifestContent := `spine_version: 1

nodes:
  - name: api
    emits:
      - event: PROCESS_DATA
        payload:
          id: string
          val1: string
          val2: string

routes:
  - on: PROCESS_DATA
    parallel: true
    steps:
      - action: set
        val1: "computed_1"
      - action: set
        val2: "computed_2"
      - action: log.write
        message: "Processing $event.payload.id"
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	var wg sync.WaitGroup
	concurrentReqs := 20
	for i := 0; i < concurrentReqs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := map[string]interface{}{
				"id":   "req_" + string(rune(idx)),
				"val1": "initial1",
				"val2": "initial2",
			}
			_, err := eng.Bus.Emit("PROCESS_DATA", payload)
			if err != nil {
				t.Errorf("parallel emit failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

// TestV23DbUpsertMissingKeyEnforcement verifies db.upsert fails cleanly when primary key is missing from payload.
func TestV23DbUpsertMissingKeyEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "upsert.spine")
	dbPath := filepath.Join(tmpDir, "upsert.db")

	manifestContent := `spine_version: 1

routes:
  - on: SAVE_USER
    steps:
      - action: db.upsert
        table: users
        key: email
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	// Missing 'email' in payload should fail cleanly with error
	_, err = eng.Bus.Emit("SAVE_USER", map[string]interface{}{
		"name": "Bob",
		"age":  30,
	})
	if err == nil {
		t.Fatalf("expected error when conflict key is missing from payload, got nil")
	}
	if !strings.Contains(err.Error(), "conflict key 'email' not present in payload") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestV23OnFailureErrorContextPreservation verifies _error_context and original trigger attributes in failure state.
func TestV23OnFailureErrorContextPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "failure.spine")
	dbPath := filepath.Join(tmpDir, "failure.db")

	manifestContent := `spine_version: 1

routes:
  - on: TRIGGER_FAIL
    on_failure: HANDLE_FAILURE
    steps:
      - action: http.post
        url: "http://invalid.local.domain.does.not.exist:9999/test"
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	initialPayload := map[string]interface{}{
		"order_id": "ORD-12345",
		"amount":   99.50,
	}

	res, err := eng.Bus.Emit("TRIGGER_FAIL", initialPayload)
	if err == nil {
		t.Fatalf("expected route execution to fail")
	}

	if res == nil || res["status"] != "error" {
		t.Fatalf("expected status error in result, got: %v", res)
	}

	failPayload, ok := eng.Bus.GetState("HANDLE_FAILURE")
	if !ok {
		t.Fatalf("expected failure state HANDLE_FAILURE to be set")
	}

	// Verify original payload attributes were preserved
	if failPayload["order_id"] != "ORD-12345" {
		t.Fatalf("expected order_id to be preserved, got %v", failPayload["order_id"])
	}

	// Verify _error_context
	errCtx, ok := failPayload["_error_context"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected _error_context map in failure payload")
	}

	if errCtx["failed_event"] != "TRIGGER_FAIL" {
		t.Fatalf("expected failed_event TRIGGER_FAIL, got %v", errCtx["failed_event"])
	}
	if errCtx["failed_action"] != "http.post" {
		t.Fatalf("expected failed_action http.post, got %v", errCtx["failed_action"])
	}

	origMap, ok := errCtx["original_payload"].(map[string]interface{})
	if !ok || origMap["order_id"] != "ORD-12345" {
		t.Fatalf("expected original_payload in _error_context to preserve order_id")
	}
}

// TestV23WebSocketAuthHandshake verifies message-based WS auth handshake for browser clients.
func TestV23WebSocketAuthHandshake(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "ws.spine")
	dbPath := filepath.Join(tmpDir, "ws.db")

	manifestContent := `spine_version: 1

routes:
  - on: WS_TEST
    steps:
      - action: log.write
        message: "WS event received"
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	eng.APIKey = "secret-key-123"

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// Connect without header or query token
	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect to /ws without initial auth header: %v", err)
	}
	defer conn.Close()

	// Try emitting event without authenticating -> should fail with error
	emitReq := map[string]interface{}{
		"event":   "WS_TEST",
		"payload": map[string]interface{}{"data": "hello"},
	}
	_ = conn.WriteJSON(emitReq)

	var errAck map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&errAck); err != nil {
		t.Fatalf("failed to read ack response: %v", err)
	}

	if errAck["status"] != "error" || !strings.Contains(errAck["error"].(string), "unauthorized") {
		t.Fatalf("expected unauthorized error ack, got: %v", errAck)
	}

	// Now send auth handshake message
	authMsg := map[string]interface{}{
		"type":  "auth",
		"token": "secret-key-123",
	}
	_ = conn.WriteJSON(authMsg)

	var authAck map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&authAck); err != nil {
		t.Fatalf("failed to read auth ack response: %v", err)
	}

	if authAck["type"] != "auth_ack" || authAck["status"] != "ok" {
		t.Fatalf("expected successful auth_ack, got: %v", authAck)
	}

	// Now try emitting event again -> should succeed
	_ = conn.WriteJSON(emitReq)

	var okAck map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&okAck); err != nil {
		t.Fatalf("failed to read event ack: %v", err)
	}

	if okAck["status"] != "ok" {
		t.Fatalf("expected status ok event ack after auth, got: %v", okAck)
	}
}

// TestV23OutboxConfigParsing verifies database.outbox configuration parsing in .spine manifests.
func TestV23OutboxConfigParsing(t *testing.T) {
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
