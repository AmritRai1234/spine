package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/tests/testhelpers"
)

const minKeyLen = 32

// TestEndToEndPerUserAuth walks the full customer-account flow over HTTP:
// register → per-user key issued (delivered via the ACCOUNT_CREATED WS state
// broadcast, the same path a browser storefront uses) → per-user row
// isolation on table reads → login rotates the key (old key dies) →
// whitelist inheritance enforced on emit → logout revokes → login lockout
// after 5 failures.
func TestEndToEndPerUserAuth(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine_auth.db")

	os.Setenv("ADMIN_SECRET", "e2e-admin-key")
	os.Setenv("CUSTOMER_SECRET", "e2e-customer-key")
	t.Cleanup(func() { os.Unsetenv("ADMIN_SECRET"); os.Unsetenv("CUSTOMER_SECRET") })

	manifestContent := `spine_version: 1

access:
  - role: admin
    key: "$ADMIN_SECRET"
  - role: customer
    key: "$CUSTOMER_SECRET"
    events:
      - REGISTER_ACCOUNT
      - LOGIN_ACCOUNT
      - LOGOUT_ACCOUNT
      - ADD_TO_CART
    tables:
      - cart_items: "email = 'pending-customer'"

database:
  tables:
    - cart_items

nodes:
  Accounts:
    emits:
      - event: REGISTER_ACCOUNT
        payload:
          email: string
          password: string
      - event: LOGIN_ACCOUNT
        payload:
          email: string
          password: string
      - event: LOGOUT_ACCOUNT
        payload:
          key: string
  Carts:
    emits:
      - event: ADD_TO_CART
        payload:
          email: string
          product: string
          qty: integer

routes:
  - on: REGISTER_ACCOUNT
    steps:
      - action: auth.register
        email: $event.payload.email
        password: $event.payload.password
      - action: set
        email: $event.payload.email
        product: welcome-item
        qty: 1
      - action: db.insert
        table: cart_items
    emit: ACCOUNT_CREATED
  - on: LOGIN_ACCOUNT
    steps:
      - action: auth.login
        email: $event.payload.email
        password: $event.payload.password
    emit: ACCOUNT_LOGGED_IN
  - on: LOGOUT_ACCOUNT
    steps:
      - action: auth.logout
        key: $event.payload.key
    emit: ACCOUNT_LOGGED_OUT
  - on: ADD_TO_CART
    steps:
      - action: set
        email: $event.payload.email
        product: $event.payload.product
        qty: $event.payload.qty
      - action: db.insert
        table: cart_items
`
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	server := httptest.NewServer(eng.HTTPHandler())
	t.Cleanup(server.Close)

	custKey := os.Getenv("CUSTOMER_SECRET")
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	emit := func(key string, event string, payload map[string]interface{}) (int, map[string]interface{}) {
		body, _ := json.Marshal(map[string]interface{}{"event": event, "payload": payload})
		req, _ := http.NewRequest("POST", server.URL+"/emit", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("emit %s: %v", event, err)
		}
		defer resp.Body.Close()
		var out map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	readCart := func(key string) (int, []interface{}) {
		req, _ := http.NewRequest("GET", server.URL+"/tables/cart_items", nil)
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("read cart: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Rows []interface{} `json:"rows"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Rows
	}

	// broadcasts receives every state broadcast visible to the customer key
	// (the real client path for step-produced payload fields like auth_key).
	broadcasts := make(chan map[string]interface{}, 32)
	go func() {
		defer close(broadcasts)
		hdr := http.Header{}
		hdr.Set("X-API-Key", custKey)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				Type    string                 `json:"type"`
				State   string                 `json:"state"`
				Payload map[string]interface{} `json:"payload"`
			}
			if json.Unmarshal(msg, &m) != nil || m.Type != "state" {
				continue
			}
			broadcasts <- map[string]interface{}{"state": m.State, "payload": m.Payload}
		}
	}()

	// waitAuthKey drains broadcasts until wantState arrives, returning the
	// first auth_key payload field (skipping `exclude` when non-empty).
	waitAuthKey := func(wantState, exclude string) string {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case m, ok := <-broadcasts:
				if !ok {
					return ""
				}
				if m["state"] != wantState {
					continue
				}
				p, _ := m["payload"].(map[string]interface{})
				if p == nil {
					continue
				}
				if k, ok := p["auth_key"].(string); ok && len(k) >= minKeyLen && k != exclude {
					return k
				}
			case <-deadline:
				return ""
			}
		}
	}

	// 1. Register — the per-user key arrives on ACCOUNT_CREATED.
	code, res := emit(custKey, "REGISTER_ACCOUNT", map[string]interface{}{
		"email": "carol@example.com", "password": "correct-horse-1",
	})
	if code != 200 {
		t.Fatalf("register: HTTP %d: %v", code, res)
	}
	testhelpers.WaitForTableRows(t, eng, "_spine_users", 1)
	userKey := waitAuthKey("ACCOUNT_CREATED", "")
	if userKey == "" {
		t.Fatal("no per-user key issued on state broadcast")
	}

	// 2. Per-user key works and is isolated: only carol's row.
	// Wait for the batched cart insert to flush — the broadcast can arrive
	// before the db.insert batch is persisted.
	testhelpers.WaitForTableRows(t, eng, "cart_items", 1)
	if code, rows := readCart(userKey); code != 200 || len(rows) != 1 {
		t.Fatalf("per-user read: HTTP %d rows=%v", code, rows)
	}

	// 3. Login rotates: old key dies, new key works.
	code, _ = emit(userKey, "LOGIN_ACCOUNT", map[string]interface{}{
		"email": "carol@example.com", "password": "correct-horse-1",
	})
	if code != 200 {
		t.Fatalf("login: HTTP %d", code)
	}
	newKey := waitAuthKey("ACCOUNT_LOGGED_IN", userKey)
	if newKey == "" {
		t.Fatal("login did not rotate the key")
	}
	if code, _ := readCart(userKey); code != 401 {
		t.Fatalf("old key still works after rotation: HTTP %d", code)
	}
	if code, rows := readCart(newKey); code != 200 || len(rows) != 1 {
		t.Fatalf("new key read: HTTP %d rows=%v", code, rows)
	}

	// 4. Bad password: soft failure (auth_key=false), 200 OK.
	code, _ = emit(newKey, "LOGIN_ACCOUNT", map[string]interface{}{
		"email": "carol@example.com", "password": "wrong-pass-999",
	})
	if code != 200 {
		t.Fatalf("bad password login: HTTP %d", code)
	}

	// 5. Whitelist inheritance: per-user key can emit a whitelisted event but
	// not an unlisted one (403 from the CanEmit gate).
	code, _ = emit(newKey, "ADD_TO_CART", map[string]interface{}{
		"email": "carol@example.com", "product": "thing", "qty": 1,
	})
	if code != 200 {
		t.Fatalf("whitelisted event denied: HTTP %d", code)
	}
	testhelpers.WaitForTableRows(t, eng, "cart_items", 2)
	code, res = emit(newKey, "NOT_IN_WHITELIST", map[string]interface{}{})
	if code != 403 {
		t.Fatalf("unlisted event allowed: HTTP %d: %v", code, res)
	}

	// 6. Logout revokes: the presented key dies.
	code, _ = emit(newKey, "LOGOUT_ACCOUNT", map[string]interface{}{"key": newKey})
	if code != 200 {
		t.Fatalf("logout: HTTP %d", code)
	}
	time.Sleep(100 * time.Millisecond) // async emit pipeline
	if code, _ := readCart(newKey); code != 401 {
		t.Fatalf("logged-out key still works: HTTP %d", code)
	}

	// 7. Login lockout: 5 bad logins lock even the correct password out.
	for i := 0; i < 5; i++ {
		emit(custKey, "LOGIN_ACCOUNT", map[string]interface{}{
			"email": "carol@example.com", "password": "wrong-pass-999",
		})
	}
	// Mark the drain point: after these 5 failures, one more locked attempt.
	code, _ = emit(custKey, "LOGIN_ACCOUNT", map[string]interface{}{
		"email": "carol@example.com", "password": "correct-horse-1",
	})
	if code != 200 {
		t.Fatalf("locked login: HTTP %d", code)
	}
	// The locked attempt's ACCOUNT_LOGGED_IN broadcast must carry
	// auth_key=false (+ retry_after), never a fresh key.
	foundFreshKey := false
	deadline := time.After(3 * time.Second)
drain:
	for {
		select {
		case m, ok := <-broadcasts:
			if !ok {
				break drain
			}
			if m["state"] != "ACCOUNT_LOGGED_IN" {
				continue
			}
			p, _ := m["payload"].(map[string]interface{})
			if p == nil {
				continue
			}
			if k, ok := p["auth_key"].(string); ok && len(k) >= minKeyLen {
				foundFreshKey = true
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	if foundFreshKey {
		t.Fatal("lockout bypassed: fresh key issued on locked-out login")
	}
}

// TestEndToEndTOTPLogin walks 2FA over HTTP: register → totp.setup (secret +
// otpauth URI in the route payload) → totp.confirm flips enrollment → login
// without a code is refused with totp_required → login with a live code
// issues the key → the same code replayed is refused → disable with a wrong
// code fails and with a live code unenrolls.
func TestEndToEndTOTPLogin(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine_totp.db")

	os.Setenv("ADMIN_SECRET", "e2e-admin-key")
	os.Setenv("CUSTOMER_SECRET", "e2e-customer-key")
	t.Cleanup(func() { os.Unsetenv("ADMIN_SECRET"); os.Unsetenv("CUSTOMER_SECRET") })

	manifestContent := `spine_version: 1

access:
  - role: admin
    key: "$ADMIN_SECRET"
  - role: customer
    key: "$CUSTOMER_SECRET"
    events:
      - REGISTER_TOTP_USER
      - LOGIN_TOTP_USER
      - TOTP_SETUP
      - TOTP_CONFIRM
      - TOTP_DISABLE
    tables:
      - cart_items: "email = 'pending-customer'"

database:
  tables:
    - cart_items

nodes:
  Accounts:
    emits:
      - event: REGISTER_TOTP_USER
        payload:
          email: string
          password: string
      - event: LOGIN_TOTP_USER
        payload:
          email: string
          password: string
          totp_code: string
      - event: TOTP_SETUP
        payload:
          email: string
      - event: TOTP_CONFIRM
        payload:
          email: string
          code: string
      - event: TOTP_DISABLE
        payload:
          email: string
          code: string

routes:
  - on: REGISTER_TOTP_USER
    steps:
      - action: auth.register
        email: $event.payload.email
        password: $event.payload.password
    emit: TOTP_USER_CREATED
  - on: TOTP_SETUP
    steps:
      - action: auth.totp.setup
        email: $event.payload.email
    emit: TOTP_SETUP_DONE
  - on: TOTP_CONFIRM
    steps:
      - action: auth.totp.confirm
        email: $event.payload.email
        code: $event.payload.code
    emit: TOTP_CONFIRMED
  - on: LOGIN_TOTP_USER
    steps:
      - action: auth.login
        email: $event.payload.email
        password: $event.payload.password
        totp_code: $event.payload.totp_code
    emit: TOTP_LOGIN_DONE
  - on: TOTP_DISABLE
    steps:
      - action: auth.totp.disable
        email: $event.payload.email
        code: $event.payload.code
    emit: TOTP_DISABLED
`
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	server := httptest.NewServer(eng.HTTPHandler())
	t.Cleanup(server.Close)

	custKey := os.Getenv("CUSTOMER_SECRET")

	// wsPayloads drains state broadcasts (the real client surface for
	// step-produced payload fields like totp_secret / auth_key, which the
	// /emit response deliberately does not echo).
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	wsPayloads := make(chan map[string]interface{}, 64)
	go func() {
		defer close(wsPayloads)
		hdr := http.Header{}
		hdr.Set("X-API-Key", custKey)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				Type    string                 `json:"type"`
				State   string                 `json:"state"`
				Payload map[string]interface{} `json:"payload"`
			}
			if json.Unmarshal(msg, &m) != nil || m.Type != "state" {
				continue
			}
			wsPayloads <- map[string]interface{}{"_state": m.State, "payload": m.Payload}
		}
	}()
	waitField := func(wantState, field string) map[string]interface{} {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case m, ok := <-wsPayloads:
				if !ok {
					return nil
				}
				if m["_state"] != wantState {
					continue
				}
				p, _ := m["payload"].(map[string]interface{})
				if p == nil {
					continue
				}
				if _, has := p[field]; has {
					return p
				}
			case <-deadline:
				return nil
			}
		}
	}
	emit := func(event string, payload map[string]interface{}) (int, map[string]interface{}) {
		body, _ := json.Marshal(map[string]interface{}{"event": event, "payload": payload})
		req, _ := http.NewRequest("POST", server.URL+"/emit", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", custKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("emit %s: %v", event, err)
		}
		defer resp.Body.Close()
		var out map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. Register.
	if code, res := emit("REGISTER_TOTP_USER", map[string]interface{}{
		"email": "trev@example.com", "password": "correct-horse-1",
	}); code != 200 {
		t.Fatalf("register: HTTP %d: %v", code, res)
	}
	testhelpers.WaitForTableRows(t, eng, "_spine_users", 1)

	// 2. Setup — secret + otpauth URI arrive on the TOTP_SETUP_DONE broadcast.
	code, res := emit("TOTP_SETUP", map[string]interface{}{"email": "trev@example.com"})
	if code != 200 {
		t.Fatalf("totp.setup: HTTP %d: %v", code, res)
	}
	setupPayload := waitField("TOTP_SETUP_DONE", "totp_secret")
	if setupPayload == nil {
		t.Fatal("totp.setup: no TOTP_SETUP_DONE broadcast with totp_secret")
	}
	secret, _ := setupPayload["totp_secret"].(string)
	if len(secret) < 32 {
		t.Fatalf("totp.setup: short secret %q", secret)
	}
	if uri, _ := setupPayload["totp_secret_uri"].(string); !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("totp.setup: bad URI %q", uri)
	}

	// 3. Confirm with a live code (totp_ok=true arrives on the broadcast).
	code1 := totpCodeFor(t, secret, time.Now())
	if code, res = emit("TOTP_CONFIRM", map[string]interface{}{
		"email": "trev@example.com", "code": code1,
	}); code != 200 {
		t.Fatalf("totp.confirm: HTTP %d: %v", code, res)
	}
	confirmPayload := waitField("TOTP_CONFIRMED", "totp_ok")
	if confirmPayload == nil || confirmPayload["totp_ok"] != true {
		t.Fatalf("totp.confirm rejected a live code: %v", confirmPayload)
	}

	// 4. Login without a code → soft-refused with totp_required.
	emitLogin := func(totp string) (int, map[string]interface{}) {
		return emit("LOGIN_TOTP_USER", map[string]interface{}{
			"email": "trev@example.com", "password": "correct-horse-1", "totp_code": totp,
		})
	}
	if code, res = emitLogin(""); code != 200 {
		t.Fatalf("login without code: HTTP %d: %v", code, res)
	}
	noCodePayload := waitField("TOTP_LOGIN_DONE", "auth_key")
	if noCodePayload == nil || noCodePayload["auth_key"] != false || noCodePayload["auth_key_totp_required"] != true {
		t.Fatalf("login without code not refused with totp_required: %v", noCodePayload)
	}

	// 5. Login with a live code → key issued. Use a code one step behind
	// now (still inside the ±1 drift window) so it can't collide with the
	// confirm code's consumed step.
	code2 := totpCodeFor(t, secret, time.Now().Add(-31*time.Second))
	if code, res = emitLogin(code2); code != 200 {
		t.Fatalf("login with code: HTTP %d: %v", code, res)
	}
	loginPayload := waitField("TOTP_LOGIN_DONE", "auth_key")
	if loginPayload == nil {
		t.Fatal("login with code: no TOTP_LOGIN_DONE broadcast")
	}
	userKey, _ := loginPayload["auth_key"].(string)
	if len(userKey) < minKeyLen {
		t.Fatalf("login with code: no key: %v", loginPayload)
	}

	// 6. Replay the same code → refused (same 30s step consumed above).
	if code, res = emitLogin(code2); code != 200 {
		t.Fatalf("replay emit: HTTP %d: %v", code, res)
	}
	replayPayload := waitField("TOTP_LOGIN_DONE", "auth_key")
	if replayPayload == nil || replayPayload["auth_key"] != false {
		t.Fatalf("replayed code accepted: %v", replayPayload)
	}
}

// totpCodeFor computes the 6-digit TOTP for secret at time t (test-local
// reimplementation mirroring the RFC 6238 algorithm under test).
func totpCodeFor(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(secretB32))
	if err != nil {
		t.Fatalf("bad secret: %v", err)
	}
	mac := hmac.New(sha1.New, key)
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], uint64(at.Unix()/30))
	mac.Write(ctr[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", v%1_000_000)
}