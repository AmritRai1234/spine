package features

// Write-path durability tests: prove the batch writer NEVER silently drops a
// batch — a busy commit is retried (whole-batch), and a persistent commit
// failure spills the batch to _spine_write_spill for replay.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

const durabilityManifest = `spine_version: 1

database:
  tables:
    - events_durability

routes:
  - on: DUR_INSERT
    steps:
      - action: db.insert
        table: events_durability
`

func newDurabilityEngineAt(t *testing.T, spineFile, dbPath string) *spine.Engine {
	t.Helper()
	eng, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func newDurabilityEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(spineFile, []byte(durabilityManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng := newDurabilityEngineAt(t, spineFile, filepath.Join(dir, "spine.db"))
	t.Cleanup(func() { engine.SetCommitFailureHook(nil) })
	return eng
}

func durabilityRowCount(t *testing.T, eng *spine.Engine) int {
	t.Helper()
	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "events_durability"`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// waitForTableRows polls until table has at least want rows (generic —
// waitForRow counts the durability engine's dedicated table).
func waitForTableRows(t *testing.T, eng *spine.Engine, table string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := eng.Bus.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)).Scan(&n)
		if err == nil && n >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var n int
	_ = eng.Bus.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)).Scan(&n)
	t.Fatalf("timed out: expected >= %d rows in %s, got %d", want, table, n)
}

func waitForRow(t *testing.T, eng *spine.Engine, timeout time.Duration, want int) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if durabilityRowCount(t, eng) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: expected >= %d rows, got %d", want, durabilityRowCount(t, eng))
}

// TestBatchCommitRetryOnBusy proves a transient busy commit is retried at the
// whole-batch level and the write lands WITHOUT spilling.
func TestBatchCommitRetryOnBusy(t *testing.T) {
	eng := newDurabilityEngine(t)

	failures := 0
	engine.SetCommitFailureHook(func() error {
		failures++
		if failures <= 1 {
			return errors.New("database is locked (injected)")
		}
		return nil
	})

	if _, err := eng.Bus.Emit("DUR_INSERT", map[string]interface{}{"msg": "retry-me"}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	waitForRow(t, eng, 5*time.Second, 1)
	if failures < 2 {
		t.Fatalf("expected at least 2 commit attempts (1 injected failure + retry), got %d", failures)
	}
	if n := eng.Bus.CommitFailures(); n != 0 {
		t.Fatalf("busy retry must NOT spill; CommitFailures=%d", n)
	}
}

// TestBatchSpillOnPersistentCommitFailure proves a non-busy commit failure is
// never silently dropped: the batch is spilled to _spine_write_spill and the
// drainer replays it once the writer recovers.
func TestBatchSpillOnPersistentCommitFailure(t *testing.T) {
	eng := newDurabilityEngine(t)

	failed := false
	engine.SetCommitFailureHook(func() error {
		if !failed {
			failed = true
			// Deliberately NOT a lock/busy error: the string must not match
			// isLockBusyErr, or the batch would be retried instead of spilled.
			return errors.New("injected commit failure (non-transient)")
		}
		return nil
	})

	if _, err := eng.Bus.Emit("DUR_INSERT", map[string]interface{}{"msg": "spill-me"}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	// The drainer polls every 5s — give it generous headroom.
	waitForRow(t, eng, 15*time.Second, 1)

	if n := eng.Bus.CommitFailures(); n < 1 {
		t.Fatalf("expected >= 1 commit failure recorded, got %d", n)
	}
	if n := eng.Bus.SpillWrites(); n < 1 {
		t.Fatalf("expected >= 1 spilled write, got %d", n)
	}
}

// TestBatchShutdownDrainPersists proves the shutdown drain path still flushes
// pending batches: emit then immediately Close, then reopen the same database
// and verify every row landed.
func TestBatchShutdownDrainPersists(t *testing.T) {
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(spineFile, []byte(durabilityManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	dbPath := filepath.Join(dir, "spine.db")

	eng := newDurabilityEngineAt(t, spineFile, dbPath)
	for i := 0; i < 5; i++ {
		if _, err := eng.Bus.Emit("DUR_INSERT", map[string]interface{}{"msg": "shutdown"}); err != nil {
			t.Fatalf("emit %d failed: %v", i, err)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	// Close again must be safe (idempotent shutdown).
	if err := eng.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	// Reopen the same database and verify the shutdown drain persisted all 5.
	reopened := newDurabilityEngineAt(t, spineFile, dbPath)
	var n int
	if err := reopened.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "events_durability"`).Scan(&n); err != nil {
		t.Fatalf("reopened count: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 persisted rows after Close, got %d", n)
	}
}
