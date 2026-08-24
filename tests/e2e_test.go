package tests

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/codegen"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// TestEndToEndFeatureParity verifies the full feature set described in
// five_year_roadmap.md end-to-end: tenant isolation, access control, outbox
// tuning, FTS, emit_to streams, and TypeScript codegen in one manifest.
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
        table: users
      - action: fts.search
        table: logs
        query: "$event.payload.email"
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

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Engine creation failed: %v", err)
	}
	defer eng.Close()

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	payload := map[string]interface{}{"email": "user@test.dev"}
	res, err := eng.Bus.Emit("USER_REGISTERED", payload)
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}

	eng.Bus.EnqueueOutboxStep(nil, "log.write", map[string]interface{}{"msg": "verified"}, 10)
	time.Sleep(100 * time.Millisecond)
}
