package spine

import (
	"os"
	"testing"
)

func TestParallelExecutionAndRetries(t *testing.T) {
	manifest := `
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
	tmpManifest.WriteString(manifest)
	tmpManifest.Close()

	schema, err := ParseManifest(tmpManifest.Name())
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if !schema.Routes[0].Parallel {
		t.Errorf("expected route parallel: true")
	}

	dbPath := "test_parallel.db"
	defer os.Remove(dbPath)

	reg := NewRegistry(schema)
	hub := NewHub()
	bus, err := NewBus(reg, dbPath, hub)
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
