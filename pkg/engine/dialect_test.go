package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// firstCached returns the single cached template from a sync.Map of
// fingerprint → *sqlTemplate (tests insert exactly one shape per cache).
func firstCached(t *testing.T, m *sync.Map) string {
	t.Helper()
	var found string
	m.Range(func(_, v interface{}) bool {
		found = v.(*sqlTemplate).sql
		return false // stop at first
	})
	if found == "" {
		t.Fatal("expected a cached SQL template, cache was empty")
	}
	return found
}

func clearTemplateCaches(b *Bus) {
	b.insertSQLCache = sync.Map{}
	b.updateSQLCache = sync.Map{}
	b.upsertSQLCache = sync.Map{}
	b.knownTable = sync.Map{} // force ensureTable to re-run for the new dialect
}

func newDialectTestBus(t *testing.T) *Bus {
	t.Helper()
	reg := manifest.NewRegistry(&manifest.SpineSchema{
		Nodes: []manifest.Node{{
			Name: "N",
			Emits: []manifest.Emit{{
				Event: "EV",
				Fields: []manifest.PayloadField{
					{Name: "title", FieldType: "string"},
					{Name: "votes", FieldType: "int"},
				},
			}},
		}},
	})
	bus, err := NewBus(reg, filepath.Join(t.TempDir(), "dialect.db"), nil)
	if err != nil {
		t.Fatalf("NewBus failed: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// exerciseAllDBActions drives every write-path builder so each template lands
// in its cache (or is returned directly for delete/sum).
func exerciseAllDBActions(b *Bus) (deleteSQL, sumSQL string, err error) {
	if e := b.dbInsert("items", "EV", map[string]interface{}{"title": "x", "votes": 1}); e != nil {
		return "", "", e
	}
	if e := b.dbUpdate("items", "title = 'x'", "EV", map[string]interface{}{"votes": 2, "title": "x"}); e != nil {
		return "", "", e
	}
	if e := b.dbUpsert("items", "title", "EV", map[string]interface{}{"title": "x", "votes": 3}); e != nil {
		return "", "", e
	}
	deleteSQL = `DELETE FROM "items" WHERE "id" = ` + b.ph(1)
	sumSQL = `SELECT COALESCE(SUM("votes"), 0) FROM "items"`
	return deleteSQL, sumSQL, nil
}

func TestDialectPrimitives(t *testing.T) {
	if got := sqliteDialect.placeholder(1); got != "?" {
		t.Errorf("sqlite placeholder(1) = %q, want \"?\"", got)
	}
	if got := sqliteDialect.placeholder(7); got != "?" {
		t.Errorf("sqlite placeholder(7) = %q, want \"?\" (anonymous)", got)
	}
	for i, want := range []string{"$1", "$2", "$3", "$4"} {
		if got := postgresDialect.placeholder(i + 1); got != want {
			t.Errorf("postgres placeholder(%d) = %q, want %q", i+1, got, want)
		}
	}
	if !strings.Contains(sqliteDialect.autoIncPK, "AUTOINCREMENT") {
		t.Errorf("sqlite autoIncPK missing AUTOINCREMENT: %q", sqliteDialect.autoIncPK)
	}
	if !strings.Contains(postgresDialect.autoIncPK, "BIGSERIAL") {
		t.Errorf("postgres autoIncPK missing BIGSERIAL: %q", postgresDialect.autoIncPK)
	}
	if strings.Contains(postgresDialect.listTables, "sqlite_master") {
		t.Error("postgres listTables must not reference sqlite_master")
	}
}

func TestSQLiteTemplatesUseAnonymousPlaceholders(t *testing.T) {
	bus := newDialectTestBus(t)
	delSQL, _, err := exerciseAllDBActions(bus)
	if err != nil {
		t.Fatalf("actions failed: %v", err)
	}

	insertSQL := firstCached(t, &bus.insertSQLCache)
	updateSQL := firstCached(t, &bus.updateSQLCache)
	upsertSQL := firstCached(t, &bus.upsertSQLCache)

	for name, sqlText := range map[string]string{
		"insert": insertSQL, "update": updateSQL, "upsert": upsertSQL,
		"delete": delSQL,
	} {
		if strings.Contains(sqlText, "$1") || strings.Contains(sqlText, "$2") {
			t.Errorf("%s: sqlite template must not contain ordinal placeholders:\n%s", name, sqlText)
		}
		if !strings.Contains(sqlText, "?") && name != "" {
			t.Errorf("%s: sqlite template missing ? placeholders:\n%s", name, sqlText)
		}
	}
	if !strings.Contains(insertSQL, `"title"`) || !strings.Contains(insertSQL, `"votes"`) {
		t.Errorf("insert: manifest columns missing:\n%s", insertSQL)
	}
	if !strings.Contains(upsertSQL, `ON CONFLICT("title") DO UPDATE SET`) {
		t.Errorf("upsert: conflict clause missing:\n%s", upsertSQL)
	}
}

func TestPostgresTemplatesUseOrdinalPlaceholders(t *testing.T) {
	bus := newDialectTestBus(t)
	// First run under SQLite so ensureTable materializes real tables.
	if _, _, err := exerciseAllDBActions(bus); err != nil {
		t.Fatalf("warmup actions failed: %v", err)
	}
	// Swap dialect and clear caches — knownTable is cleared too, so
	// ensureTable re-runs and emits Postgres DDL against the live SQLite DB
	// (valid no-op DDL thanks to IF NOT EXISTS semantics).
	bus.dialect = &postgresDialect
	clearTemplateCaches(bus)

	delSQL, sumSQL, err := exerciseAllDBActions(bus)
	if err != nil {
		t.Fatalf("actions failed: %v", err)
	}

	insertSQL := firstCached(t, &bus.insertSQLCache)
	updateSQL := firstCached(t, &bus.updateSQLCache)
	upsertSQL := firstCached(t, &bus.upsertSQLCache)

	checks := map[string]struct {
		sql   string
		wants []string
	}{
		"insert": {insertSQL, []string{`INSERT INTO "items" (`, `$1`, `$2`, `)`}},
		"update": {updateSQL, []string{`SET "title" = $1`, `WHERE "title" = $3`}},
		"upsert": {upsertSQL, []string{`$1`, `$2`, `ON CONFLICT("title") DO UPDATE SET`}},
		"delete": {delSQL, []string{`WHERE "id" = $1`}},
	}
	for name, c := range checks {
		for _, want := range c.wants {
			if !strings.Contains(c.sql, want) {
				t.Errorf("%s: postgres template missing %q:\n%s", name, want, c.sql)
			}
		}
		if strings.Contains(c.sql, "?") {
			t.Errorf("%s: postgres template must not contain anonymous '?':\n%s", name, c.sql)
		}
	}
	if !strings.Contains(sumSQL, "SUM") {
		t.Errorf("sum query unexpected:\n%s", sumSQL)
	}
}

func TestEnsureTableUsesDialectAutoPK(t *testing.T) {
	bus := newDialectTestBus(t)
	colDefs := []string{`"name" TEXT`}
	if err := bus.ensureTable("pk_check", colDefs); err != nil {
		t.Fatalf("ensureTable: %v", err)
	}
	var sqlText string
	if err := bus.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pk_check'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "_spine_id INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Errorf("table DDL missing sqlite auto PK:\n%s", sqlText)
	}
}
