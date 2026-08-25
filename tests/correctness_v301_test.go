package tests

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Regression tests for v3.0.1 correctness fixes:
//   1. SetState deep-copies payloads (no shared mutable state)
//   2. WS reconnect replay is gap-free (GetEventsSince + has_more)
//   4. GetEventLogs surfaces DB errors instead of returning empty+nil
//   5. Parallel-route failures saga-compensate succeeded sibling steps

func newV301Bus(t *testing.T, extra string) (*engine.Bus, func()) {
	t.Helper()
	manifestContent := `
spine_version: 1

database:
  tables:
    - items
` + extra + `
routes:
  - on: REPLAY_EVT
    steps:
      - action: log.write
        message: "replay marker $event.payload.i"
`

	tmp, err := os.CreateTemp("", "v301_*.spine")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString(manifestContent)
	tmp.Close()

	schema, err := manifest.ParseManifest(tmp.Name())
	if err != nil {
		os.Remove(tmp.Name())
		t.Fatalf("ParseManifest failed: %v", err)
	}
	reg := manifest.NewRegistry(schema)
	dbPath := tmp.Name() + ".db"
	bus, err := engine.NewBus(reg, dbPath, engine.NewHub())
	if err != nil {
		os.Remove(tmp.Name())
		t.Fatalf("NewBus failed: %v", err)
	}
	cleanup := func() {
		bus.Close()
		os.Remove(tmp.Name())
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")
	}
	return bus, cleanup
}

// Fix 1/3: cached state must be an immutable snapshot — later mutations of the
// source payload map (by later steps or chained emissions) must not rewrite it.
func TestSetStateSnapshotIsolation(t *testing.T) {
	bus, cleanup := newV301Bus(t, "")
	defer cleanup()

	payload := map[string]interface{}{"status": "pending", "nested": map[string]interface{}{"v": 1}}
	bus.SetState("ORDER_STATE", payload)

	// Mutate the original AFTER caching — both top level and nested.
	payload["status"] = "cancelled"
	payload["nested"].(map[string]interface{})["v"] = 99
	delete(payload, "status")

	got, ok := bus.GetState("ORDER_STATE")
	if !ok {
		t.Fatal("state not found in cache")
	}
	if got["status"] != "pending" {
		t.Errorf("cached state was mutated retroactively: status = %v", got["status"])
	}
	if nested, _ := got["nested"].(map[string]interface{}); nested == nil || nested["v"] != 1 {
		t.Errorf("cached nested state was mutated retroactively: %+v", got["nested"])
	}

	// Mutating the returned snapshot must not corrupt the cache either.
	got["status"] = "hacked"
	again, _ := bus.GetState("ORDER_STATE")
	if again["status"] != "pending" {
		t.Errorf("cache corrupted via returned reference: %v", again["status"])
	}
}

// Fix 2: replay since last_seen_id must be gap-free and report truncation.
func TestGetEventsSinceGapFreeReplay(t *testing.T) {
	bus, cleanup := newV301Bus(t, "")
	defer cleanup()

	const total = 12
	for i := 0; i < total; i++ {
		if _, err := bus.Emit("REPLAY_EVT", map[string]interface{}{"i": i}); err != nil {
			t.Fatalf("emit %d failed: %v", i, err)
		}
	}

	// Audit writes go through the async batch writer — wait for the flush.
	deadline := time.Now().Add(5 * time.Second)
	all, hasMore, err := bus.GetEventsSince(0, 500)
	for len(all) < total && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		all, hasMore, err = bus.GetEventsSince(0, 500)
	}
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(all) != total {
		t.Fatalf("expected %d events, got %d", total, len(all))
	}
	if hasMore {
		t.Error("hasMore true when all events fit in one batch")
	}

	// Page in batches of 5 starting from id=0: results must be strictly
	// ascending and contiguous (no gaps, no duplicates).
	var seen []int64
	lastID := int64(0)
	for page := 0; ; page++ {
		batch, more, err := bus.GetEventsSince(lastID, 5)
		if err != nil {
			t.Fatalf("page %d failed: %v", page, err)
		}
		for _, ev := range batch {
			id, _ := ev["id"].(int64)
			if id <= lastID {
				t.Fatalf("non-ascending replay: id %d after %d", id, lastID)
			}
			if lastID > 0 && id != lastID+1 {
				t.Fatalf("gap in replay: jumped from %d to %d", lastID, id)
			}
			seen = append(seen, id)
			lastID = id
		}
		if !more {
			break
		}
		if len(batch) == 0 {
			t.Fatal("hasMore=true with empty batch — infinite loop guard")
		}
	}
	if len(seen) != total {
		t.Errorf("expected to page through %d events, saw %d (%v)", total, len(seen), seen)
	}

	// A client fully caught up gets nothing, no more flag.
	fresh, more, err := bus.GetEventsSince(lastID, 500)
	if err != nil || len(fresh) != 0 || more {
		t.Errorf("expected empty caught-up replay, got n=%d more=%v err=%v", len(fresh), more, err)
	}
}

// Fix 4: DB failures must surface as errors, never as "zero events".
func TestGetEventLogsSurfacesDBErrors(t *testing.T) {
	bus, cleanup := newV301Bus(t, "")
	cleanup() // closes the underlying DB immediately

	if _, err := bus.GetEventLogs("", 10, 0); err == nil {
		t.Error("GetEventLogs returned nil error on closed DB — outage would masquerade as empty log")
	}
	if _, _, err := bus.GetEventsSince(0, 10); err == nil {
		t.Error("GetEventsSince returned nil error on closed DB")
	}
}

// Fix 5: when any step of a parallel route fails, every sibling that already
// succeeded must be saga-compensated (same guarantee as sequential routes).
func TestParallelSagaCompensation(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	bus, cleanup := newV301Bus(t, `
routes:
  - on: PARALLEL_BOOK
    parallel: true
    steps:
      - action: track.charge
        compensate: track.refund
      - action: track.reserve
        compensate: track.release
      - action: track.explode
`)
	defer cleanup()

	mustRecord := func(name string) engine.ActionFunc {
		return func(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
			mu.Lock()
			calls = append(calls, name)
			mu.Unlock()
			return nil
		}
	}
	bus.RegisterAction("track.charge", mustRecord("charge"))
	bus.RegisterAction("track.refund", mustRecord("refund"))
	bus.RegisterAction("track.reserve", mustRecord("reserve"))
	bus.RegisterAction("track.release", mustRecord("release"))
	bus.RegisterAction("track.explode", func(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
		mu.Lock()
		calls = append(calls, "explode")
		mu.Unlock()
		return errors.New("inventory gone")
	})

	_, err := bus.Emit("PARALLEL_BOOK", map[string]interface{}{"order_id": "o-1"})
	if err == nil {
		t.Fatal("expected emit to fail when a parallel step fails")
	}

	mu.Lock()
	defer mu.Unlock()
	refunds, releases := 0, 0
	for _, c := range calls {
		switch c {
		case "refund":
			refunds++
		case "release":
			releases++
		}
	}
	// Both siblings ran before/alongside the failing step; each must be
	// compensated exactly once regardless of goroutine completion order.
	if refunds != 1 || releases != 1 {
		t.Errorf("parallel saga compensation incomplete: calls=%v (refunds=%d releases=%d)", calls, refunds, releases)
	}
	fmt.Println("call order:", calls)
}
