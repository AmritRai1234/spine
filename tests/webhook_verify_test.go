package tests

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// computeStripeSignature generates a Stripe-compatible HMAC-SHA256 signature.
func computeStripeSignature(timestamp int64, rawBody []byte, secret string) string {
	signedPayload := strconv.FormatInt(timestamp, 10) + "." + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", timestamp, sig)
}

// computeGenericSignature generates an X-Spine-Signature header value.
func computeGenericSignature(timestamp int64, rawBody []byte, secret string) string {
	signedPayload := strconv.FormatInt(timestamp, 10) + "." + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%d.%s", timestamp, sig)
}

// setupTestEngine creates a test engine with the webhook manifest and a given webhook secret.
func setupTestEngine(t *testing.T, allowUnsigned bool, secret string) (*spine.Engine, *httptest.Server) {
	t.Helper()

	if allowUnsigned {
		os.Setenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS", "1")
	} else {
		os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")
	}

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - webhooks_log

routes:
  - on: WEBHOOK_STRIPE
    steps:
      - action: db.insert
        table: webhooks_log
    emit: STRIPE_EVENT_PROCESSED
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}

	if secret != "" {
		eng.SetWebhookSecret("stripe", secret)
	}

	server := httptest.NewServer(eng.HTTPHandler())
	t.Cleanup(func() {
		server.Close()
		eng.Close()
	})

	return eng, server
}

func TestWebhookValidStripeSignature(t *testing.T) {
	secret := "whsec_test_secret_key_12345"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_test_event_id",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()
	sigHeader := computeStripeSignature(ts, bodyBytes, secret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST /webhook/stripe: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}
	if res["event"] != "WEBHOOK_STRIPE" {
		t.Errorf("Expected event 'WEBHOOK_STRIPE', got %v", res["event"])
	}
}

func TestWebhookTamperedPayloadRejected(t *testing.T) {
	secret := "whsec_test_secret_key_12345"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_test",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()
	sigHeader := computeStripeSignature(ts, bodyBytes, secret)

	// Tamper with the payload after signing
	tamperedPayload := map[string]interface{}{
		"id":   "evt_tampered",
		"type": "payment_intent.canceled",
	}
	tamperedBody, _ := json.Marshal(tamperedPayload)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(tamperedBody))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("Expected 401 for tampered payload, got %d", resp.StatusCode)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	if res["error"] != "invalid_webhook_signature" {
		t.Errorf("Expected 'invalid_webhook_signature', got %v", res["error"])
	}
}

func TestWebhookExpiredTimestampRejected(t *testing.T) {
	secret := "whsec_test_secret_key_12345"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_test",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	// Timestamp 10 minutes in the past (> 300s)
	ts := time.Now().Unix() - 600
	sigHeader := computeStripeSignature(ts, bodyBytes, secret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("Expected 401 for expired timestamp, got %d", resp.StatusCode)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	if res["error"] != "invalid_webhook_signature" {
		t.Errorf("Expected 'invalid_webhook_signature', got %v", res["error"])
	}
}

func TestWebhookWrongSecretRejected(t *testing.T) {
	secret := "whsec_test_secret_key_12345"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_test",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()
	// Sign with a different secret
	wrongSecret := "whsec_wrong_secret"
	sigHeader := computeStripeSignature(ts, bodyBytes, wrongSecret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("Expected 401 for wrong secret, got %d", resp.StatusCode)
	}
}

func TestWebhookUnsignedBlockedWhenConfigured(t *testing.T) {
	secret := "whsec_test_secret_key_12345"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_test",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	// Send without signature header
	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("Expected 401 for unsigned request, got %d", resp.StatusCode)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	if res["error"] != "invalid_webhook_signature" {
		t.Errorf("Expected 'invalid_webhook_signature', got %v", res["error"])
	}
}

func TestWebhookUnconfiguredReturns503(t *testing.T) {
	// No secret configured, SPINE_ALLOW_UNSIGNED_WEBHOOKS not set
	_, server := setupTestEngine(t, false, "")

	payload := map[string]interface{}{
		"id":   "evt_test",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Errorf("Expected 503 for unconfigured provider, got %d", resp.StatusCode)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	if res["error"] != "webhook_provider_not_configured" {
		t.Errorf("Expected 'webhook_provider_not_configured', got %v", res["error"])
	}
}

func TestWebhookUnsignedWithAllowEscapeHatch(t *testing.T) {
	// No secret configured, but SPINE_ALLOW_UNSIGNED_WEBHOOKS=1
	_, server := setupTestEngine(t, true, "")

	payload := map[string]interface{}{
		"id":   "evt_test_escape",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 with SPINE_ALLOW_UNSIGNED_WEBHOOKS=1, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}
}

func TestWebhookRawBodyReachesHandler(t *testing.T) {
	secret := "whsec_test_secret"
	eng, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_raw_body_test",
		"type": "payment_intent.succeeded",
		"data": map[string]interface{}{
			"amount": 1000,
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()
	sigHeader := computeStripeSignature(ts, bodyBytes, secret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// Verify the event was processed by checking the result contains event info
	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	if res["event"] != "WEBHOOK_STRIPE" {
		t.Errorf("Expected event 'WEBHOOK_STRIPE', got %v", res["event"])
	}

	// The idempotency key should have been set from the event id
	// Verify by checking the event logs
	logs, err := eng.Bus.GetEventLogs("WEBHOOK_STRIPE", 10, 0)
	if err != nil {
		t.Logf("Event logs error (may be empty DB): %v", err)
	} else {
		t.Logf("Event logs: %+v", logs)
	}
}

func TestWebhookGenericSignature(t *testing.T) {
	secret := "whsec_generic_secret"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_generic",
		"type": "generic.event",
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()
	sigHeader := computeGenericSignature(ts, bodyBytes, secret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Spine-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for valid generic signature, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}
}

func TestWebhookMultipleV1Entries(t *testing.T) {
	secret := "whsec_test_secret"
	_, server := setupTestEngine(t, false, secret)

	payload := map[string]interface{}{
		"id":   "evt_multi_v1",
		"type": "payment_intent.succeeded",
	}
	bodyBytes, _ := json.Marshal(payload)

	ts := time.Now().Unix()

	// Build a header with multiple v1 entries (Stripe might send multiples during key rotation)
	signedPayload := strconv.FormatInt(ts, 10) + "." + string(bodyBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	validSig := hex.EncodeToString(mac.Sum(nil))

	// Invalid old key signature
	oldMac := hmac.New(sha256.New, []byte("whsec_old_key"))
	oldMac.Write([]byte(signedPayload))
	oldSig := hex.EncodeToString(oldMac.Sum(nil))

	sigHeader := fmt.Sprintf("t=%d,v1=%s,v1=%s", ts, oldSig, validSig)

	req, err := http.NewRequest("POST", server.URL+"/webhook/stripe", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 with multiple v1 entries (one valid), got %d", resp.StatusCode)
	}
}

func TestWebhookMissingProvider(t *testing.T) {
	secret := "whsec_test_secret"
	_, server := setupTestEngine(t, false, secret)

	req, err := http.NewRequest("POST", server.URL+"/webhook/", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("Expected 400 for missing provider, got %d", resp.StatusCode)
	}
}

// ─── WebSocket Origin Tests ────────────────────────────────────────────────────

func TestWSOriginSameOriginAllowed(t *testing.T) {
	os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")
	_, server := setupTestEngine(t, false, "")

	// Attempt a WS upgrade with same-origin header
	// The upgrade will fail but we can see if CheckOrigin blocks it
	req, err := http.NewRequest("GET", server.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Use server's own host as origin
	origin := server.URL
	req.Header.Set("Origin", origin)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed WS request: %v", err)
	}
	defer resp.Body.Close()

	// If same-origin is allowed, the upgrade should be attempted
	// (it will fail because client doesn't handle WS upgrade, but status should be 101 or similar upgrade attempt)
	if resp.StatusCode == 403 {
		t.Errorf("Same-origin WebSocket request was blocked (got 403)")
	}
	t.Logf("Same-origin WS response status: %d", resp.StatusCode)
}

func TestWSOriginForeignOriginDenied(t *testing.T) {
	os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")
	_, server := setupTestEngine(t, false, "")

	req, err := http.NewRequest("GET", server.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Foreign origin - should be denied
	req.Header.Set("Origin", "https://evil-attacker.com")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed WS request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("Expected 403 for foreign origin, got %d", resp.StatusCode)
	}
}

func TestWSOriginAllowlistedOriginAllowed(t *testing.T) {
	// Set up allowlist that includes our test origin
	allowedOrigin := "https://trusted-app.com"
	os.Setenv("SPINE_WS_ORIGINS", allowedOrigin)
	defer os.Unsetenv("SPINE_WS_ORIGINS")
	os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")

	_, server := setupTestEngine(t, false, "")

	req, err := http.NewRequest("GET", server.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed WS request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		t.Errorf("Allowlisted origin was blocked (got 403)")
	}
	t.Logf("Allowlisted origin WS response status: %d", resp.StatusCode)
}

func TestWSOriginNoOriginAllowed(t *testing.T) {
	os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")
	_, server := setupTestEngine(t, false, "")

	req, err := http.NewRequest("GET", server.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// No Origin header - non-browser client, should be allowed
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed WS request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		t.Errorf("No-Origin WS request was blocked (got 403)")
	}
	t.Logf("No-Origin WS response status: %d", resp.StatusCode)
}

func TestWSOriginWildcardAllowlist(t *testing.T) {
	os.Setenv("SPINE_WS_ORIGINS", "*")
	defer os.Unsetenv("SPINE_WS_ORIGINS")
	os.Unsetenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS")

	_, server := setupTestEngine(t, false, "")

	req, err := http.NewRequest("GET", server.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Origin", "https://any-origin.com")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed WS request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		t.Errorf("Wildcard allowlisted origin was blocked (got 403)")
	}
	t.Logf("Wildcard allowlist WS response status: %d", resp.StatusCode)
}
