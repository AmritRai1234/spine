package security

// L8 — upgrade-time WebSocket authentication. When credentials are present
// on the upgrade request (ticket, header key, legacy ?token=), they are
// validated BEFORE the 101; a caller presenting a wrong key gets 401 and
// never enters connection accounting. A credential-less upgrade still
// completes and must authenticate in-frame within wsAuthTimeout (unchanged
// browser fallback).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 1. Wrong header key → 401 BEFORE the upgrade (no 101, no socket).
func TestWSUpgradeWrongKeyRejectedPreUpgrade(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	headers := http.Header{"X-API-Key": []string{"wrong-key"}}
	_, resp, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), headers)
	if err == nil {
		t.Fatal("dial with wrong key must fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 pre-upgrade, got %v", resp)
	}
	// 401 must come from the handler, not a failed handshake after 101.
	if resp.Header.Get("Sec-WebSocket-Accept") != "" {
		t.Fatal("101 handshake must not have completed")
	}
}

// 2. Valid header key → pre-authenticated at upgrade: the socket is
// registered immediately and can emit without any in-frame auth handshake.
func TestWSUpgradeValidKeyPreAuths(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	headers := http.Header{"X-API-Key": []string{"secret-123"}}
	conn, _, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), headers)
	if err != nil {
		t.Fatalf("dial with valid key failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{"type": "reconnect", "last_seen_id": 0}); err != nil {
		t.Fatalf("send reconnect: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read reconnect ack: %v", err)
	}
	// Pre-authenticated clients are allowed straight to replay: no
	// "unauthorized" error ack.
	if resp["status"] != "ok" {
		t.Fatalf("pre-authed socket must get ok reconnect_ack, got: %v", resp)
	}
}

// 3. Valid ticket (M5 flow) → pre-authenticated at upgrade AND its bound
// AccessContext carries through: the client is registered on connect.
func TestWSUpgradeTicketPreAuths(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	// Issue a ticket through the header-only endpoint.
	req, _ := http.NewRequest("GET", server.URL+"/ws-ticket", nil)
	req.Header.Set("X-API-Key", "secret-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ticket issue failed: %v %v", err, resp)
	}
	var issued struct {
		Ticket string `json:"ticket"`
	}
	json.NewDecoder(resp.Body).Decode(&issued)
	resp.Body.Close()
	if issued.Ticket == "" {
		t.Fatal("no ticket issued")
	}

	url := "ws" + hardeningWSUrl(server)[2:] + "?ticket=" + issued.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ticket dial failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{"type": "reconnect", "last_seen_id": 0}); err != nil {
		t.Fatalf("send reconnect: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack map[string]interface{}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("read reconnect ack: %v", err)
	}
	if ack["status"] != "ok" {
		t.Fatalf("ticket-pre-authed socket must get ok reconnect_ack, got: %v", ack)
	}
}

// 4. Invalid ticket → rejected pre-upgrade (never upgraded, never fell
// through to the in-frame path).
func TestWSUpgradeBadTicketRejected(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	url := "ws" + hardeningWSUrl(server)[2:] + "?ticket=deadbeef"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("bad ticket dial must fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 pre-upgrade for bad ticket, got %v", resp)
	}
}

// 5. No credentials at all → the legacy in-frame contract holds: the
// upgrade succeeds, in-frame auth works, and the auth deadline still
// closes credential-less sockets that never authenticate (covered by
// TestWebSocketAuthTimeoutCloses; here we assert auth succeeds).
func TestWSUpgradeNoCredsInFrameAuthStillWorks(t *testing.T) {
	eng := newHardeningEngine(t)
	eng.APIKey = "secret-123"
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(hardeningWSUrl(server), nil)
	if err != nil {
		t.Fatalf("credential-less dial must still upgrade, got %v (status %v)", err, resp)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{"type": "auth", "token": "secret-123"}); err != nil {
		t.Fatalf("send in-frame auth: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var authAck map[string]interface{}
	if err := conn.ReadJSON(&authAck); err != nil {
		t.Fatalf("read auth ack: %v", err)
	}
	if authAck["type"] != "auth_ack" || authAck["status"] != "ok" {
		t.Fatalf("in-frame auth must still succeed, got: %v", authAck)
	}
}
