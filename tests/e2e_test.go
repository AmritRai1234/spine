package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/codegen"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// TestEndToEndFeatureParity verifies the roadmap feature set end-to-end:
// tenant parsing, TypeScript codegen, HTTP emit with access control, FTS
// execution, audit logging, and outbox enqueue/processing in one manifest.
func TestEndToEndFeatureParity(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine_roadmap.db")

	manifestContent := `spine_version: 1
tenant: tenant_test_corp

access:
  - role: admin
    key: "$ADMIN_KEY"

database:
  tables:
    - users
    - logs
  outbox:
    max_workers: 5
    max_retries: 3
    backoff_ms: 50

nodes:
  TestNode:
    emits:
      - event: USER_REGISTERED
        payload:
          email: string

routes:
  - on: USER_REGISTERED
    steps:
      - action: db.insert
        table: logs
        sync: "true"
      - action: fts.search
        table: logs
        query: ""$event.payload.email""
      - action: db.insert
        table: users
      - action: emit_to
        stream: external_kafka
    emit: REGISTRATION_SUCCESS
`
	t.Setenv("ADMIN_KEY", "sk_admin_secret_key")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write test manifest: %v", err)
	}

	schema, err := manifest.ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("Manifest parsing failed: %v", err)
	}

	if schema.Tenant != "tenant_test_corp" {
		t.Errorf("Expected Tenant 'tenant_test_corp', got '%s'", schema.Tenant)
	}

	tsTypes := codegen.GenerateTypeScript(schema)
	if tsTypes == "" {
		t.Fatalf("TypeScript codegen returned empty string")
	}
	// The generated code must actually contain the declared event contract.
	if !bytes.Contains([]byte(tsTypes), []byte("USER_REGISTERED")) {
		t.Errorf("codegen output missing USER_REGISTERED contract")
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Engine creation failed: %v", err)
	}
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	// Emit over the REAL HTTP surface with the admin key — the httptest
	// server used to be created and never used.
	body, _ := json.Marshal(map[string]interface{}{
		"event":   "USER_REGISTERED",
		"payload": map[string]interface{}{"email": "user@test.dev"},
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/emit", bytes.NewReader(body))
	req.Header.Set("X-API-Key", "sk_admin_secret_key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP emit failed: %v", err)
	}
	defer resp.Body.Close()

	var emitResp struct {
		Status string `json:"status"`
		Event  string `json:"event"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emitResp); err != nil {
		t.Fatalf("decode emit response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || emitResp.Status != "ok" || emitResp.Event != "USER_REGISTERED" {
		t.Fatalf("HTTP emit: status=%d body=%+v", resp.StatusCode, emitResp)
	}

	// The emit's writes must be durable (the route uses sync inserts).
	waitForTableRows(t, eng, "users", 1)
	waitForTableRows(t, eng, "logs", 1)

	// The audit log must contain the event.
	waitUntil(t, "audit row", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "_spine_events"`).Scan(&n)
		return n >= 1
	})

	// Unauthenticated emits must be rejected (access rules configured).
	badReq, _ := http.NewRequest(http.MethodPost, server.URL+"/emit", bytes.NewReader(body))
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("unauthenticated emit failed: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated emit must be 401, got %d", badResp.StatusCode)
	}

	// Outbox: enqueue a task and let the worker process it to 'completed'.
	eng.Bus.EnqueueOutboxStep(nil, "log.write", map[string]interface{}{"msg": "verified"}, 10)
	eng.Bus.NotifyOutbox()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := eng.Bus.DB().QueryRow(`SELECT status FROM "_spine_outbox" ORDER BY id DESC LIMIT 1`).Scan(&status); err == nil && status == "completed" {
			break
		}
		eng.Bus.NotifyOutbox()
		time.Sleep(100 * time.Millisecond)
	}
	var status string
	if err := eng.Bus.DB().QueryRow(`SELECT status FROM "_spine_outbox" ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatalf("outbox status query failed: %v", err)
	}
	if status != "completed" {
		t.Errorf("enqueued outbox step must complete, got status %q", status)
	}
}
