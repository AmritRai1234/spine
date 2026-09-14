package engine

import (
	"database/sql"
	"fmt"
	"strings"
)

// queryRunner is the minimal query surface needed for catalog introspection
// (satisfied by *sql.DB and *sql.Conn).
type queryRunner interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

// tableColumnsForDialect returns the table's column names using the
// dialect-appropriate catalog introspection. Two implementations, because
// the stale-projection fix is structural on BOTH backends:
//
//   - SQLite: PRAGMA table_info (in-process catalog read, ~11µs).
//   - PostgreSQL: information_schema.columns — PRAGMA is SQLite-only syntax
//     and a hard syntax error on pgx (SQLSTATE 42601). The same correctness
//     argument applies here: pgx stdlib runs with QueryExecModeExec
//     (unnamed statements), but the PG server plans each statement and a
//     plan for `SELECT *` compiled before a concurrent ALTER can project
//     the old column set — the same stale-projection window as SQLite's
//     per-connection plan cache. Explicit column lists close it on both.
//
// Why fresh-per-query and NOT cached: a cached list would itself go stale at
// exactly the wrong moment (the race this fixes), and an invalidation hook
// would have to be airtight against the same concurrent-DDL window. The
// per-query catalog read costs ~11µs (SQLite, measured: 45.5µs SELECT *
// vs 59µs pragma+select) — noise for an in-process catalog.
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

// tableColumns returns the table's column names via the bus's dialect.
//
// This is the load-bearing primitive for the engine's read paths: every
// SELECT builds an EXPLICIT column list from this instead of using SELECT *.
//
// Why this is correctness, not style (see
// .hermes/issues/flaky-table-scope-content-visibility.md): mattn/go-sqlite3
// caches compiled statements per connection. A pooled connection whose
// `SELECT *` plan was compiled before a lazy ALTER (first-insert
// ensureTable evolution) replays the STALE projection — rows returned
// without their payload columns — for up to ~30ms after the DDL commits.
// A query whose SQL TEXT contains the new column name can only have been
// compiled AFTER the ALTER that added it, so the stale-plan mechanism is
// structurally impossible for explicit lists. That is why the column list
// is resolved FRESH PER QUERY and deliberately NOT cached: a cached list
// would itself go stale at exactly the wrong moment (the race this fixes),
// and any invalidation hook would have to be airtight against the same
// concurrent-DDL window. The per-query PRAGMA costs ~11µs on this hardware
// (measured: 45.5µs SELECT * vs 59µs pragma+select) — noise for an
// in-process catalog read.
func (b *Bus) tableColumns(table string) ([]string, error) {
	return tableColumnsForDialect(b.dialect, b.db, table)
}

// quotedColumnList renders the columns as a quoted SQL projection list.
func quotedColumnList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
	}
	return strings.Join(quoted, ", ")
}
