package security

// Regression test for the stale-plan-projection flake
// (.hermes/issues/flaky-table-scope-content-visibility.md).
//
// Mechanism: mattn/go-sqlite3 caches compiled statements per connection.
// A pooled connection whose `SELECT *` plan was compiled before a lazy
// ALTER TABLE (first-insert ensureTable evolution) replays the STALE
// two-column projection — rows appear without their payload columns, which
// broke TestTableScopeAllowedTableFiltered ~2-4% of runs.
//
// Deterministic RED/GREEN shape: pre-create the table exactly as
// EnsureTables does (base columns only, committed BEFORE any read has
// happened), then compile the read path's plan on pooled connections via a
// first read, THEN evolve the schema the way dbInsert's ensureTable does.
// Every read after that must project the payload column. With SELECT *
// this fails reliably (stale plan is certain on the conn that served the
// pre-DDL read); with explicit column lists the post-DDL query text can
// only compile after the ALTER — structurally stale-proof.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestStalePlanProjectionRace(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(tableScopeManifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	server := httptest.NewServer(eng.HTTPHandler())
	t.Cleanup(func() { server.Close(); _ = eng.Close() })

	// Step 1: pre-create the table with ONLY the EnsureTables base columns
	// and commit. No reader has ever seen anything else.
	if _, err := eng.Bus.DB().Exec(`CREATE TABLE IF NOT EXISTS "orders" (_spine_id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}

	// Step 2: warm the read path's plan on a pooled connection — one scoped
	// read against the two-column table. This conn now holds a cached
	// `SELECT *` plan for the OLD schema.
	scopedRead := func() (int, string) {
		req, _ := http.NewRequest("GET", server.URL+"/tables/orders", nil)
		req.Header.Set("X-API-Key", "shopper-scope-key")
		resp, err := server.Client().Do(req)
		if err != nil {
			return -1, err.Error()
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(raw)
	}
	if code, _ := scopedRead(); code != 200 {
		t.Fatalf("warmup read: %d", code)
	}

	// Step 3: evolve the schema through the ENGINE's own evolution path —
	// a first-insert db.insert on the new column (same as any manifest
	// route). This is the shape production evolution takes; the epoch
	// cache is keyed on ensureTable, which this exercises. (Raw SQL DDL
	// is out-of-band and consciously not tracked — see
	// TestSchemaEpochOutOfBandDDLContract.)
	if _, err := eng.Bus.Emit("NEW_ORDER", map[string]interface{}{"email": "mine@example.com"}); err != nil {
		t.Fatal(err)
	}

	// Step 4: read through the pool. The connection that served the warmup
	// read may serve this one; with SELECT * it replays its stale plan.
	for i := 0; i < 200; i++ {
		code, body := scopedRead()
		if code != 200 {
			t.Fatalf("read %d: %d %s", i, code, body)
		}
		var resp struct {
			Rows []map[string]interface{} `json:"rows"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("read %d: bad json: %s", i, body)
		}
		for _, row := range resp.Rows {
			if _, hasEmail := row["email"]; !hasEmail {
				t.Fatalf("stale projection observed at read %d: %s", i, body)
			}
		}
	}
}
