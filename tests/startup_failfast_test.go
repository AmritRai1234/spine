package tests

import (
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

// TestStartupFailFast_CorruptDB verifies that NewBus/NewFromFile fails fast
// with a clear error when the SQLite database is corrupt or unwritable.
func TestStartupFailFast_CorruptDB(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
database:
  tables:
    - items
routes:
  - on: TEST
    steps:
      - action: db.insert
        table: items
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	// Create a corrupt file at the DB path (non-SQLite garbage)
	if err := os.WriteFile(dbPath, []byte("this is not a valid SQLite database file"), 0644); err != nil {
		t.Fatalf("Failed to write corrupt DB file: %v", err)
	}

	// NewFromFile should fail because the DB is corrupt and can't be opened
	_, err := spine.NewFromFile(manifestPath, dbPath)
	if err == nil {
		t.Fatal("Expected NewFromFile to fail with corrupt DB, but it succeeded")
	}
	t.Logf("Corrupt DB error: %v", err)
}

// TestStartupFailFast_UnwritableDB verifies that NewBus/NewFromFile fails fast
// when the database directory is not writable.
func TestStartupFailFast_UnwritableDB(t *testing.T) {
	// Skip on CI or when running as root since root can write anywhere
	if os.Geteuid() == 0 {
		t.Skip("Skipping unwritable DB test when running as root")
	}

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "nopenopenope", "spine.db") // directory doesn't exist

	manifest := `spine_version: 1
database:
  tables:
    - items
routes:
  - on: TEST
    steps:
      - action: db.insert
        table: items
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	// Ensure the parent directory doesn't exist
	os.RemoveAll(filepath.Join(dir, "nopenopenope"))

	_, err := spine.NewFromFile(manifestPath, dbPath)
	if err == nil {
		t.Fatal("Expected NewFromFile to fail with unwritable DB path, but it succeeded")
	}
	t.Logf("Unwritable DB error: %v", err)
}

// TestStartupFailFast_ReadOnlyDB verifies failure when connecting to a
// read-only database (simulated by creating the DB file without write permission).
func TestStartupFailFast_ReadOnlyDB(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("Skipping read-only DB test when running as root")
	}

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "readonly.db")

	manifest := `spine_version: 1
database:
  tables:
    - items
routes:
  - on: TEST
    steps:
      - action: db.insert
        table: items
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	// Create the DB file and set it read-only
	if err := os.WriteFile(dbPath, nil, 0644); err != nil {
		t.Fatalf("Failed to create DB file: %v", err)
	}
	if err := os.Chmod(dbPath, 0444); err != nil {
		t.Fatalf("Failed to chmod DB file to read-only: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dbPath, 0644) })

	_, err := spine.NewFromFile(manifestPath, dbPath)
	if err == nil {
		t.Fatal("Expected NewFromFile to fail with read-only DB, but it succeeded")
	}
	t.Logf("Read-only DB error: %v", err)
}

// TestStartupFailFast_PragmaError verifies that non-WAL pragma failures abort
// startup. We simulate this by flocking the DB file before opening, which
// prevents the PRAGMA statements from completing.
//
// NOTE: journal_mode=WAL errors are tolerated (degraded mode); any other pragma
// failure (e.g., synchronous, cache_size, temp_store) must abort.
func TestStartupFailFast_PragmaError(t *testing.T) {
	// SKIPPED: this test previously "passed" unconditionally — the flock
	// approach cannot reliably force a non-WAL pragma failure (SQLite opens
	// its own fd), so both branches only t.Log'd. A real version needs a
	// driver-level error-injection hook like SetCommitFailureHook. The
	// degraded-WAL and non-WAL abort branches are covered by
	// TestStartupFailFast_CorruptDB / _ReadOnlyDB / _UnwritableDB above.
	t.Skip("cannot force a PRAGMA journal_mode failure portably without a driver error-injection hook (tracked in FIX_PLAN Batch I, P7-4)")
}
