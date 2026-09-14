package engine

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
)

// queryRunner is the minimal query surface needed for catalog introspection
// (satisfied by *sql.DB and *sql.Conn).
type queryRunner interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

// Schema-epoch-keyed column cache. Correctness contract:
//
// Every schema evolution in the engine funnels through ensureTable
// (db.insert/update/upsert/adjust, analytics schema, slots, EnsureTables),
// and ensureTable bumps bus.schemaEpochCtr BEFORE its effects become
// observable. tableColumns keys its cache on the epoch observed AT QUERY
// TIME, so a cached list can only be served while no DDL has happened
// since it was captured. A stale list — one missing a column that a
// concurrent write just added — is structurally impossible: the write
// bumps the epoch before committing, and any read after that commit sees
// the new epoch and misses the cache.
//
// This is the same guarantee the fresh-per-query version had, minus the
// per-query catalog round-trip (~9µs + 55 allocs on SQLite; materially
// more on PostgreSQL, where information_schema.columns is a real
// cross-backend catalog join).
type colCacheEntry struct {
	cols []string
}

func (b *Bus) cachedTableColumns(table string) ([]string, error) {
	epoch := atomic.LoadUint64(&b.schemaEpochCtr)
	key := table
	if cached, ok := b.colCache.Load(key); ok {
		if e := cached.(*colCacheEntry); e != nil && len(e.cols) > 0 {
			if cachedEpoch, ok := b.colCacheEpoch.Load(key); ok && cachedEpoch.(uint64) == epoch {
				return e.cols, nil
			}
		}
	}

	cols, err := tableColumnsForDialect(b.dialect, b.db, table)
	if err != nil {
		return nil, err
	}
	b.colCacheEpoch.Store(key, epoch)
	b.colCache.Store(key, &colCacheEntry{cols: cols})
	return cols, nil
}

// tableColumnsForDialect returns the table's column names using the
// dialect-appropriate catalog introspection. Two implementations, because
// the stale-projection fix is structural on BOTH backends:
//
//   - SQLite: PRAGMA table_info (in-process catalog read).
//   - PostgreSQL: information_schema.columns — PRAGMA is SQLite-only syntax
//     and a hard syntax error on pgx (SQLSTATE 42601). The same correctness
//     argument applies here: pgx stdlib runs with QueryExecModeExec
//     (unnamed statements), but the PG server plans each statement and a
//     plan for `SELECT *` compiled before a concurrent ALTER can project
//     the old column set — the same stale-projection window as SQLite's
//     per-connection plan cache. Explicit column lists close it on both.
//
// Callers should go through (Bus).tableColumns, which adds the
// epoch-keyed cache; this function always hits the live catalog.
//
// See .hermes/issues/flaky-table-scope-content-visibility.md for the full
// mechanism record.
func tableColumnsForDialect(d *dialect, db queryRunner, table string) ([]string, error) {
	if d.name == "pgx" {
		return pgTableColumns(db, table)
	}

	rows, err := db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return nil, fmt.Errorf("tableColumns %q: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("tableColumns %q: scan: %w", table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tableColumns %q: %w", table, err)
	}
	if len(cols) == 0 {
		// PRAGMA table_info returns zero rows for both empty-result and
		// missing-table cases; missing tables are created on first insert.
		return nil, fmt.Errorf("tableColumns %q: no columns (table does not exist yet?)", table)
	}
	return cols, nil
}

// pgTableColumns introspects column names on PostgreSQL via
// information_schema.columns, ordered by ordinal position so projections
// are deterministic.
func pgTableColumns(db queryRunner, table string) ([]string, error) {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("tableColumns %q (pg): %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("tableColumns %q (pg): scan: %w", table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tableColumns %q (pg): %w", table, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("tableColumns %q (pg): no columns (table does not exist yet?)", table)
	}
	return cols, nil
}

// tableColumns returns the table's column names via the bus's dialect,
// served from the schema-epoch cache (see colCacheEntry for the
// correctness contract).
//
// This is the load-bearing primitive for the engine's read paths: every
// SELECT builds an EXPLICIT column list from this instead of using SELECT *.
//
// Why this is correctness, not style (see
// .hermes/issues/flaky-table-scope-content-visibility.md): mattn/go-sqlite3
// caches compiled statements per connection. A pooled connection whose
// `SELECT *` plan was compiled before a lazy ALTER (first-insert
// ensureTable evolution) replays the STALE projection — rows returned
// without their payload columns. A query whose SQL TEXT contains the new
// column name can only have been compiled AFTER the ALTER that added it,
// so the stale-plan mechanism is structurally impossible for explicit
// lists. The epoch cache preserves that guarantee (an evolved schema
// always produces a cache miss and a fresh, post-DDL list) while removing
// the per-query catalog round-trip.
func (b *Bus) tableColumns(table string) ([]string, error) {
	return b.cachedTableColumns(table)
}

// quotedColumnList renders the columns as a quoted SQL projection list.
func quotedColumnList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
	}
	return strings.Join(quoted, ", ")
}
