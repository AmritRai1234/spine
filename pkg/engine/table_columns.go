package engine

import (
	"fmt"
	"strings"
)

// tableColumns returns the table's column names via PRAGMA table_info.
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
	rows, err := b.db.Query(`PRAGMA table_info("` + table + `")`)
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

// quotedColumnList renders the columns as a quoted SQL projection list.
func quotedColumnList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
	}
	return strings.Join(quoted, ", ")
}
