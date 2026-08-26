package features

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

// ==================== Test 1: Numeric Field Validation & SQLite Storage ====================

func TestNumericPayloadFields(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "numeric_test.db")
	spineFile := filepath.Join(tempDir, "numeric_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - metrics

nodes:
  AnalyticsNode:
    owns_files:
      - metrics.ts
    emits:
      - event: RECORD_METRIC
        payload:
          score: float
          progress: int
          segment_count: integer
          duration: number

routes:
  - on: RECORD_METRIC
    steps:
      - action: db.insert
        table: metrics
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write spine file: %v", err)
	}

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Emit valid numeric values (numbers & parseable numeric strings)
	payload := map[string]interface{}{
		"score":         98.5,
		"progress":      75,
		"segment_count": 12,
		"duration":      120.4,
	}

	res, err := engine.Bus.Emit("RECORD_METRIC", payload)
	if err != nil {
		t.Fatalf("emit failed for valid numeric payload: %v", err)
	}
	if res["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", res["status"])
	}

	time.Sleep(100 * time.Millisecond)

	rows, err := engine.Bus.GetTableRows("metrics", 10, 0)
	if err != nil {
		t.Fatalf("failed to get table rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	// Test invalid numeric type fails validation
	invalidPayload := map[string]interface{}{
		"score":         "invalid_number",
		"progress":      75,
		"segment_count": 12,
		"duration":      120.4,
	}

	_, err = engine.Bus.Emit("RECORD_METRIC", invalidPayload)
	if err == nil {
		t.Fatal("expected validation error for string in score field, got nil")
	}
}

// ==================== Test 2: Route-level on_failure ====================

func TestRouteOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "on_failure_test.db")
	spineFile := filepath.Join(tempDir, "on_failure_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - audit_logs

nodes:
  PipelineNode:
    owns_files:
      - pipeline.ts
    emits:
      - event: START_PIPELINE
        payload:
          project_id: string

routes:
  - on: START_PIPELINE
    on_failure: PROCESSING_FAILED
    steps:
      - action: failing.action

  - on: PROCESSING_FAILED
    steps:
      - action: db.insert
        table: audit_logs
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write spine file: %v", err)
	}

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Register failing action
	engine.Bus.RegisterAction("failing.action", func(step *spine.RouteStep, eventName string, payload map[string]interface{}) error {
		return errors.New("pipeline engine crashed")
	})

	res, err := engine.Bus.Emit("START_PIPELINE", map[string]interface{}{"project_id": "proj_123"})
	if err == nil {
		t.Fatal("expected error from Emit when step fails, got nil")
	}

	// Check response status and emitted_states
	if res["status"] != "error" {
		t.Errorf("expected status=error, got %v", res["status"])
	}
	emittedStates, ok := res["emitted_states"].([]string)
	if !ok {
		t.Fatalf("expected emitted_states slice, got %T", res["emitted_states"])
	}
	if len(emittedStates) == 0 || emittedStates[0] != "PROCESSING_FAILED" {
		t.Errorf("expected emitted_states to contain PROCESSING_FAILED, got %v", emittedStates)
	}

	// Verify state cache received error state with context
	state, exists := engine.Bus.GetState("PROCESSING_FAILED")
	if !exists {
		t.Fatal("expected PROCESSING_FAILED state in state cache")
	}
	if state["project_id"] != "proj_123" {
		t.Errorf("expected project_id=proj_123 in error state, got %v", state["project_id"])
	}
	if state["failed_action"] != "failing.action" {
		t.Errorf("expected failed_action=failing.action, got %v", state["failed_action"])
	}

	// Verify chained route triggered and inserted into audit_logs
	time.Sleep(100 * time.Millisecond)
	rows, err := engine.Bus.GetTableRows("audit_logs", 10, 0)
	if err != nil {
		t.Fatalf("failed to get audit_logs rows: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row in audit_logs from chained error route, got %d", len(rows))
	}
}

// ==================== Test 3: Step-level on_failure ====================

func TestStepOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "step_failure_test.db")
	spineFile := filepath.Join(tempDir, "step_failure_test.spine")

	manifest := `spine_version: 1
database:
  tables:
    - errors

nodes:
  ProcessorNode:
    owns_files:
      - proc.ts
    emits:
      - event: PROCESS_DATA
        payload:
          task_id: string

routes:
  - on: PROCESS_DATA
    steps:
      - action: custom.step1
        on_failure: STEP1_FAILED

  - on: STEP1_FAILED
    steps:
      - action: db.insert
        table: errors
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write spine file: %v", err)
	}

	engine, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	engine.Bus.RegisterAction("custom.step1", func(step *spine.RouteStep, eventName string, payload map[string]interface{}) error {
		return errors.New("step 1 execution error")
	})

	res, err := engine.Bus.Emit("PROCESS_DATA", map[string]interface{}{"task_id": "t_456"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	emittedStates, ok := res["emitted_states"].([]string)
	if !ok || len(emittedStates) == 0 || emittedStates[0] != "STEP1_FAILED" {
		t.Errorf("expected emitted_states=[STEP1_FAILED], got %v", emittedStates)
	}

	// Verify state cache
	state, exists := engine.Bus.GetState("STEP1_FAILED")
	if !exists {
		t.Fatal("expected STEP1_FAILED in state cache")
	}
	if state["task_id"] != "t_456" {
		t.Errorf("expected task_id=t_456, got %v", state["task_id"])
	}
}

// TestOnFailureErrorContextPreservation verifies _error_context and original trigger attributes in failure state.
func TestOnFailureErrorContextPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	spineFile := filepath.Join(tmpDir, "failure.spine")
	dbPath := filepath.Join(tmpDir, "failure.db")

	manifestContent := `spine_version: 1

routes:
  - on: TRIGGER_FAIL
    on_failure: HANDLE_FAILURE
    steps:
      - action: http.post
        url: "http://invalid.local.domain.does.not.exist:9999/test"
`
	os.WriteFile(spineFile, []byte(manifestContent), 0644)

	eng, err := engine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	initialPayload := map[string]interface{}{
		"order_id": "ORD-12345",
		"amount":   99.50,
	}

	res, err := eng.Bus.Emit("TRIGGER_FAIL", initialPayload)
	if err == nil {
		t.Fatalf("expected route execution to fail")
	}

	if res == nil || res["status"] != "error" {
		t.Fatalf("expected status error in result, got: %v", res)
	}

	failPayload, ok := eng.Bus.GetState("HANDLE_FAILURE")
	if !ok {
		t.Fatalf("expected failure state HANDLE_FAILURE to be set")
	}

	// Verify original payload attributes were preserved
	if failPayload["order_id"] != "ORD-12345" {
		t.Fatalf("expected order_id to be preserved, got %v", failPayload["order_id"])
	}

	// Verify _error_context
	errCtx, ok := failPayload["_error_context"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected _error_context map in failure payload")
	}

	if errCtx["failed_event"] != "TRIGGER_FAIL" {
		t.Fatalf("expected failed_event TRIGGER_FAIL, got %v", errCtx["failed_event"])
	}
	if errCtx["failed_action"] != "http.post" {
		t.Fatalf("expected failed_action http.post, got %v", errCtx["failed_action"])
	}

	origMap, ok := errCtx["original_payload"].(map[string]interface{})
	if !ok || origMap["order_id"] != "ORD-12345" {
		t.Fatalf("expected original_payload in _error_context to preserve order_id")
	}
}
