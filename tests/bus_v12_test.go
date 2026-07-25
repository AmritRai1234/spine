package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestEventChainingAndActions(t *testing.T) {
	// 1. Setup mock HTTP server for http.post testing
	receivedWebhook := make(chan map[string]interface{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&data)
		receivedWebhook <- data
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// 2. Setup schema with event chaining and new actions
	manifestContent := `spine_version: 1

database:
  tables:
    - leads
    - audit_log

nodes:
  TestNode:
    emits:
      - event: SUBMIT_FORM
        payload:
          email: string
    listens:
      - state: FORM_PROCESSED
      - state: CHAIN_COMPLETED

routes:
  - on: SUBMIT_FORM
    steps:
      - action: db.insert
        table: leads
      - action: log.write
        message: "New form submitted for $event.payload.email"
      - action: http.post
        url: "` + ts.URL + `"
    emit: FORM_PROCESSED

  - on: FORM_PROCESSED
    steps:
      - action: db.insert
        table: audit_log
    emit: CHAIN_COMPLETED
`

	tmpFile, err := os.CreateTemp("", "test_v12_*.spine")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString(manifestContent)
	tmpFile.Close()

	schema, err := spine.ParseManifest(tmpFile.Name())
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	dbPath := tmpFile.Name() + ".db"
	defer os.Remove(dbPath)

	engine, err := spine.New(schema, dbPath)
	if err != nil {
		t.Fatalf("New engine failed: %v", err)
	}
	defer engine.Close()

	// 3. Emit SUBMIT_FORM
	res, err := engine.Bus.Emit("SUBMIT_FORM", map[string]interface{}{
		"email": "test@v12.dev",
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// 4. Verify Chained States
	emittedStates, ok := res["emitted_states"].([]string)
	if !ok || len(emittedStates) < 2 {
		t.Fatalf("expected at least 2 chained states, got: %v", res["emitted_states"])
	}
	if emittedStates[0] != "FORM_PROCESSED" || emittedStates[1] != "CHAIN_COMPLETED" {
		t.Errorf("unexpected emitted state sequence: %v", emittedStates)
	}

	// 5. Verify Webhook received payload
	select {
	case webhookData := <-receivedWebhook:
		if webhookData["email"] != "test@v12.dev" {
			t.Errorf("expected email test@v12.dev in webhook, got: %v", webhookData)
		}
	default:
		t.Errorf("webhook server did not receive payload")
	}
}
