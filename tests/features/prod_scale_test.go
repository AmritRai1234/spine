package features

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

func TestProductionScaleFeatures(t *testing.T) {
	tempDir := t.TempDir()

	manifest := `spine_version: 1
database:
  tables:
    - users

nodes:
  TestNode:
    emits:
      - event: TEST_EVENT
        payload:
          email: string

routes:
  - on: TEST_EVENT
    steps:
      - action: db.insert
        table: users
        input: "$event.payload"
`
	manifestPath := filepath.Join(tempDir, "app.spine")
	_ = os.WriteFile(manifestPath, []byte(manifest), 0644)

	dbPath := filepath.Join(tempDir, "prod_scale.db")
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	// 1. Enable API Key Auth
	eng.SetAPIKey("secret-api-key-123")
	handler := eng.HTTPHandler()

	// Test 1a: Request without API key -> Should fail with 401 Unauthorized
	req1, _ := http.NewRequest("POST", "/emit", strings.NewReader(`{"event":"TEST_EVENT","payload":{"email":"test@prod.dev"}}`))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without API key, got %d", rec1.Code)
	}

	// Test 1b: Request with Header X-API-Key -> Should succeed (200 OK)
	req2, _ := http.NewRequest("POST", "/emit", strings.NewReader(`{"event":"TEST_EVENT","payload":{"email":"test@prod.dev"}}`))
	req2.Header.Set("X-API-Key", "secret-api-key-123")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid X-API-Key, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Test 2: /healthz and /readyz probes
	reqH, _ := http.NewRequest("GET", "/healthz", nil)
	recH := httptest.NewRecorder()
	handler.ServeHTTP(recH, reqH)
	if recH.Code != http.StatusOK || !strings.Contains(recH.Body.String(), "healthy") {
		t.Errorf("expected healthy response from /healthz, got %s", recH.Body.String())
	}

	reqR, _ := http.NewRequest("GET", "/readyz", nil)
	recR := httptest.NewRecorder()
	handler.ServeHTTP(recR, reqR)
	if recR.Code != http.StatusOK || !strings.Contains(recR.Body.String(), "ready") {
		t.Errorf("expected ready response from /readyz, got %s", recR.Body.String())
	}

	// Test 3: LocalPubSub
	ps := engine.NewLocalPubSub()
	recChan := make(chan string, 1)
	ps.Subscribe("user_events", func(payload map[string]interface{}) {
		if email, ok := payload["email"].(string); ok {
			recChan <- email
		}
	})
	_ = ps.Publish("user_events", map[string]interface{}{"email": "pubsub@spine.dev"})

	select {
	case email := <-recChan:
		if email != "pubsub@spine.dev" {
			t.Errorf("expected pubsub email 'pubsub@spine.dev', got %s", email)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("pubsub notification timed out")
	}
}
