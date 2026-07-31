package tests

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestWebhookIngestion(t *testing.T) {
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
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	payload := map[string]interface{}{
		"type":   "payment_intent.succeeded",
		"amount": 5000,
	}
	bodyBytes, _ := json.Marshal(payload)

	resp, err := server.Client().Post(server.URL+"/webhook/stripe", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("Failed POST /webhook/stripe: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}
	if res["event"] != "WEBHOOK_STRIPE" {
		t.Errorf("Expected event 'WEBHOOK_STRIPE', got %v", res["event"])
	}
}
