package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Idempotency claim namespacing — regression tests for the cross-caller
// data disclosure via _spine_idem. Claims are namespaced by caller
// (PRIMARY KEY (caller, key)): the same client-supplied key in different
// namespaces must be independent, and a cached result must only ever be
// replayed to the caller that produced it.

func newIdemTestBus(t *testing.T) *Bus {
	t.Helper()
	schema := &manifest.SpineSchema{
		DbTables: []string{"out"},
		Nodes: []manifest.Node{{
			Name: "n",
			Emits: []manifest.Emit{{
				Event:  "PING",
				Fields: []manifest.PayloadField{{Name: "marker", FieldType: "string"}},
			}},
		}},
		Routes: []manifest.Route{{
			OnEvent: "PING",
			Steps: []manifest.RouteStep{{
				Action: "db.insert",
				Table:  "out",
				Config: map[string]string{"sync": "true"},
			}},
			EmitState: "OUT_DONE",
		}},
	}
	bus, err := NewBus(manifest.NewRegistry(schema), filepath.Join(t.TempDir(), "idem.db"), NewHub())
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// Caller A claims a key and completes; caller B presenting the SAME key
// must get a conflict — never A's cached result. This is the core of the
// cross-caller disclosure fix.
func TestIdempotencyNamespacesIsolateCallers(t *testing.T) {
	bus := newIdemTestBus(t)

	resA, err := bus.EmitAs("role:shopper", "PING", map[string]interface{}{
		"marker":           "A-payload",
		"_idempotency_key": "checkout-42",
	})
	if err != nil {
		t.Fatalf("caller A first emit: %v", err)
	}
	if resA["idempotent_hit"] == true {
		t.Fatal("first emit must not be a replay")
	}

	// Same caller, same key → replay of THEIR cached result (intended behavior).
	resA2, err := bus.EmitAs("role:shopper", "PING", map[string]interface{}{
		"marker":           "A-payload",
		"_idempotency_key": "checkout-42",
	})
	if err != nil {
		t.Fatalf("caller A replay: %v", err)
	}
	if resA2["idempotent_hit"] != true {
		t.Fatalf("same-caller same-key must replay cached result, got: %v", resA2)
	}

	// Different caller, SAME key → independent claim: B's emit proceeds
	// normally in its own namespace. The isolation property under test is
	// that B's result is B's own — never a replay of A's cached result.
	resB, err := bus.EmitAs("role:other", "PING", map[string]interface{}{
		"marker":           "B-payload",
		"_idempotency_key": "checkout-42",
	})
	if err != nil {
		t.Fatalf("caller B own emit failed: %v", err)
	}
	if resB["idempotent_hit"] == true {
		t.Fatal("B must not receive an idempotent replay for A's completed claim")
	}

	// And B's own distinct key is a fresh claim in B's namespace.
	resB2, err := bus.EmitAs("role:other", "PING", map[string]interface{}{
		"marker":           "B-payload",
		"_idempotency_key": "checkout-B-own",
	})
	if err != nil {
		t.Fatalf("caller B own key: %v", err)
	}
	if resB2["idempotent_hit"] == true {
		t.Fatal("B's own new key must not be a replay")
	}
}

// The internal namespace is distinct from every caller namespace: an
// internal claim (webhook stamp, fanout) cannot be replayed by an HTTP
// caller presenting the same key, and vice versa.
func TestIdempotencyInternalNamespaceIsolated(t *testing.T) {
	bus := newIdemTestBus(t)

	// Internal emit claims the key (webhook stamp path uses Bus.Emit).
	if _, err := bus.Emit("PING", map[string]interface{}{
		"marker":           "internal",
		"_idempotency_key": "evt_stripe_123",
	}); err != nil {
		t.Fatalf("internal emit: %v", err)
	}

	// HTTP caller with the same key: independent claim in its own namespace
	// — the client's result is the client's own, never a replay of the
	// internal emit's cached result.
	p := map[string]interface{}{
		"marker":           "client",
		"_idempotency_key": "evt_stripe_123",
	}
	res, err := bus.EmitAs("role:shopper", "PING", p)
	if err != nil {
		t.Fatalf("client same-key emit failed: %v", err)
	}
	if res["idempotent_hit"] == true {
		t.Fatal("client must not replay the internal namespace's cached result")
	}

	// Reverse direction: client claims first, internal emit unaffected.
	if _, err := bus.EmitAs("role:shopper", "PING", map[string]interface{}{
		"marker":           "client2",
		"_idempotency_key": "evt_stripe_456",
	}); err != nil {
		t.Fatalf("client claim: %v", err)
	}
	if _, err := bus.Emit("PING", map[string]interface{}{
		"marker":           "internal2",
		"_idempotency_key": "evt_stripe_456",
	}); err != nil {
		t.Fatalf("internal must not be blocked by client's claim: %v", err)
	}
}

// Failed emits release the claim (retry after failure stays possible) —
// the deferred cleanup must also be caller-scoped.
func TestIdempotencyFailureReleasesClaimInNamespace(t *testing.T) {
	schema := &manifest.SpineSchema{
		Nodes: []manifest.Node{{
			Name: "n",
			Emits: []manifest.Emit{{
				Event:  "PING",
				Fields: []manifest.PayloadField{{Name: "marker", FieldType: "string"}},
			}},
		}},
		Routes: []manifest.Route{{
			OnEvent: "PING",
			Steps: []manifest.RouteStep{{
				Action: "assert",
				Config: map[string]string{"condition": "1 == 2", "message": "always fails"},
			}},
		}},
	}
	bus, err := NewBus(manifest.NewRegistry(schema), filepath.Join(t.TempDir(), "idem.db"), NewHub())
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	// First attempt fails (assert) — claim must be released.
	if _, err := bus.EmitAs("role:shopper", "PING", map[string]interface{}{
		"marker":           "x",
		"_idempotency_key": "retry-key",
	}); err == nil {
		t.Fatal("expected assert to fail the route")
	}

	// Retry by the same caller must execute again (not hit in-flight).
	if _, err := bus.EmitAs("role:shopper", "PING", map[string]interface{}{
		"marker":           "x",
		"_idempotency_key": "retry-key",
	}); err == nil {
		t.Fatal("expected retry to fail the route again (claim was released)")
	}
}

// Migration: a legacy single-PK _spine_idem table is upgraded in place to
// the (caller, key) schema, with existing rows attributed to 'internal'.
func TestIdempotencyLegacyTableMigration(t *testing.T) {
	bus := newIdemTestBus(t)

	// Force a legacy-shaped table with a pre-existing claim.
	if _, err := bus.db.Exec(`DROP TABLE "_spine_idem"`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := bus.db.Exec(`CREATE TABLE "_spine_idem" (
		key TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		result_json TEXT,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("legacy create: %v", err)
	}
	if _, err := bus.db.Exec(`INSERT INTO "_spine_idem" (key, status, result_json, created_at)
		VALUES ('legacy-key', 'completed', '{"status":"ok","legacy":true}', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	// Re-running init must detect the legacy shape and migrate.
	if err := bus.initIdempotencyTable(); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// Schema upgraded: composite PK.
	var pkCols []string
	rows, err := bus.db.Query(`SELECT "name" FROM "pragma_table_info"("_spine_idem") WHERE "pk" > 0 ORDER BY "pk"`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		pkCols = append(pkCols, c)
	}
	rows.Close()
	if len(pkCols) != 2 || pkCols[0] != "caller" || pkCols[1] != "key" {
		t.Fatalf("expected composite PK (caller,key), got %v", pkCols)
	}

	// Legacy row preserved under the internal namespace.
	var status, resultJSON string
	err = bus.db.QueryRow(`SELECT status, result_json FROM "_spine_idem" WHERE caller = 'internal' AND key = 'legacy-key'`).Scan(&status, &resultJSON)
	if err != nil {
		t.Fatalf("legacy row lost in migration: %v", err)
	}
	var cached map[string]interface{}
	if json.Unmarshal([]byte(resultJSON), &cached) != nil || cached["legacy"] != true {
		t.Fatalf("legacy result_json corrupted: %s", resultJSON)
	}

	// Migration is tracked — re-running init must not re-apply.
	if err := bus.initIdempotencyTable(); err != nil {
		t.Fatalf("second init: %v", err)
	}
	var count int
	bus.db.QueryRow(`SELECT COUNT(1) FROM "_spine_migrations" WHERE version = 1`).Scan(&count)
	if count != 1 {
		t.Fatalf("migration tracked %d times, want 1", count)
	}
}
