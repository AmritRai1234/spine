package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

// TestFullTextSearch is the black-box regression test for fts.search:
// previously the FTS5 index was never provisioned, the MATCH query always
// failed, the fallback searched the created_at timestamp column, and errors
// were swallowed — the route always "succeeded" with empty fts_results.
// Now search results must actually be returned, in sync with the data.
//
// NOTE: requires the sqlite_fts5 build tag (Makefile/CI build with
// -tags sqlite_fts5).
func TestFullTextSearch(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - articles
    - search_log

nodes:
  - name: ArticlesNode
    emits:
      - event: ADD_ARTICLE
        payload:
          title: string
          body: string
      - event: SEARCH_ARTICLES
        payload:
          q: string

routes:
  - on: ADD_ARTICLE
    steps:
      - action: db.insert
        table: articles
        sync: "true"

  - on: SEARCH_ARTICLES
    steps:
      - action: fts.search
        table: articles
        query: "$event.payload.q"
    emit: SEARCH_RESULTS

  - on: SEARCH_RESULTS
    steps:
      - action: db.insert
        table: search_log
        sync: "true"
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Seed articles.
	seeds := []map[string]interface{}{
		{"title": "Hello World", "body": "spine fts five test"},
		{"title": "Goodbye", "body": "unrelated text"},
	}
	for _, s := range seeds {
		if _, err := eng.Bus.Emit("ADD_ARTICLE", s); err != nil {
			t.Fatalf("seed emit failed: %v", err)
		}
	}

	readResults := func(q string) []map[string]interface{} {
		t.Helper()
		if _, err := eng.Bus.Emit("SEARCH_ARTICLES", map[string]interface{}{"q": q}); err != nil {
			t.Fatalf("search emit for %q failed: %v", q, err)
		}
		var raw string
		if err := eng.Bus.DB().QueryRow(`SELECT "fts_results" FROM search_log ORDER BY _spine_id DESC LIMIT 1`).Scan(&raw); err != nil {
			t.Fatalf("failed to read fts_results for %q: %v", q, err)
		}
		var results []map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &results); err != nil {
			t.Fatalf("failed to parse fts_results %q: %v", raw, err)
		}
		return results
	}

	// Matching query returns the seeded row with its content.
	results := readResults("spine")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'spine', got %d: %v", len(results), results)
	}
	if content, _ := results[0]["content"].(string); !strings.Contains(content, "spine") || !strings.Contains(content, "five") {
		t.Errorf("expected content to contain indexed text, got %q", content)
	}

	// No-match query returns an empty result set.
	if results := readResults("zzzz_nomatch"); len(results) != 0 {
		t.Errorf("expected 0 results for 'zzzz_nomatch', got %d", len(results))
	}

	// A malformed FTS query fails loudly instead of "succeeding" with empty results.
	if _, err := eng.Bus.Emit("SEARCH_ARTICLES", map[string]interface{}{"q": "spine AND"}); err == nil {
		t.Error("expected malformed FTS query to fail the emit, got nil error")
	}
}
