package tests

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestMetricsEndpoint(t *testing.T) {
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

	resp, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("Failed GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}

	metricsStr := string(body)
	if !strings.Contains(metricsStr, "spine_requests_per_second") {
		t.Errorf("Expected spine_requests_per_second metric, got:\n%s", metricsStr)
	}
	if !strings.Contains(metricsStr, "spine_optimizer_batch_size") {
		t.Errorf("Expected spine_optimizer_batch_size metric, got:\n%s", metricsStr)
	}
}
