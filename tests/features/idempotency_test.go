package features

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

func TestIdempotencyKeys(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - payments

routes:
  - on: CHARGE_PAYMENT
    steps:
      - action: db.insert
        table: payments
    emit: PAYMENT_SUCCESS
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	payload := map[string]interface{}{
		"amount":           100,
		"_idempotency_key": "pay_key_9999",
	}

	// First emission
	res1, err := eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err != nil {
		t.Fatalf("First emit failed: %v", err)
	}

	if res1["idempotent_hit"] == true {
		t.Error("Expected first emit to NOT be an idempotent hit")
	}

	// Re-emit with same idempotency key
	res2, err := eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err != nil {
		t.Fatalf("Second emit failed: %v", err)
	}

	if res2["idempotent_hit"] != true {
		t.Error("Expected second emit to be an idempotent hit")
	}

	// Wait briefly for async batch writer flush
	time.Sleep(150 * time.Millisecond)

	// Verify database only has 1 row inserted
	rows, err := eng.Bus.GetTableRows("payments", 10, 0)
	if err != nil {
		t.Fatalf("Failed to query table: %v", err)
	}

	if len(rows) != 1 {
		t.Errorf("Expected exactly 1 row in payments table, got %d", len(rows))
	}
}

func TestIdempotencyKeys_Conflict(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - payments

routes:
  - on: CHARGE_PAYMENT
    steps:
      - action: db.insert
        table: payments
    emit: PAYMENT_SUCCESS
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Manually insert a 'running' claim to simulate an in-flight request
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = eng.Bus.DB().Exec(`INSERT OR IGNORE INTO "_spine_idem" (key, status, result_json, created_at) VALUES (?, 'running', NULL, ?)`, "conflict_key", now)
	if err != nil {
		t.Fatalf("Failed to insert idempotency claim: %v", err)
	}

	// Re-emit with same key — should get a conflict error
	payload := map[string]interface{}{
		"amount":           200,
		"_idempotency_key": "conflict_key",
	}
	_, err = eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err == nil {
		t.Fatal("Expected conflict error for in-flight idempotency key, got nil")
	}
	t.Logf("Got expected conflict error: %v", err)
}

func TestIdempotencyKeys_StaleSteal(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - payments

routes:
  - on: CHARGE_PAYMENT
    steps:
      - action: db.insert
        table: payments
    emit: PAYMENT_SUCCESS
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Manually insert a stale 'running' claim (7 minutes old) — past the 5-min TTL
	staleTime := time.Now().Add(-7 * time.Minute).UTC().Format(time.RFC3339)
	_, err = eng.Bus.DB().Exec(`INSERT OR IGNORE INTO "_spine_idem" (key, status, result_json, created_at) VALUES (?, 'running', NULL, ?)`, "stale_key", staleTime)
	if err != nil {
		t.Fatalf("Failed to insert stale claim: %v", err)
	}

	// Re-emit with same key — should steal the stale claim and succeed
	payload := map[string]interface{}{
		"amount":           300,
		"_idempotency_key": "stale_key",
	}
	res, err := eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err != nil {
		t.Fatalf("Emit with stale key steal failed: %v", err)
	}
	if res["idempotent_hit"] == true {
		t.Error("Expected stolen claim to NOT be an idempotent hit (first real execution)")
	}
	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%v'", res["status"])
	}

	// Now verify a second emit returns the cached result
	res2, err := eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err != nil {
		t.Fatalf("Second emit after steal failed: %v", err)
	}
	if res2["idempotent_hit"] != true {
		t.Error("Expected second emit after steal to be an idempotent hit")
	}
}

func TestIdempotencyKeys_CompletedReplay(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - payments

routes:
  - on: CHARGE_PAYMENT
    steps:
      - action: db.insert
        table: payments
    emit: PAYMENT_SUCCESS
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Manually insert a completed claim with a pre-computed result
	resultJSON := `{"status":"ok","event":"CHARGE_PAYMENT","routes_matched":1,"emitted_states":["PAYMENT_SUCCESS"]}`
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = eng.Bus.DB().Exec(`INSERT OR IGNORE INTO "_spine_idem" (key, status, result_json, created_at) VALUES (?, 'completed', ?, ?)`, "completed_key", resultJSON, now)
	if err != nil {
		t.Fatalf("Failed to insert completed claim: %v", err)
	}

	// Re-emit with same key — should return cached result
	payload := map[string]interface{}{
		"amount":           400,
		"_idempotency_key": "completed_key",
	}
	res, err := eng.Bus.Emit("CHARGE_PAYMENT", payload)
	if err != nil {
		t.Fatalf("Completed replay failed: %v", err)
	}
	if res["idempotent_hit"] != true {
		t.Error("Expected completed replay to be an idempotent hit")
	}
	if res["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%v'", res["status"])
	}
	if res["event"] != "CHARGE_PAYMENT" {
		t.Errorf("Expected event 'CHARGE_PAYMENT', got '%v'", res["event"])
	}
}

func TestIdempotencyKeys_FailedExecution(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	// Route that will fail: db.lookup requires both key_column and value_expr config,
	// but we omit value_expr so it returns an error — simulating a route failure.
	manifest := `spine_version: 1
database:
  tables:
    - inventory

routes:
  - on: RESERVE_STOCK
    steps:
      - action: db.lookup
        table: inventory
        config:
          key_column: "sku"
    emit: STOCK_RESERVED
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	payload := map[string]interface{}{
		"sku":              "ABC-123",
		"_idempotency_key": "fail_key_001",
	}

	// First emission — should fail because db.lookup is misconfigured
	_, err = eng.Bus.Emit("RESERVE_STOCK", payload)
	if err == nil {
		t.Fatal("Expected first emit to fail due to misconfigured db.lookup, but it succeeded")
	}
	t.Logf("First emit failed as expected: %v", err)

	// The failed execution should have deleted the claim — verify by re-emitting
	// with the same key; it should NOT be an idempotent hit (the claim was cleaned up).
	res, err := eng.Bus.Emit("RESERVE_STOCK", payload)
	if err == nil {
		t.Fatal("Expected second emit to also fail (route is still broken)")
	}
	if res != nil && res["idempotent_hit"] == true {
		t.Error("Expected second emit to NOT be an idempotent hit (failed execution should have cleaned the claim)")
	}
}
