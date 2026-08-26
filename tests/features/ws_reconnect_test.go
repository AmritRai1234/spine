package features

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/gorilla/websocket"
)

func TestWebSocketReconnectProtocol(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - events_test

routes:
  - on: USER_ACTION
    steps:
      - action: db.insert
        table: events_test
    emit: ACTION_DONE
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial WS: %v", err)
	}

	// Send reconnect handshake asking for missed events after ID 0
	reconnectMsg := map[string]interface{}{
		"type":         "reconnect",
		"last_seen_id": 0,
	}
	if err := conn.WriteJSON(reconnectMsg); err != nil {
		t.Fatalf("Failed to send reconnect: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read reconnect ack: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatalf("Invalid json response: %v", err)
	}

	if resp["type"] != "reconnect_ack" {
		t.Errorf("Expected type 'reconnect_ack', got %v", resp["type"])
	}
	if resp["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", resp["status"])
	}

	conn.Close()
}

// TestWebSocketAuthHandshake verifies message-based WS auth handshake for browser clients.
func TestWebSocketAuthHandshake(t *testing.T) {
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
