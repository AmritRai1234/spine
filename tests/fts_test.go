package tests

import (
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestFullTextSearch(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - articles

routes:
  - on: SEARCH_ARTICLES
    steps:
      - action: fts.search
        table: articles
        query: "$event.payload.q"
    emit: SEARCH_RESULTS
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Test fts.search step execution
	payload := map[string]interface{}{"q": "event"}
	res, err := eng.Bus.Emit("SEARCH_ARTICLES", payload)
	if err != nil {
		t.Fatalf("Emit SEARCH_ARTICLES failed: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", res["status"])
	}
}
