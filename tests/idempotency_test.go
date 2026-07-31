package tests

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
