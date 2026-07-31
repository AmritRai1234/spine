package tests

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
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
