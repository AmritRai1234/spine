package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

func TestScheduledCronEvents(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - cron_logs

routes:
  - on: TICK_EVENT
    cron: "1s"
    steps:
      - action: db.insert
        table: cron_logs
    emit: TICK_STATE
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	// Wait 2.5 seconds for cron ticker to execute at least once
	time.Sleep(2500 * time.Millisecond)

	rows, err := eng.Bus.GetTableRows("cron_logs", 10, 0)
	if err != nil {
		t.Fatalf("Failed to query cron_logs table: %v", err)
	}

	if len(rows) == 0 {
		t.Errorf("Expected scheduled cron worker to insert rows into cron_logs, got 0")
	}
}
