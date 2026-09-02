package features

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

// newMetricsEngine builds an engine for metrics-endpoint tests. apiKey "" and
// failClosed false = legacy permissive mode, so the only thing gating
// /metrics is the metrics-specific public-opt-in check.
func newMetricsEngine(t *testing.T, apiKey string) *spine.Engine {
	t.Helper()
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
	if apiKey != "" {
		eng.SetAPIKey(apiKey)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func getMetrics(t *testing.T, eng *spine.Engine, apiKey string) (*http.Response, string) {
	t.Helper()
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Failed GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	// Body must be read before server.Close returns — snapshot it.
	return resp, string(body)
}

// TestMetricsEndpoint verifies the metrics body content when the deployment
// is explicitly declared public (SPINE_METRICS_PUBLIC=1).
func TestMetricsEndpoint(t *testing.T) {
	t.Setenv("SPINE_METRICS_PUBLIC", "1")
	eng := newMetricsEngine(t, "")

	resp, body := getMetrics(t, eng, "")
	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(body, "spine_requests_per_second") {
		t.Errorf("Expected spine_requests_per_second metric, got:\n%s", body)
	}
	if !strings.Contains(body, "spine_optimizer_batch_size") {
		t.Errorf("Expected spine_optimizer_batch_size metric, got:\n%s", body)
	}
}

// TestMetricsRequiresAuth verifies M1 hardening: with an API key configured,
// unauthenticated scrapes get 401 — no recon for free.
func TestMetricsRequiresAuth(t *testing.T) {
	eng := newMetricsEngine(t, "secret-key-123")

	resp, body := getMetrics(t, eng, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /metrics: expected 401, got %d", resp.StatusCode)
	}
	if strings.Contains(body, "spine_requests_per_second") {
		t.Errorf("metric body leaked to unauthenticated caller:\n%s", body)
	}

	resp, body = getMetrics(t, eng, "wrong-key")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-key /metrics: expected 401, got %d", resp.StatusCode)
	}

	resp, body = getMetrics(t, eng, "secret-key-123")
	if resp.StatusCode != 200 {
		t.Errorf("valid-key /metrics: expected 200, got %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "spine_commit_failures") {
		t.Errorf("durability counters missing with valid key:\n%s", body)
	}
}

// TestMetricsPublicOptIn verifies SPINE_METRICS_PUBLIC=1 opens /metrics only
// when NO auth is configured — with a key set, the key is still required.
func TestMetricsPublicOptIn(t *testing.T) {
	t.Setenv("SPINE_METRICS_PUBLIC", "1")
	eng := newMetricsEngine(t, "")

	resp, _ := getMetrics(t, eng, "")
	if resp.StatusCode != 200 {
		t.Errorf("public opt-in with no key: expected 200, got %d", resp.StatusCode)
	}
}
