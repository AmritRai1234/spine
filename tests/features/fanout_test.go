package features

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// db.fanout test suite. A minimal engine: FANOUT_TICK runs db.fanout over
// `subscriptions` and each due row emits DUE_FIRED, which inserts into
// fired_log so test assertions count emissions durably.

const fanoutManifest = `spine_version: 3
database:
  tables:
    - subscriptions
    - fired_log

nodes:
  - name: billing
    emits:
      - event: FANOUT_TICK
      - event: DUE_FIRED
      - event: SEED_SUB

routes:
  - on: FANOUT_TICK
    steps:
      - action: db.fanout
        table: subscriptions
        where: "next_charge_date <= $now"
        emit_event: DUE_FIRED
        due_column: next_charge_date
        interval_column: interval_months
%s

  - on: DUE_FIRED
    steps:
      - action: db.insert
        table: fired_log

  - on: SEED_SUB
    steps:
      - action: db.insert
        table: subscriptions
`

func newFanoutEngine(t *testing.T, dir string, extraRoutes string) *spine.Engine {
	t.Helper()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")
	if err := os.WriteFile(manifestPath, []byte(
		strings.Replace(fanoutManifest, "%s", extraRoutes, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	return eng
}

func seedSub(t *testing.T, eng *spine.Engine, id string, due string, interval int) {
	t.Helper()
	if _, err := eng.Bus.Emit("SEED_SUB", map[string]interface{}{
		"id":               id,
		"email":            id + "@example.com",
		"next_charge_date": due,
		"interval_months":  interval,
	}); err != nil {
		t.Fatalf("seed %s failed: %v", id, err)
	}
}

func runScan(t *testing.T, eng *spine.Engine) {
	t.Helper()
	if _, err := eng.Bus.Emit("FANOUT_TICK", map[string]interface{}{}); err != nil {
		t.Fatalf("fanout scan failed: %v", err)
	}
}

func countFired(t *testing.T, eng *spine.Engine) int {
	t.Helper()
	rows, err := eng.Bus.GetTableRows("fired_log", 10000, 0)
	if err != nil {
		t.Fatalf("GetTableRows(fired_log): %v", err)
	}
	return len(rows)
}

// flush waits for the async batch writer to persist pending inserts.
func flush() { time.Sleep(250 * time.Millisecond) }

// 1. Scan fires exactly the due rows and skips not-yet-due ones.
func TestFanoutFiresOnlyDueRows(t *testing.T) {
	eng := newFanoutEngine(t, t.TempDir(), "")
	defer eng.Close()

	now := time.Now().UTC()
	seedSub(t, eng, "due-1", now.Add(-48*time.Hour).Format(time.RFC3339), 1)
	seedSub(t, eng, "due-2", now.Add(-time.Hour).Format(time.RFC3339), 1)
	seedSub(t, eng, "future", now.Add(72*time.Hour).Format(time.RFC3339), 1)
	flush()

	runScan(t, eng)
	flush()

	if got := countFired(t, eng); got != 2 {
		t.Errorf("expected exactly 2 due rows fired (future skipped), got %d", got)
	}
}

// 2. Re-running the same scan produces zero duplicate emissions.
func TestFanoutRerunNoDuplicates(t *testing.T) {
	eng := newFanoutEngine(t, t.TempDir(), "")
	defer eng.Close()

	due := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	seedSub(t, eng, "sub-1", due, 1)
	flush()

	runScan(t, eng)
	flush()
	if got := countFired(t, eng); got != 1 {
		t.Fatalf("after scan 1 expected 1 fired row, got %d", got)
	}

	runScan(t, eng)
	flush()
	if got := countFired(t, eng); got != 1 {
		t.Errorf("re-run of same scan produced duplicates: got %d, want 1", got)
	}
}

// 3. Mid-batch crash simulation: a tiny batch size forces multiple pages;
// closing the engine mid-lifecycle ("crash") then reopening must resume
// without double-firing or losing rows.
func TestFanoutCrashMidBatchResumesWithoutLossOrDuplication(t *testing.T) {
	dir := t.TempDir()
	tinyBatch := `
    batch_size: 2`
	eng := newFanoutEngine(t, dir, tinyBatch)

	due := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	for i := 1; i <= 5; i++ {
		seedSub(t, eng, "s"+strconv.Itoa(i), due, 1)
	}
	flush()

	// Crash before any scan completes; durable _spine_idem survives reopen.
	eng.Close()

	eng2 := newFanoutEngine(t, dir, tinyBatch)
	defer eng2.Close()

	runScan(t, eng2)
	flush()
	if got := countFired2(t, eng2); got != 5 {
		t.Errorf("post-crash resume: expected all 5 rows fired exactly once, got %d", got)
	}

	// Replay on the reopened engine must also be a no-op.
	runScan(t, eng2)
	flush()
	if got := countFired2(t, eng2); got != 5 {
		t.Errorf("replay after crash-resume duplicated: got %d, want 5", got)
	}
}

// 4. Long-outage catch-up: a monthly subscription that missed several cycles
// fires ONCE per scan, never once-per-missed-interval, and its advanced due
// date excludes it from subsequent scans.
func TestFanoutLongOutageSingleFire(t *testing.T) {
	eng := newFanoutEngine(t, t.TempDir(), "")
	defer eng.Close()

	longPast := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339) // ~3 missed monthly cycles
	seedSub(t, eng, "lapsed", longPast, 1)
	flush()

	runScan(t, eng)
	flush()
	if got := countFired(t, eng); got != 1 {
		t.Errorf("long outage must collapse missed intervals into one fire, got %d", got)
	}

	runScan(t, eng)
	flush()
	if got := countFired(t, eng); got != 1 {
		t.Errorf("advanced due date must exclude row from later scans, got %d", got)
	}
}

// 5. Tier gating: spine_version 2 manifests reject db.fanout with the version
// unlock error.
func TestFanoutTierGating(t *testing.T) {
	dir := t.TempDir()
	m := strings.Replace(strings.Replace(fanoutManifest, "%s", "", 1),
		"spine_version: 3", "spine_version: 2", 1)
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "spine.db"))
	if err == nil {
		t.Fatal("expected engine init to fail for tier-2 manifest using db.fanout")
	}
	if !strings.Contains(err.Error(), "version") && !strings.Contains(err.Error(), "3") {
		t.Errorf("expected version-unlock error naming the required version, got: %v", err)
	}
}

func countFired2(t *testing.T, eng *spine.Engine) int {
	return countFired(t, eng)
}
