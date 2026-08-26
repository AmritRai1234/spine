package engine

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSplitStatements: migrations are split on top-level semicolons only —
// semicolons inside quoted regions must survive.
func TestSplitStatements(t *testing.T) {
	sql := `CREATE TABLE "a" (x TEXT); CREATE TABLE "b" (y TEXT DEFAULT ';');`
	parts := splitStatements(sql)
	if len(parts) != 3 { // two statements + trailing empty
		t.Fatalf("expected 3 parts, got %d: %q", len(parts), parts)
	}
	if !strings.Contains(parts[0], "CREATE TABLE \"a\"") || !strings.Contains(parts[1], "DEFAULT ';'") {
		t.Errorf("quoted semicolon was split: %q", parts)
	}
}

// TestApplyMigrationMultiStatementAndIdempotent: multi-statement DDL applies
// atomically, a second call is a no-op, and both tables exist.
func TestApplyMigrationMultiStatementAndIdempotent(t *testing.T) {
	bus := newTestBus(t)
	defer bus.Close()

	m := Migration{Version: 1, Name: "multi", SQL: `CREATE TABLE IF NOT EXISTS "m1" (x TEXT); CREATE TABLE IF NOT EXISTS "m2" (y TEXT);`}
	applied, err := bus.ApplyMigration(m)
	if err != nil || !applied {
		t.Fatalf("first apply: applied=%v err=%v", applied, err)
	}
	applied2, err := bus.ApplyMigration(m)
	if err != nil || applied2 {
		t.Fatalf("second apply must be a no-op: applied=%v err=%v", applied2, err)
	}
	for _, table := range []string{"m1", "m2"} {
		var n int
		if err := bus.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)).Scan(&n); err != nil {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
}

// TestLocalPubSubSafeDelivery: handlers get deep copies, a panicking handler
// is recovered, and delivery is synchronous.
func TestLocalPubSubSafeDelivery(t *testing.T) {
	ps := NewLocalPubSub()
	var got map[string]interface{}
	if err := ps.Subscribe("ch", func(p map[string]interface{}) {
		got = p
		p["mutated"] = true // must not affect the caller's payload
		panic("subscriber boom") // must not kill the process
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payload := map[string]interface{}{"a": 1}
	if err := ps.Publish("ch", payload); err != nil {
		t.Fatalf("publish with panicking subscriber must recover and return nil, got: %v", err)
	}
	if _, mutated := payload["mutated"]; mutated {
		t.Error("subscriber mutation leaked into the caller's payload (shared map)")
	}
	if got == nil || got["a"] != 1 {
		t.Errorf("subscriber did not receive the payload: %v", got)
	}
}

// TestHubCloseStopsLoopsAndCountsDrops: after Close, BroadcastState must not
// panic (drop path) and the drop is counted — Engine.New/Close cycles must
// not leak hub goroutines.
func TestHubCloseStopsLoopsAndCountsDrops(t *testing.T) {
	h := NewHub()
	go h.Run()

	h.BroadcastState("s1", "e1", map[string]interface{}{"a": 1})
	h.Close()

	// Post-close broadcasts are dropped and counted, never panicking.
	deadline := time.Now().Add(3 * time.Second)
	for h.DroppedBroadcasts() == 0 && time.Now().Before(deadline) {
		h.BroadcastState("s2", "e2", map[string]interface{}{"b": 2})
		time.Sleep(20 * time.Millisecond)
	}
	if h.DroppedBroadcasts() == 0 {
		t.Error("expected post-close broadcasts to be counted as dropped")
	}
}

// TestDrainSpillDeletesOnlyAfterCommit: the spill drainer replays durable rows
// through the writer and deletes them only after the flush fence commits them.
func TestDrainSpillDeletesOnlyAfterCommit(t *testing.T) {
	bus := newTestBus(t)
	defer bus.Close()

	// Create the target table via a sync insert (also proves the writer works).
	if _, err := bus.Emit("SEED_ITEMS", map[string]interface{}{"note": "seed"}); err != nil {
		t.Fatalf("seed emit failed: %v", err)
	}
	waitForRows := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&n)
			if n == want {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		var n int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&n)
		t.Fatalf("items did not reach %d rows (got %d)", want, n)
	}
	waitForRows(1)

	// Manually plant two durable spill rows referencing valid writes.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, note := range []string{"spill-a", "spill-b"} {
		paramsJSON, _ := json.Marshal([]interface{}{note})
		if _, err := bus.DB().Exec(`INSERT INTO "_spine_write_spill" (query, params_json, status, created_at) VALUES (?, ?, 'pending', ?)`,
			`INSERT INTO "items" ("note") VALUES (?)`, string(paramsJSON), now); err != nil {
			t.Fatalf("plant spill row: %v", err)
		}
	}

	bus.drainSpill()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "_spine_write_spill" WHERE status = 'pending'`).Scan(&pending)
		var rows int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&rows)
		if pending == 0 && rows == 3 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	var pending, rows int
	_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "_spine_write_spill" WHERE status = 'pending'`).Scan(&pending)
	_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM "items"`).Scan(&rows)
	t.Fatalf("spill drain failed: pending=%d items=%d (want 0/3)", pending, rows)
}

// TestUnauthenticatedWSDoesNotLeakWriterGoroutine: an unauthenticated WS
// client's writer goroutine must exit when the connection closes (auth
// timeout), not linger ~25s until a ping write fails.
func TestUnauthenticatedWSDoesNotLeakWriterGoroutine(t *testing.T) {
	eng, err := NewFromFile(newTempManifest(t), filepath.Join(t.TempDir(), "leak.db"))
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	defer eng.Close()
	eng.wsAuthTimeout = 300 * time.Millisecond

	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	before := runtime.NumGoroutine()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Read until the server closes us (auth timeout).
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	conn.Close()

	// The writer goroutine must exit promptly; without the fix it lingers
	// until a ping write fails (~30s).
	deadline := time.Now().Add(6 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines did not return to baseline: before=%d after=%d (writer goroutine leak)", before, n)
	}
}

// newTestBus builds a Bus from a minimal manifest with an items table.
func newTestBus(t *testing.T) *Bus {
	t.Helper()
	eng, err := NewFromFile(newTempManifest(t), filepath.Join(t.TempDir(), "bus.db"))
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng.Bus
}

func newTempManifest(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.spine")
	content := `spine_version: 1
database:
  tables:
    - items
nodes:
  N:
    emits:
      - event: SEED_ITEMS
        payload:
          note: string
routes:
  - on: SEED_ITEMS
    steps:
      - action: db.insert
        table: items
        sync: "true"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// BenchmarkBatchedDurableWrites measures the DURABLE write path that the
// enqueue benchmarks exclude: N emits through the batch writer, then a flush
// fence. With real batching (P3-11) the writer commits one transaction per
// 250–10,000 writes instead of one per write.
func BenchmarkBatchedDurableWrites(b *testing.B) {
	eng, err := NewFromFile(newTempManifest(b), filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("NewFromFile: %v", err)
	}
	defer eng.Close()
	bus := eng.Bus

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := bus.Emit("SEED_ITEMS", map[string]interface{}{"note": "x"}); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
	if !bus.writer.flushAndWait(writerFlushTimeout) {
		b.Fatal("flush fence timed out")
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "durable_writes/s")
	b.ReportMetric(elapsed.Seconds()*1e9/float64(b.N), "ns/durable_write")
}
