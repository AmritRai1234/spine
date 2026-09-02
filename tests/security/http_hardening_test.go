package security

// HTTP/WS hardening tests: truthful readiness probe, auth-gated WS replay,
// first-message auth timeout, and the WS connection cap.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/gorilla/websocket"
)

const hardeningManifest = `spine_version: 1

routes:
  - on: HARD_TEST
    steps:
      - action: log.write
        message: "hardening test"
`

func newHardeningEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(spineFile, []byte(hardeningManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func hardeningWSUrl(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
}

// TestReadyzPingsDB verifies /readyz reflects real database health.
func TestReadyzPingsDB(t *testing.T) {
	eng := newHardeningEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("readyz GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 when DB is healthy, got %d", resp.StatusCode)
	}

	// Kill the DB — readyz must now report not-ready.
	if err := eng.Bus.DB().Close(); err != nil {
		t.Fatalf("closing db for test: %v", err)
	}
	resp, err = http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("readyz GET after db close failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when DB is down, got %d", resp.StatusCode)
	}
}

// TestWebSocketReconnectRequiresAuth verifies the replay-leak fix: an
// unauthenticated client must NOT receive event history via reconnect.
func TestWebSocketReconnectRequiresAuth(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{"type": "reconnect", "last_seen_id": 0}); err != nil {
		t.Fatalf("send reconnect: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if resp["type"] != "reconnect_ack" || resp["status"] != "error" {
		t.Fatalf("expected error reconnect_ack for unauthenticated client, got: %v", resp)
	}
	if resp["error"] == nil || !strings.Contains(resp["error"].(string), "unauthorized") {
		t.Fatalf("expected unauthorized error, got: %v", resp)
	}
}

// TestWebSocketReconnectAfterAuth verifies replay still works once the client
// has authenticated (in-band auth handshake).
func TestWebSocketReconnectAfterAuth(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{"type": "auth", "token": "secret-123"}); err != nil {
		t.Fatalf("send auth: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var authAck map[string]interface{}
	if err := conn.ReadJSON(&authAck); err != nil {
		t.Fatalf("read auth ack: %v", err)
	}
	if authAck["status"] != "ok" {
		t.Fatalf("auth failed: %v", authAck)
	}

	if err := conn.WriteJSON(map[string]interface{}{"type": "reconnect", "last_seen_id": 0}); err != nil {
		t.Fatalf("send reconnect: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var rec map[string]interface{}
	if err := conn.ReadJSON(&rec); err != nil {
		t.Fatalf("read reconnect ack: %v", err)
	}
	if rec["type"] != "reconnect_ack" || rec["status"] != "ok" {
		t.Fatalf("expected ok reconnect_ack after auth, got: %v", rec)
	}
}

// TestWebSocketAuthTimeoutCloses verifies unauthenticated connections are
// closed with 4001 after the auth deadline instead of lingering forever.
func TestWebSocketAuthTimeoutCloses(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	eng.SetWSAuthTimeout(200 * time.Millisecond)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by the auth timeout")
	}
	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected CloseError, got %T: %v", err, err)
	}
	if closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("expected close code 1008 (policy violation), got %d", closeErr.Code)
	}
}

// TestWebSocketConnCap verifies the connection cap refuses upgrades with 503.
func TestWebSocketConnCap(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.SetMaxWSConns(1)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	first, _, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err != nil {
		t.Fatalf("first dial failed: %v", err)
	}
	defer first.Close()

	_, _, err = websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err == nil {
		t.Fatal("expected second dial to be refused by the connection cap")
	}
	if !strings.Contains(err.Error(), "bad handshake") {
		t.Fatalf("expected handshake refusal, got: %v", err)
	}
}

// TestMetricsDurabilityCounters verifies the write-path durability counters
// are exposed on /metrics when the endpoint is publicly opted in.
func TestMetricsDurabilityCounters(t *testing.T) {
	t.Setenv("SPINE_METRICS_PUBLIC", "1")
	eng := newHardeningEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics GET failed: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	for _, metric := range []string{
		"spine_commit_failures",
		"spine_spill_writes",
		"spine_lost_writes",
		"spine_dropped_audit",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("expected metric %s on /metrics", metric)
		}
	}
}
