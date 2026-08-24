package tests

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestParallelExecutionAndRetries(t *testing.T) {
	manifestContent := `
spine_version: 1

database:
  tables:
    - parallel_events

routes:
  - on: PROCESS_BATCH
    parallel: true
    steps:
      - action: log.write
        message: "Parallel step 1"
      - action: log.write
        message: "Parallel step 2"
      - action: db.insert
        table: parallel_events
        max_attempts: 2
        backoff_ms: 10
`

	tmpManifest, err := os.CreateTemp("", "parallel_*.spine")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpManifest.Name())
	tmpManifest.WriteString(manifestContent)
	tmpManifest.Close()

	schema, err := manifest.ParseManifest(tmpManifest.Name())
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if !schema.Routes[0].Parallel {
		t.Errorf("expected route parallel: true")
	}

	dbPath := "test_parallel.db"
	defer os.Remove(dbPath)

	reg := manifest.NewRegistry(schema)
	hub := engine.NewHub()
	bus, err := engine.NewBus(reg, dbPath, hub)
	if err != nil {
		t.Fatalf("NewBus failed: %v", err)
	}
	defer bus.Close()

	res, err := bus.Emit("PROCESS_BATCH", map[string]interface{}{
		"item":   "test_item",
		"status": "processed",
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	if status, ok := res["status"].(string); !ok || status != "ok" {
		t.Errorf("expected status 'ok', got %v", res)
	}
}

// TestParallelRouteThreadSafety verifies parallel route steps run concurrently without data races or slice corruption.
func TestParallelRouteThreadSafety(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "parallel.spine")
	dbPath := filepath.Join(tmpDir, "parallel.db")

	manifestContent := `spine_version: 1

nodes:
  - name: api
    emits:
      - event: PROCESS_DATA
        payload:
          id: string
          val1: string
          val2: string

routes:
  - on: PROCESS_DATA
    parallel: true
    steps:
      - action: set
        val1: "computed_1"
      - action: set
        val2: "computed_2"
      - action: log.write
        message: "Processing $event.payload.id"
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	var wg sync.WaitGroup
	concurrentReqs := 20
	for i := 0; i < concurrentReqs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := map[string]interface{}{
				"id":   "req_" + string(rune(idx)),
				"val1": "initial1",
				"val2": "initial2",
			}
			_, err := eng.Bus.Emit("PROCESS_DATA", payload)
			if err != nil {
				t.Errorf("parallel emit failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
