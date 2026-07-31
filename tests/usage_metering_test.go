package tests

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestUsageMeteringEndpoint(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - users
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

	resp, err := server.Client().Get(server.URL + "/admin/usage")
	if err != nil {
		t.Fatalf("Failed GET /admin/usage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var usage map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if usage["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", usage["status"])
	}
	if _, ok := usage["events_per_second"]; !ok {
		t.Error("Expected events_per_second field")
	}
	if _, ok := usage["ws_connections"]; !ok {
		t.Error("Expected ws_connections field")
	}
}
