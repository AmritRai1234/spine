package tests

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
	"github.com/AmritRai1234/spine/pkg/middleware"
)

const featuresManifest = `spine_version: 1

database:
  tables:
    - items
    - accounts

nodes:
  - name: feature_api
    emits:
      - event: CREATE_ITEM
        payload:
          name: string
          category: string
      - event: PROCESS_SAGA
        payload:
          acc_id: string
          amount: number

routes:
  - on: CREATE_ITEM
    steps:
      - action: db.insert
        table: items

  - on: PROCESS_SAGA
    steps:
      - action: db.insert
        table: accounts
        compensate: db.delete
        config:
          compensate_where: "acc_id = '$event.payload.acc_id'"
      - action: http.post
        url: "http://127.0.0.1:9999/non_existent_fail"
        max_attempts: 1
`

func TestDepthLimitMiddleware(t *testing.T) {
	depth2JSON := `{"a": {"b": "value"}}`
	depth, err := middleware.CalculateJSONDepth([]byte(depth2JSON))
	if err != nil || depth != 2 {
		t.Fatalf("Expected depth 2, got %d (err: %v)", depth, err)
	}

	handler := middleware.DepthLimitMiddleware(2, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	// Depth 2 allowed
	req := httptest.NewRequest("POST", "/emit", strings.NewReader(depth2JSON))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("Expected 200 for depth 2, got %d", rr.Code)
	}

	// Depth 4 rejected
	depth4JSON := `{"a": {"b": {"c": {"d": "nested"}}}}`
	req4 := httptest.NewRequest("POST", "/emit", strings.NewReader(depth4JSON))
	rr4 := httptest.NewRecorder()
	handler.ServeHTTP(rr4, req4)
	if rr4.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for depth 4 (limit 2), got %d", rr4.Code)
	}
}

func TestCursorAndMultiWhereQueries(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "feat.spine")
	dbPath := filepath.Join(dir, "feat.db")
	os.WriteFile(manifestPath, []byte(featuresManifest), 0644)

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Engine init failed: %v", err)
	}
	defer eng.Close()

	// Insert 5 items
	for i := 1; i <= 5; i++ {
		eng.Bus.Emit("CREATE_ITEM", map[string]interface{}{
			"name":     "item",
			"category": "cat_a",
		})
	}
	time.Sleep(100 * time.Millisecond)

	// Keyset Cursor Pagination
	rows1, nextCursor, err := eng.Bus.GetTableRowsCursor("items", 0, 2, "")
	if err != nil {
		t.Fatalf("Cursor query page 1 failed: %v", err)
	}
	if len(rows1) != 2 {
		t.Errorf("Expected 2 rows on page 1, got %d", len(rows1))
	}
	if nextCursor <= 0 {
		t.Errorf("Expected nextCursor > 0, got %d", nextCursor)
	}

	rows2, _, err := eng.Bus.GetTableRowsCursor("items", nextCursor, 2, "")
	if err != nil {
		t.Fatalf("Cursor query page 2 failed: %v", err)
	}
	if len(rows2) != 2 {
		t.Errorf("Expected 2 rows on page 2, got %d", len(rows2))
	}

	// Multi-where query
	multiRows, err := eng.Bus.QueryMultiWhere("items", map[string]string{
		"name":     "item",
		"category": "cat_a",
	}, 10, 0, "")
	if err != nil {
		t.Fatalf("MultiWhere query failed: %v", err)
	}
	if len(multiRows) != 5 {
		t.Errorf("Expected 5 rows matching multi-where, got %d", len(multiRows))
	}
}

func TestManifestDiff(t *testing.T) {
	oldSchema := &manifest.SpineSchema{
		DbTables: []string{"users"},
		Routes:   []manifest.Route{{OnEvent: "EVT_A"}},
		Nodes:    []manifest.Node{{Name: "NodeA"}},
	}
	newSchema := &manifest.SpineSchema{
		DbTables: []string{"users", "orders"},
		Routes:   []manifest.Route{{OnEvent: "EVT_A"}, {OnEvent: "EVT_B"}},
		Nodes:    []manifest.Node{{Name: "NodeA"}},
	}

	diff := engine.DiffManifests(oldSchema, newSchema)
	if !diff.HasChanges() {
		t.Error("Expected diff changes, found none")
	}
	if len(diff.AddedTables) != 1 || diff.AddedTables[0] != "orders" {
		t.Errorf("Expected added table 'orders', got %v", diff.AddedTables)
	}
	if len(diff.AddedRoutes) != 1 || diff.AddedRoutes[0] != "EVT_B" {
		t.Errorf("Expected added route 'EVT_B', got %v", diff.AddedRoutes)
	}
}

func TestSagaCompensation(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "saga.spine")
	dbPath := filepath.Join(dir, "saga.db")
	os.WriteFile(manifestPath, []byte(featuresManifest), 0644)

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Engine init failed: %v", err)
	}
	defer eng.Close()

	// Emit saga event where step 1 inserts account, step 2 fails on bad HTTP request
	_, emitErr := eng.Bus.Emit("PROCESS_SAGA", map[string]interface{}{
		"acc_id": "acc_1001",
		"amount": 500,
	})
	if emitErr == nil {
		t.Error("Expected saga step 2 to fail, but emit succeeded")
	}

	time.Sleep(150 * time.Millisecond)

	// Verify step 1 insertion was rolled back by step 1 compensation (db.delete)
	rows, err := eng.Bus.GetTableRows("accounts", 10, 0)
	if err != nil {
		t.Fatalf("Failed to query accounts table: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Expected 0 rows in accounts after saga rollback, got %d", len(rows))
	}
}

func TestEventReplay(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "replay.spine")
	dbPath := filepath.Join(dir, "replay.db")
	os.WriteFile(manifestPath, []byte(featuresManifest), 0644)

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Engine init failed: %v", err)
	}
	defer eng.Close()

	// Emit initial event to audit log
	eng.Bus.Emit("CREATE_ITEM", map[string]interface{}{
		"name":     "item_to_replay",
		"category": "test_cat",
	})
	time.Sleep(100 * time.Millisecond)

	// Dry run replay
	dryResults, err := eng.Bus.ReplayEvents(spine.ReplayFilter{
		EventName: "CREATE_ITEM",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Dry run replay failed: %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Status != "dry_run" {
		t.Errorf("Expected 1 dry run replay result with status 'dry_run', got %v", dryResults)
	}

	// Real replay
	realResults, err := eng.Bus.ReplayEvents(spine.ReplayFilter{
		EventName: "CREATE_ITEM",
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("Real replay failed: %v", err)
	}
	if len(realResults) != 1 || realResults[0].Status != "replayed" {
		t.Errorf("Expected 1 real replay result with status 'replayed', got %v", realResults)
	}
}
