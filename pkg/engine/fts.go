package engine

import (
	"database/sql"
	"fmt"
	"strings"
)

// ftsTableName returns the FTS5 virtual table name for a user table.
// Tables already suffixed "_fts" are used as-is (declared in the manifest).
func ftsTableName(table string) string {
	if strings.HasSuffix(table, "_fts") {
		return table
	}
	return table + "_fts"
}

// ensureFTS provisions the FTS5 virtual table and synchronization triggers for
// `table`, backfilling any rows that already exist. It is idempotent (cached
// per table in knownTable) and SQLite/Turso-only: on other backends it returns
// an explicit error so fts.search fails loudly instead of silently returning
// empty results.
//
// The index is a plain (non-external-content) FTS5 table over the source
// table's text columns, kept in sync by AFTER INSERT/UPDATE/DELETE triggers.
func (b *Bus) ensureFTS(table string) error {
	if b.dialect.name != "sqlite3" {
		return fmt.Errorf("fts.search is only supported on SQLite/Turso backends (current dialect: %q)", b.dialect.name)
	}

	cacheKey := "fts|" + table
	if _, ok := b.knownTable.Load(cacheKey); ok {
		return nil
	}

	// Introspect the source table to find indexable text columns.
	rows, err := b.db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return fmt.Errorf("fts.search: cannot introspect table %q: %w", table, err)
	}

	type colInfo struct{ name, ctype string }
	var cols []colInfo
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("fts.search: cannot read schema of table %q: %w", table, err)
		}
		cols = append(cols, colInfo{name: name, ctype: ctype})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("fts.search: reading schema of table %q failed: %w", table, err)
	}

	var textCols []string
	for _, c := range cols {
		if c.name == "_spine_id" {
			continue
		}
		t := strings.ToUpper(c.ctype)
		if strings.Contains(t, "TEXT") || strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || t == "" {
			textCols = append(textCols, `"`+c.name+`"`)
		}
	}
	if len(textCols) == 0 {
		// PRAGMA table_info returns zero rows both for empty tables and for
		// tables that do not exist yet (they are created on first insert).
		return fmt.Errorf("fts.search: table %q has no text columns to index (does it exist yet? tables are created on first insert)", table)
	}

	ftsTable := ftsTableName(table)
	colList := strings.Join(textCols, ", ")

	createSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS "%s" USING fts5(%s)`, ftsTable, colList)
	if _, err := b.db.Exec(createSQL); err != nil {
		// Common failure: the sqlite_fts5 build tag is not enabled on the
		// go-sqlite3 driver ("no such module: fts5"). Surface that hint.
		return fmt.Errorf("fts.search: cannot create FTS index for %q (build with -tags sqlite_fts5?): %w", table, err)
	}

	// Keep the index in sync with the content table.
	newRefs := make([]string, len(textCols))
	for i, c := range textCols {
		newRefs[i] = "new." + c
	}
	triggers := []string{
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS "%s_ai" AFTER INSERT ON "%s" BEGIN
			INSERT INTO "%s"(rowid, %s) VALUES (new._spine_id, %s); END`,
			ftsTable, table, ftsTable, colList, strings.Join(newRefs, ", ")),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS "%s_ad" AFTER DELETE ON "%s" BEGIN
			DELETE FROM "%s" WHERE rowid = old._spine_id; END`,
			ftsTable, table, ftsTable),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS "%s_au" AFTER UPDATE ON "%s" BEGIN
			DELETE FROM "%s" WHERE rowid = old._spine_id;
			INSERT INTO "%s"(rowid, %s) VALUES (new._spine_id, %s); END`,
			ftsTable, table, ftsTable, ftsTable, colList, strings.Join(newRefs, ", ")),
	}
	for _, trig := range triggers {
		if _, err := b.db.Exec(trig); err != nil {
			return fmt.Errorf("fts.search: cannot create sync trigger for %q: %w", table, err)
		}
	}

	// Backfill rows that existed before the index was created.
	backfillSQL := fmt.Sprintf(`INSERT INTO "%s"(rowid, %s) SELECT _spine_id, %s FROM "%s"`,
		ftsTable, colList, colList, table)
	if _, err := b.db.Exec(backfillSQL); err != nil {
		return fmt.Errorf("fts.search: backfilling index for %q failed: %w", table, err)
	}

	b.knownTable.Store(cacheKey, true)
	return nil
}
