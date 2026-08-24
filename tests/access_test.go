package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

const accessManifest = `spine_version: 1

database:
  tables:
    - leads

access:
  - role: admin
    key: "sk_admin_secret"

  - role: viewer
    key: "sk_viewer_secret"
    read_only: true

  - role: tenant_acme
    key: "sk_acme_secret"
    filter: "source = 'acme'"
    events:
      - SUBMIT_LEAD

nodes:
  - name: api
    emits:
      - event: SUBMIT_LEAD
        payload:
          email: string
          source: string
      - event: DELETE_LEAD
        payload:
          id: string

routes:
  - on: SUBMIT_LEAD
    steps:
      - action: db.insert
        table: leads
    emit_state: LEAD_STATUS

  - on: DELETE_LEAD
    steps:
      - action: log.write
        message: "Deleted lead $event.payload.id"
`

func setupAccessEngine(t *testing.T) (*spine.Engine, func()) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "access.spine")
	dbPath := filepath.Join(dir, "access.db")

	if err := os.WriteFile(manifestPath, []byte(accessManifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	return eng, func() { eng.Close() }
}

func doEmit(handler http.Handler, apiKey, event string, payload map[string]interface{}) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]interface{}{
		"event":   event,
		"payload": payload,
	})
	req := httptest.NewRequest("POST", "/emit", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func doGet(handler http.Handler, apiKey, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestAccessAdminCanEmitAll verifies admin role has full access.
func TestAccessAdminCanEmitAll(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	// Admin can emit SUBMIT_LEAD
	rr := doEmit(handler, "sk_admin_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "admin@test.com", "source": "admin",
	})
	if rr.Code != 200 {
		t.Errorf("Admin emit SUBMIT_LEAD: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Admin can emit DELETE_LEAD
	rr = doEmit(handler, "sk_admin_secret", "DELETE_LEAD", map[string]interface{}{
		"id": "123",
	})
	if rr.Code != 200 {
		t.Errorf("Admin emit DELETE_LEAD: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Admin can query tables
	rr = doGet(handler, "sk_admin_secret", "/tables/leads")
	if rr.Code != 200 {
		t.Errorf("Admin GET /tables/leads: expected 200, got %d", rr.Code)
	}
}

// TestAccessViewerReadOnly verifies viewer role cannot emit but can read.
func TestAccessViewerReadOnly(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	// Viewer should be rejected from emitting
	rr := doEmit(handler, "sk_viewer_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "viewer@test.com", "source": "test",
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("Viewer emit: expected 403, got %d: %s", rr.Code, rr.Body.String())
	}

	// Viewer can read tables
	rr = doGet(handler, "sk_viewer_secret", "/tables/leads")
	if rr.Code != 200 {
		t.Errorf("Viewer GET /tables/leads: expected 200, got %d", rr.Code)
	}
}

// TestAccessTenantFilteredRows verifies tenant role only sees rows matching their filter.
func TestAccessTenantFilteredRows(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	// Insert rows with different sources using admin key
	doEmit(handler, "sk_admin_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "acme1@test.com", "source": "acme",
	})
	doEmit(handler, "sk_admin_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "acme2@test.com", "source": "acme",
	})
	doEmit(handler, "sk_admin_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "other@test.com", "source": "other_company",
	})

	// Wait for batch writer to flush
	time.Sleep(100 * time.Millisecond)

	// Admin sees all rows
	rr := doGet(handler, "sk_admin_secret", "/tables/leads")
	if rr.Code != 200 {
		t.Fatalf("Admin GET /tables/leads failed: %d", rr.Code)
	}
	var adminResp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &adminResp)
	adminCount := int(adminResp["count"].(float64))
	if adminCount < 3 {
		t.Errorf("Admin should see >= 3 rows, got %d", adminCount)
	}

	// Tenant sees only their rows (source = 'acme')
	rr = doGet(handler, "sk_acme_secret", "/tables/leads")
	if rr.Code != 200 {
		t.Fatalf("Tenant GET /tables/leads failed: %d: %s", rr.Code, rr.Body.String())
	}
	var tenantResp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &tenantResp)
	tenantCount := int(tenantResp["count"].(float64))

	if tenantCount != 2 {
		t.Errorf("Tenant should see exactly 2 rows (source='acme'), got %d", tenantCount)
	}

	// Verify all tenant rows have source=acme
	rows := tenantResp["rows"].([]interface{})
	for i, row := range rows {
		r := row.(map[string]interface{})
		if r["source"] != "acme" {
			t.Errorf("Row %d has source='%v', expected 'acme'", i, r["source"])
		}
	}
}

// TestAccessTenantEventRestriction verifies tenant can only emit whitelisted events.
func TestAccessTenantEventRestriction(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	// Tenant can emit SUBMIT_LEAD (whitelisted)
	rr := doEmit(handler, "sk_acme_secret", "SUBMIT_LEAD", map[string]interface{}{
		"email": "acme@test.com", "source": "acme",
	})
	if rr.Code != 200 {
		t.Errorf("Tenant emit SUBMIT_LEAD: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Tenant CANNOT emit DELETE_LEAD (not in whitelist)
	rr = doEmit(handler, "sk_acme_secret", "DELETE_LEAD", map[string]interface{}{
		"id": "456",
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("Tenant emit DELETE_LEAD: expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAccessUnknownKeyRejected verifies unknown API keys get 401.
func TestAccessUnknownKeyRejected(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	rr := doEmit(handler, "sk_unknown_key", "SUBMIT_LEAD", map[string]interface{}{
		"email": "hacker@test.com", "source": "evil",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Unknown key emit: expected 401, got %d: %s", rr.Code, rr.Body.String())
	}

	rr = doGet(handler, "sk_unknown_key", "/tables/leads")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Unknown key GET: expected 401, got %d", rr.Code)
	}
}

// TestAccessNoKeyRejected verifies missing API key gets 401.
func TestAccessNoKeyRejected(t *testing.T) {
	eng, cleanup := setupAccessEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	rr := doEmit(handler, "", "SUBMIT_LEAD", map[string]interface{}{
		"email": "anon@test.com", "source": "none",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("No key emit: expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAccessBackwardCompatNoRules verifies no access: block = legacy single-key behavior.
func TestAccessBackwardCompatNoRules(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

database:
  tables:
    - items

nodes:
  - name: api
    emits:
      - event: ADD_ITEM
        payload:
          name: string

routes:
  - on: ADD_ITEM
    steps:
      - action: db.insert
        table: items
`
	manifestPath := filepath.Join(dir, "compat.spine")
	dbPath := filepath.Join(dir, "compat.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	// Set legacy single API key
	eng.SetAPIKey("legacy_key_123")
	handler := eng.HTTPHandler()

	// Should work with the legacy key
	rr := doEmit(handler, "legacy_key_123", "ADD_ITEM", map[string]interface{}{
		"name": "test_item",
	})
	if rr.Code != 200 {
		t.Errorf("Legacy key emit: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Should fail with wrong key
	rr = doEmit(handler, "wrong_key", "ADD_ITEM", map[string]interface{}{
		"name": "bad_item",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Wrong legacy key: expected 401, got %d", rr.Code)
	}
}

// TestAccessEnvVarExpansion verifies $ENV_VAR expansion in access keys.
func TestAccessEnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

access:
  - role: env_user
    key: "$TEST_SPINE_KEY"

nodes:
  - name: api
    emits:
      - event: PING

routes:
  - on: PING
    steps:
      - action: log.write
        message: "pong"
`
	os.Setenv("TEST_SPINE_KEY", "sk_from_env_var")
	defer os.Unsetenv("TEST_SPINE_KEY")

	manifestPath := filepath.Join(dir, "env.spine")
	dbPath := filepath.Join(dir, "env.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	handler := eng.HTTPHandler()

	// Should authenticate with the env var value
	rr := doEmit(handler, "sk_from_env_var", "PING", map[string]interface{}{})
	if rr.Code != 200 {
		body, _ := io.ReadAll(rr.Result().Body)
		t.Errorf("Env var key emit: expected 200, got %d: %s", rr.Code, string(body))
	}

	// Should fail with wrong key
	rr = doEmit(handler, "wrong_key", "PING", map[string]interface{}{})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Wrong key with env expansion: expected 401, got %d", rr.Code)
	}
}

// TestAccessEnvDotVarExpansion verifies that access keys accept the documented
// "$env.VAR" syntax in addition to the legacy "$VAR" syntax.
func TestAccessEnvDotVarExpansion(t *testing.T) {
	dir := t.TempDir()
	manifestContent := `spine_version: 1

access:
  - role: env_user
    key: "$env.TEST_SPINE_DOTENV_KEY"

nodes:
  - name: api
    emits:
      - event: PING

routes:
  - on: PING
    steps:
      - action: log.write
        message: "pong"
`
	os.Setenv("TEST_SPINE_DOTENV_KEY", "sk_dotenv_value")
	defer os.Unsetenv("TEST_SPINE_DOTENV_KEY")

	manifestPath := filepath.Join(dir, "dotenv.spine")
	dbPath := filepath.Join(dir, "dotenv.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	handler := eng.HTTPHandler()

	// Should authenticate with the env var value
	rr := doEmit(handler, "sk_dotenv_value", "PING", map[string]interface{}{})
	if rr.Code != 200 {
		t.Errorf("$env.VAR key emit: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Should fail with wrong key
	rr = doEmit(handler, "wrong_key", "PING", map[string]interface{}{})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Wrong key with $env.VAR expansion: expected 401, got %d", rr.Code)
	}
}
