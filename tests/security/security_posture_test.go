package security

// Security posture regression tests (Batch E):
//   - P2-1: fail-closed auth (no key + no access rules ⇒ 401)
//   - P2-2: RLAC on /events audit log and WS state broadcasts
//   - P2-3: /ws rate limiting (the upgrade path bypasses the HTTP chain)
//   - P2-7: Stripe — payload-derived redirects rejected, Idempotency-Key sent
//   - P2-8: email From/To header injection stripped

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/tests/testhelpers"
	"github.com/gorilla/websocket"
)

const rlcaManifest = `spine_version: 1

access:
  - role: admin
    key: sk_admin_all
  - role: viewer
    key: sk_viewer_events
    events:
      - USER_SIGNUP

database:
  tables:
    - users

nodes:
  AuthNode:
    emits:
      - event: USER_SIGNUP
        payload:
          email: string
      - event: SYSTEM_HEARTBEAT
        payload:
          ok: boolean

routes:
  - on: USER_SIGNUP
    steps:
      - action: db.insert
        table: users
    emit: SIGNUP_DONE
  - on: SYSTEM_HEARTBEAT
    steps:
      - action: log.write
        message: "hb"
    emit: HEARTBEAT_DONE
`

func newRLCAEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(spineFile, []byte(rlcaManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// TestAuthFailClosed: with fail-closed mode on and NO authentication
// configured at all, every endpoint must refuse unauthenticated requests.
func TestAuthFailClosed(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	manifest := `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: EVT
routes:
  - on: EVT
    steps:
      - action: log.write
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	// Legacy permissive mode (default): unauthenticated requests pass.
	resp, err := http.Get(server.URL + "/tables")
	if err != nil {
		t.Fatalf("GET /tables failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 in legacy permissive mode, got %d", resp.StatusCode)
	}

	// Fail-closed mode: no key, no access rules ⇒ refuse everything.
	eng.SetAuthFailClosed(true)
	resp, err = http.Get(server.URL + "/tables")
	if err != nil {
		t.Fatalf("GET /tables failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 in fail-closed mode, got %d", resp.StatusCode)
	}
}

// TestRLCAAuditLogFilter: a role with an Events whitelist must not read audit
// entries for events outside its whitelist via /events.
func TestRLCAAuditLogFilter(t *testing.T) {
	eng := newRLCAEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	for _, ev := range []string{"USER_SIGNUP", "SYSTEM_HEARTBEAT"} {
		payload := map[string]interface{}{"email": "a@b.dev"}
		if ev == "SYSTEM_HEARTBEAT" {
			payload = map[string]interface{}{"ok": true}
		}
		if _, err := eng.Bus.Emit(ev, payload); err != nil {
			t.Fatalf("emit %s failed: %v", ev, err)
		}
	}
	// Give the audit writer a moment to flush.
	testhelpers.WaitUntil(t, "audit rows flushed", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "_spine_events"`).Scan(&n)
		return n >= 2
	})

	getEvents := func(key string) []string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/events", nil)
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /events failed: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Events []map[string]interface{} `json:"events"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode /events: %v", err)
		}
		var names []string
		for _, e := range body.Events {
			if n, ok := e["event"].(string); ok {
				names = append(names, n)
			}
		}
		return names
	}

	// Admin sees both events.
	admin := getEvents("sk_admin_all")
	if len(admin) != 2 {
		t.Fatalf("admin should see both events, got %v", admin)
	}
	// Viewer sees only its whitelisted event.
	viewer := getEvents("sk_viewer_events")
	if len(viewer) != 1 || viewer[0] != "USER_SIGNUP" {
		t.Errorf("viewer must only see whitelisted events, got %v", viewer)
	}
	// Unknown key is rejected outright.
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/events", nil)
	req.Header.Set("X-API-Key", "sk_wrong_key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown key must be 401, got %d", resp.StatusCode)
	}
}

// TestRLCABroadcastFilter: a whitelisted WS client must not receive state
// broadcasts originating from events outside its whitelist.
func TestRLCABroadcastFilter(t *testing.T) {
	eng := newRLCAEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=sk_viewer_events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer ws dial failed: %v", err)
	}
	defer conn.Close()

	// Give registration a beat, then emit a whitelisted and a non-whitelisted
	// event. The viewer must receive the whitelisted broadcast...
	time.Sleep(100 * time.Millisecond)
	if _, err := eng.Bus.Emit("USER_SIGNUP", map[string]interface{}{"email": "x@y.dev"}); err != nil {
		t.Fatalf("emit USER_SIGNUP failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("viewer did not receive the whitelisted broadcast: %v", err)
	}
	if !strings.Contains(string(msg), "USER_SIGNUP") {
		t.Errorf("expected USER_SIGNUP broadcast, got: %s", msg)
	}

	// ...and must NOT receive the non-whitelisted broadcast.
	if _, err := eng.Bus.Emit("SYSTEM_HEARTBEAT", map[string]interface{}{"ok": true}); err != nil {
		t.Fatalf("emit SYSTEM_HEARTBEAT failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err == nil && strings.Contains(string(msg), "SYSTEM_HEARTBEAT") {
		t.Errorf("viewer received a broadcast outside its whitelist: %s", msg)
	}
}

// TestWSRateLimited: the /ws upgrade path bypasses the HTTP middleware chain,
// so rate limiting must be enforced inside the handler itself.
func TestWSRateLimited(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	manifest := `spine_version: 1
database:
  tables:
    - data
routes:
  - on: EVT
    steps:
      - action: log.write
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()
	eng.SetRateLimit(1, 1)

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	// Plain GETs (no upgrade) still hit the limiter before the upgrade check.
	first, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatalf("first /ws GET failed: %v", err)
	}
	first.Body.Close()
	if first.StatusCode == http.StatusTooManyRequests {
		t.Fatal("first /ws request must not be rate limited")
	}

	second, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatalf("second /ws GET failed: %v", err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second /ws request must be rate limited (429), got %d", second.StatusCode)
	}
}

// TestStripePayloadRedirectRejected: post-payment redirect targets derived
// from the client payload are refused (open-redirect protection).
func TestStripePayloadRedirectRejected(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	manifest := `spine_version: 3
database:
  tables:
    - orders
nodes:
  N:
    emits:
      - event: CHARGE
        payload:
          amount: number
          redirect: string
routes:
  - on: CHARGE
    steps:
      - action: stripe.checkout
        order_id: "o-1"
        amount: "$event.payload.amount"
        success_url: "$event.payload.redirect"
        cancel_url: "$env.STORE_URL/cancel"
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_123")
	t.Setenv("STORE_URL", "https://shop.example.com")

	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	_, err = eng.Bus.Emit("CHARGE", map[string]interface{}{
		"amount":   10.0,
		"redirect": "https://evil.example.com/phish",
	})
	if err == nil {
		t.Fatal("payload-derived redirect URL must be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "open-redirect") && !strings.Contains(err.Error(), "client payload") {
		t.Errorf("expected open-redirect rejection, got: %v", err)
	}
}

// TestStripeIdempotencyKeySent: every checkout session request carries an
// Idempotency-Key derived from the order id, so retries cannot duplicate.
func TestStripeIdempotencyKeySent(t *testing.T) {
	var mu sync.Mutex
	var gotHeader string
	fakeStripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeader = r.Header.Get("Idempotency-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cs_test_1","url":"https://checkout.stripe.com/c/pay/cs_test_1"}`))
	}))
	defer fakeStripe.Close()
	t.Setenv("STRIPE_API_BASE", fakeStripe.URL)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_123")

	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	manifest := `spine_version: 3
database:
  tables:
    - orders
nodes:
  N:
    emits:
      - event: CHARGE
        payload:
          amount: number
routes:
  - on: CHARGE
    steps:
      - action: stripe.checkout
        order_id: "o-42"
        amount: "$event.payload.amount"
        success_url: "$env.STORE_URL/success"
        cancel_url: "$env.STORE_URL/cancel"
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("STORE_URL", "https://shop.example.com")

	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	if _, err := eng.Bus.Emit("CHARGE", map[string]interface{}{"amount": 42.0}); err != nil {
		t.Fatalf("stripe checkout emit failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotHeader != "spine-order-o-42" {
		t.Errorf("expected Idempotency-Key 'spine-order-o-42', got %q", gotHeader)
	}
}

// TestEmailFromToInjectionStripped: CRLF in payload-derived From/To must not
// smuggle extra SMTP headers.
func TestEmailFromToInjectionStripped(t *testing.T) {
	server := &testhelpers.FakeSMTPServer{}
	hostPort := server.Start(t)
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("split hostport: %v", err)
	}
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)

	injectManifest := `
nodes:
  MailNode:
    emits:
      - event: SEND_INJECTED
        payload:
          from: string
          to: string
routes:
  - on: SEND_INJECTED
    steps:
      - action: email.send
        from: "$event.payload.from"
        to: "$event.payload.to"
        subject: hi
        body: hello
`
	engine, _ := testhelpers.SetupEmailEngine(t, injectManifest)
	if _, err := engine.Bus.Emit("SEND_INJECTED", map[string]interface{}{
		"from": "store@example.com\r\nBcc: attacker@example.com",
		"to":   "victim@example.com\r\nCc: other@example.com",
	}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	msgs := server.WaitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	for _, line := range strings.Split(msgs[0].Data, "\r\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "bcc:") || strings.HasPrefix(lower, "cc:") {
			t.Errorf("header injection survived via From/To:\n%s", msgs[0].Data)
		}
	}
}

// TestBroadcastCarriesAuditID (P5-1): state broadcasts must be stamped with
// the originating event's audit-log id so the SDKs' reconnect cursor
// advances. A reconnect with last_seen_id = that id must replay NOTHING.
func TestBroadcastCarriesAuditID(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	manifest := `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: EMIT_STATE
        payload:
          note: string
routes:
  - on: EMIT_STATE
    steps:
      - action: db.insert
        table: data
    emit: DATA_CHANGED
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer conn.Close()
	time.Sleep(100 * time.Millisecond) // let the client register

	if _, err := eng.Bus.Emit("EMIT_STATE", map[string]interface{}{"note": "hi"}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read broadcast: %v", err)
	}
	var broadcast struct {
		ID      int64  `json:"id"`
		State   string `json:"state"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(msg, &broadcast); err != nil {
		t.Fatalf("unmarshal broadcast: %v", err)
	}
	if broadcast.State != "DATA_CHANGED" {
		t.Fatalf("expected DATA_CHANGED broadcast, got %s", broadcast.State)
	}
	if broadcast.ID <= 0 {
		t.Fatalf("broadcast must carry the audit id, got id=%d (msg=%s)", broadcast.ID, msg)
	}
	// The audit row the id refers to must exist and be committed.
	var auditEvent string
	if err := eng.Bus.DB().QueryRow(`SELECT event_name FROM "_spine_events" WHERE id = ?`, broadcast.ID).Scan(&auditEvent); err != nil {
		t.Fatalf("audit row %d missing: %v", broadcast.ID, err)
	}
	if auditEvent != "EMIT_STATE" {
		t.Errorf("audit row %d is for %q, want EMIT_STATE", broadcast.ID, auditEvent)
	}

	// Reconnect with last_seen_id = the broadcast id: nothing may replay.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"reconnect","last_seen_id":%d}`, broadcast.ID))); err != nil {
		t.Fatalf("send reconnect: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read reconnect_ack: %v", err)
	}
	var ackMsg struct {
		Type     string `json:"type"`
		Replayed int    `json:"replayed"`
	}
	if err := json.Unmarshal(ack, &ackMsg); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ackMsg.Type != "reconnect_ack" {
		t.Fatalf("expected reconnect_ack, got %s", ackMsg.Type)
	}
	if ackMsg.Replayed != 0 {
		t.Errorf("reconnect at cursor %d replayed %d events (want 0 — the cursor protocol is broken)", broadcast.ID, ackMsg.Replayed)
	}
}
