package engine

import "strconv"

// dialect abstracts the SQL syntax differences between supported database
// backends (SQLite/libSQL/Turso vs PostgreSQL). A Bus binds exactly one
// dialect at construction time, so per-fingerprint SQL template caches stay
// valid for the lifetime of the process.
type dialect struct {
	name string
	// placeholder renders the bind marker for the n-th parameter (1-based):
	// SQLite uses anonymous "?", PostgreSQL uses ordinal "$1..$n".
	placeholder func(n int) string
	// autoIncPK is the auto-increment primary key clause appended after a
	// column name (e.g. "_spine_id <autoIncPK>" or "id <autoIncPK>").
	autoIncPK string
	// listTables lists user-defined tables in this backend's current schema.
	listTables string
	// rowIDCol names the implicit per-row identifier usable in SELECT lists
	// ("rowid" on SQLite/libSQL, the engine's surrogate PK on PostgreSQL).
	rowIDCol string
	// idemInsertPrefix opens an idempotent single-row insert. SQLite/Turso use
	// "INSERT OR IGNORE INTO"; PostgreSQL uses "INSERT INTO" plus
	// idemConflictSuffix (" ON CONFLICT (...) DO NOTHING") — "OR IGNORE" is
	// SQLite-only syntax and would be a syntax error on PG.
	idemInsertPrefix   string
	idemConflictSuffix string
}

var (
	sqliteDialect = dialect{
		name:               "sqlite3",
		placeholder:        func(int) string { return "?" },
		autoIncPK:          `INTEGER PRIMARY KEY AUTOINCREMENT`,
		listTables:         `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`,
		rowIDCol:           `rowid`,
		idemInsertPrefix:   `INSERT OR IGNORE INTO`,
		idemConflictSuffix: ``,
	}
	postgresDialect = dialect{
		name:               "pgx",
		placeholder:        func(n int) string { return "$" + strconv.Itoa(n) },
		autoIncPK:          `BIGSERIAL PRIMARY KEY`,
		listTables:         `SELECT table_name AS name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`,
		rowIDCol:           `_spine_id`,
		idemInsertPrefix:   `INSERT INTO`,
		idemConflictSuffix: ` ON CONFLICT ("key") DO NOTHING`,
	}
)

// ph returns the placeholder for parameter n via the bus's dialect.
func (b *Bus) ph(n int) string {
	return b.dialect.placeholder(n)
}
