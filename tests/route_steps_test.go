package tests

// Route step tests: RouteStep Config map parsing, custom actions reading
// Config, and known-key isolation from Config.

import (
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestRouteStepConfigParsing(t *testing.T) {
	tempDir := t.TempDir()
	spineFile := filepath.Join(tempDir, "config_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - jobs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: RUN_PIPELINE
        payload:
          project_id: string

routes:
  - on: RUN_PIPELINE
    steps:
      - action: pipeline.run
        channel: fast-lane
        priority: high
        timeout_sec: 30
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	schema, err := spine.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if len(schema.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(schema.Routes))
	}
	step := schema.Routes[0].Steps[0]

	if step.Action != "pipeline.run" {
		t.Errorf("expected action=pipeline.run, got %s", step.Action)
	}
	if step.Config == nil {
		t.Fatal("expected Config map to be populated, got nil")
	}
	if step.Config["channel"] != "fast-lane" {
		t.Errorf("expected Config[channel]=fast-lane, got %s", step.Config["channel"])
	}
	if step.Config["priority"] != "high" {
		t.Errorf("expected Config[priority]=high, got %s", step.Config["priority"])
	}
	if step.Config["timeout_sec"] != "30" {
		t.Errorf("expected Config[timeout_sec]=30, got %s", step.Config["timeout_sec"])
	}
}

func TestRouteStepConfigInAction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "config_action_test.db")
	spineFile := filepath.Join(tempDir, "config_action_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - jobs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: RUN_JOB
        payload:
          job_name: string

routes:
  - on: RUN_JOB
    steps:
      - action: custom.job
        queue: priority
        retries: 5
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Register custom action that reads Config
	var receivedQueue, receivedRetries string
	engine.Bus.RegisterAction("custom.job", func(step *spine.RouteStep, eventName string, payload map[string]interface{}) error {
		receivedQueue = step.Config["queue"]
		receivedRetries = step.Config["retries"]
		return nil
	})

	_, err = engine.Bus.Emit("RUN_JOB", map[string]interface{}{"job_name": "test-job"})
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	if receivedQueue != "priority" {
		t.Errorf("expected queue=priority, got %s", receivedQueue)
	}
	if receivedRetries != "5" {
		t.Errorf("expected retries=5, got %s", receivedRetries)
	}
}

// Backwards Compatibility: known keys should NOT leak into Config

func TestRouteStepKnownKeysNotInConfig(t *testing.T) {
	tempDir := t.TempDir()
	spineFile := filepath.Join(tempDir, "known_keys_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - logs

nodes:
  TestNode:
    owns_files:
      - test.ts
    emits:
      - event: LOG_EVENT
        payload:
          msg: string

routes:
  - on: LOG_EVENT
    steps:
      - action: db.insert
        table: logs
        input: "$event.payload"
        where: "id = 1"
`
	os.WriteFile(spineFile, []byte(manifest), 0644)

	schema, err := spine.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	step := schema.Routes[0].Steps[0]

	// Known keys should be in their struct fields
	if step.Table != "logs" {
		t.Errorf("expected Table=logs, got %s", step.Table)
	}
	if step.Input != "$event.payload" {
		t.Errorf("expected Input=$event.payload, got %s", step.Input)
	}
	if step.Where != "id = 1" {
		t.Errorf("expected Where='id = 1', got %s", step.Where)
	}

	// Config should be nil — no unknown keys
	if step.Config != nil {
		t.Errorf("expected Config to be nil for known-keys-only step, got %v", step.Config)
	}
}
