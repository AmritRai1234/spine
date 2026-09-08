package e2e

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// requireFTS5 skips a test when the linked go-sqlite3 driver was built
// without the sqlite_fts5 tag (default `go test ./...`). Routes containing
// fts.search cannot provision the FTS5 virtual table without it and fail
// with 400 — that is an environment gap, not a regression. A bare test run
// must stay a trustworthy green signal: skip with the reason, don't fail.
func requireFTS5(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Skipf("cannot open in-memory sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE fts5_probe USING fts5(x)`); err != nil {
		t.Skip("FTS5 not available: rebuild tests with -tags sqlite_fts5 (see Makefile)")
	}
}
