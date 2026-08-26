package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// TestFTSProvisionAndSearch is the regression test for fts.search being
// non-functional: previously no FTS5 virtual table was ever created, the
// MATCH query always failed, and the error was swallowed (route succeeded
// with empty results). Now ensureFTS provisions the index on first use and
// ftsSearch returns real matches, stays in sync with inserts, and fails
// loudly on malformed queries or missing tables.
//
// NOTE: requires the sqlite_fts5 build tag (Makefile/CI build with
// -tags sqlite_fts5); without it the driver lacks FTS5 and CREATE VIRTUAL
// TABLE fails with "no such module: fts5".
func TestFTSProvisionAndSearch(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "app.db")

	manifestContent := `spine_version: 1
database:
  tables:
    - articles
nodes:
  - name: A
    emits:
      - event: SEED
        payload:
          title: string
          body: string
routes:
  - on: SEED
    steps:
      - action: db.insert
        table: articles
        sync: "true"
`
	if err := os.WriteFile(spineFile, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()
	bus := eng.Bus

	// Seed rows (sync inserts — durable before the search runs).
	if _, err := bus.Emit("SEED", map[string]interface{}{"title": "Hello World", "body": "spine fts five test"}); err != nil {
		t.Fatalf("seed emit 1 failed: %v", err)
	}
	if _, err := bus.Emit("SEED", map[string]interface{}{"title": "Goodbye", "body": "unrelated text"}); err != nil {
		t.Fatalf("seed emit 2 failed: %v", err)
	}

	step := &manifest.RouteStep{
		Action: "fts.search",
		Table:  "articles",
		Config: map[string]string{"query": "$event.payload.q"},
	}

	// Matching query returns the right row with its content.
	payload := map[string]interface{}{"q": "spine"}
	if err := bus.ftsSearch(step, "SEARCH", payload); err != nil {
		t.Fatalf("ftsSearch failed: %v", err)
	}
	results, ok := payload["fts_results"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected payload[\"fts_results\"] to be []map[string]interface{}, got %T", payload["fts_results"])
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'spine', got %d: %v", len(results), results)
	}
	content, _ := results[0]["content"].(string)
	if !strings.Contains(content, "spine") || !strings.Contains(content, "five") {
		t.Errorf("expected content to contain indexed text, got %q", content)
	}

	// No-match query returns an empty (non-nil) result set.
	payload2 := map[string]interface{}{"q": "zzzz_nomatch"}
	if err := bus.ftsSearch(step, "SEARCH", payload2); err != nil {
		t.Fatalf("ftsSearch (no match) failed: %v", err)
	}
	if res := payload2["fts_results"].([]map[string]interface{}); len(res) != 0 {
		t.Errorf("expected 0 results for 'zzzz_nomatch', got %d", len(res))
	}

	// The index stays in sync with later inserts (trigger path).
	if _, err := bus.Emit("SEED", map[string]interface{}{"title": "More", "body": "spine again"}); err != nil {
		t.Fatalf("seed emit 3 failed: %v", err)
	}
	payload3 := map[string]interface{}{"q": "again"}
	if err := bus.ftsSearch(step, "SEARCH", payload3); err != nil {
		t.Fatalf("ftsSearch (after insert) failed: %v", err)
	}
	if res := payload3["fts_results"].([]map[string]interface{}); len(res) != 1 {
		t.Errorf("expected 1 result for 'again' after post-provision insert, got %d", len(res))
	}

	// Malformed FTS queries must fail loudly, not silently return empty.
	payload4 := map[string]interface{}{"q": "spine AND"}
	if err := bus.ftsSearch(step, "SEARCH", payload4); err == nil {
		t.Error("expected malformed FTS query ('spine AND') to return an error, got nil")
	}

	// Missing tables must fail loudly, not silently return empty.
	missing := &manifest.RouteStep{Action: "fts.search", Table: "does_not_exist", Config: map[string]string{"query": "x"}}
	if err := bus.ftsSearch(missing, "SEARCH", map[string]interface{}{}); err == nil {
		t.Error("expected ftsSearch on a missing table to return an error, got nil")
	}
}

func TestFTSProvisionAndSearch_ServerRestart(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "app.db")

	manifestContent := `spine_version: 1
database:
  tables:
    - articles
nodes:
  - name: A
    emits:
      - event: SEED
        payload:
          title: string
          body: string
routes:
  - on: SEED
    steps:
      - action: db.insert
        table: articles
        sync: "true"
`
	if err := os.WriteFile(spineFile, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng1, err := NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine 1: %v", err)
	}

	if _, err := eng1.Bus.Emit("SEED", map[string]interface{}{"title": "Restart Test", "body": "persisted search query"}); err != nil {
		t.Fatalf("seed emit failed: %v", err)
	}

	step := &manifest.RouteStep{
		Action: "fts.search",
		Table:  "articles",
		Config: map[string]string{"query": "$event.payload.q"},
	}

	p1 := map[string]interface{}{"q": "persisted"}
	if err := eng1.Bus.ftsSearch(step, "SEARCH", p1); err != nil {
		t.Fatalf("ftsSearch on eng1 failed: %v", err)
	}
	eng1.Close()

	// Re-open engine with existing database (simulates server restart)
	eng2, err := NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("Failed to re-init engine on restart: %v", err)
	}
	defer eng2.Close()

	p2 := map[string]interface{}{"q": "persisted"}
	if err := eng2.Bus.ftsSearch(step, "SEARCH", p2); err != nil {
		t.Fatalf("ftsSearch on eng2 after restart failed: %v", err)
	}
	res, ok := p2["fts_results"].([]map[string]interface{})
	if !ok || len(res) != 1 {
		t.Fatalf("expected 1 result after restart, got %d", len(res))
	}
}

