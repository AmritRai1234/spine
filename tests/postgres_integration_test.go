//go:build integration

package tests

// Postgres integration tests — verify the dialect-abstraction fixes end to end:
// idempotent inserts use ON CONFLICT DO NOTHING and every system-table
// statement uses $n placeholders (hardcoded "?" / INSERT OR IGNORE were
// syntax errors on PostgreSQL).
//
// Run against a real PostgreSQL server:
//
//	docker run -d --name spine-pg -e POSTGRES_PASSWORD=test -p 5432:5432 postgres:16
//	SPINE_TEST_PG_DSN=postgres://postgres:test@localhost:5432/postgres \
//	  go test -tags integration -run TestPG ./tests/
//
// Tests are skipped when SPINE_TEST_PG_DSN is not set.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

const pgManifest = `spine_version: 1

database:
  tables:
    - items

nodes:
  PGNode:
    emits:
      - event: PG_INSERT
        payload:
          name: string
          votes: integer
      - event: PG_UPSERT
        payload:
          sku: string
          qty: integer
      - event: PG_UPDATE
        payload:
          name: string
          votes: integer
      - event: PG_WEBHOOK
        payload:
          x: integer

routes:
  - on: PG_INSERT
    steps:
      - action: db.insert
        table: items

  - on: PG_UPSERT
    steps:
      - action: db.upsert
        table: items
        key: sku

  - on: PG_UPDATE
    steps:
      - action: db.update
        table: items
        where: "name = '$event.payload.name'"

  - on: PG_WEBHOOK
    steps:
      - action: http.post
        url: "http://127.0.0.1:1/dead"
`

func newPGEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dsn := os.Getenv("SPINE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("SPINE_TEST_PG_DSN not set; skipping Postgres integration tests")
	}
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(spineFile, []byte(pgManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, dsn)
	if err != nil {
		t.Fatalf("NewFromFile on Postgres DSN failed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func waitForPG(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func pgRowCount(t *testing.T, eng *spine.Engine, table string) int {
	t.Helper()
	var n int
	err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestPG_EmitPersistAndIdempotency verifies that emits persist through the
// batched writer AND that the idempotency claim protocol works on Postgres
// (ON CONFLICT DO NOTHING + $n placeholders).
func TestPG_EmitPersistAndIdempotency(t *testing.T) {
	eng := newPGEngine(t)

	// Unique per run: _spine_idem claims and rows persist in the shared test
	// database between runs.
	key := "pg-key-" + time.Now().Format("150405.000000000")
	name := "alpha-" + time.Now().Format("150405")

	res1, err := eng.Bus.Emit("PG_INSERT", map[string]interface{}{
		"name": name, "votes": 1, "_idempotency_key": key,
	})
	if err != nil {
		t.Fatalf("first emit failed: %v", err)
	}
	if res1["status"] != "ok" {
		t.Fatalf("first emit status: %v", res1["status"])
	}

	res2, err := eng.Bus.Emit("PG_INSERT", map[string]interface{}{
		"name": name, "votes": 1, "_idempotency_key": key,
	})
	if err != nil {
		t.Fatalf("duplicate emit failed: %v", err)
	}
	if res2["idempotent_hit"] != true {
		t.Fatalf("expected idempotent_hit on duplicate, got: %v", res2)
	}

	waitForPG(t, 5*time.Second, "batch flush", func() bool {
		var n int
		err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "items" WHERE "name" = $1`, name).Scan(&n)
		return err == nil && n >= 1
	})
	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "items" WHERE "name" = $1`, name).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row after idempotent duplicate, got %d", n)
	}
}

// TestPG_Upsert verifies ON CONFLICT upserts against Postgres.
func TestPG_Upsert(t *testing.T) {
	eng := newPGEngine(t)

	for _, qty := range []int{1, 2} {
		if _, err := eng.Bus.Emit("PG_UPSERT", map[string]interface{}{"sku": "sku-1", "qty": qty}); err != nil {
			t.Fatalf("upsert emit (qty=%d) failed: %v", qty, err)
		}
	}

	waitForPG(t, 5*time.Second, "upsert flush", func() bool {
		var n int
		err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "items" WHERE "sku" = $1`, "sku-1").Scan(&n)
		return err == nil && n >= 1
	})
	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM "items" WHERE "sku" = $1`, "sku-1").Scan(&n); err != nil {
		t.Fatalf("count sku-1 rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row for sku-1 after upsert x2, got %d", n)
	}

	var qty int
	if err := eng.Bus.DB().QueryRow(`SELECT "qty" FROM "items" WHERE "sku" = $1`, "sku-1").Scan(&qty); err != nil {
		t.Fatalf("read upserted row: %v", err)
	}
	if qty != 2 {
		t.Fatalf("expected qty=2 after second upsert, got %d", qty)
	}
}

// TestPG_UpdateWithWhere verifies parameterized db.update with an explicit
// where expression on Postgres.
func TestPG_UpdateWithWhere(t *testing.T) {
	eng := newPGEngine(t)

	if _, err := eng.Bus.Emit("PG_INSERT", map[string]interface{}{"name": "beta", "votes": 1}); err != nil {
		t.Fatalf("insert emit failed: %v", err)
	}
	waitForPG(t, 5*time.Second, "insert flush", func() bool {
		return pgRowCount(t, eng, "items") >= 1
	})

	if _, err := eng.Bus.Emit("PG_UPDATE", map[string]interface{}{"name": "beta", "votes": 99}); err != nil {
		t.Fatalf("update emit failed: %v", err)
	}
	waitForPG(t, 5*time.Second, "update flush", func() bool {
		var v int
		err := eng.Bus.DB().QueryRow(`SELECT "votes" FROM "items" WHERE "name" = $1`, "beta").Scan(&v)
		return err == nil && v == 99
	})
}

// TestPG_OutboxRetry verifies the durable outbox enqueue path works on
// Postgres: an http.post to an unreachable host must produce an outbox row.
func TestPG_OutboxRetry(t *testing.T) {
	eng := newPGEngine(t)

	if _, err := eng.Bus.Emit("PG_WEBHOOK", map[string]interface{}{"x": 1}); err == nil {
		t.Fatal("expected emit to fail (http.post to dead endpoint)")
	}

	waitForPG(t, 5*time.Second, "outbox row", func() bool {
		return pgRowCount(t, eng, "_spine_outbox") >= 1
	})
}

// TestPG_TableRead verifies the public read path (GetTableRows) against PG.
func TestPG_TableRead(t *testing.T) {
	eng := newPGEngine(t)

	if _, err := eng.Bus.Emit("PG_INSERT", map[string]interface{}{"name": "gamma", "votes": 3}); err != nil {
		t.Fatalf("insert emit failed: %v", err)
	}
	waitForPG(t, 5*time.Second, "gamma row", func() bool {
		rows, err := eng.Bus.GetTableRows("items", 50, 0)
		if err != nil {
			return false
		}
		for _, r := range rows {
			if r["name"] == "gamma" {
				return true
			}
		}
		return false
	})

	rows, err := eng.Bus.GetTableRows("items", 50, 0)
	if err != nil {
		t.Fatalf("GetTableRows failed: %v", err)
	}
	found := false
	for _, r := range rows {
		if r["name"] == "gamma" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a gamma row from GetTableRows")
	}
}
