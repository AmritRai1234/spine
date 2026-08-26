package engine

import (
	"fmt"
	"strings"
	"time"
)

// Migration represents a versioned DDL schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// initMigrationsTable initializes the migration tracking table.
func (b *Bus) initMigrationsTable() error {
	query := `CREATE TABLE IF NOT EXISTS "_spine_migrations" (
		id ` + b.dialect.autoIncPK + `,
		version INTEGER UNIQUE NOT NULL,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	);`
	_, err := b.db.Exec(query)
	return err
}

// splitStatements splits a migration script on top-level semicolons (outside
// quoted regions). SQLite tolerates multi-statement Exec; pgx does not — this
// is the same limitation the engine documents for initEventTable.
func splitStatements(sql string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' || c == '"' {
			if inQuote == 0 {
				inQuote = c
			} else if inQuote == c {
				inQuote = 0
			}
		}
		if c == ';' && inQuote == 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	return parts
}

// isUniqueViolation reports whether err is a duplicate-key error across the
// supported dialects (SQLite "UNIQUE constraint failed", Postgres 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

// ApplyMigration applies a versioned migration if not already applied.
func (b *Bus) ApplyMigration(m Migration) (bool, error) {
	if err := b.initMigrationsTable(); err != nil {
		return false, fmt.Errorf("failed to init migrations table: %w", err)
	}

	var count int
	err := b.db.QueryRow(`SELECT COUNT(1) FROM "_spine_migrations" WHERE version = `+b.ph(1), m.Version).Scan(&count)
	if err != nil {
		return false, err
	}

	if count > 0 {
		return false, nil // Migration already applied
	}

	tx, err := b.db.Begin()
	if err != nil {
		return false, fmt.Errorf("failed to start migration transaction: %w", err)
	}

	statements := []string{m.SQL}
	if b.dialect.name == "pgx" {
		statements = splitStatements(m.SQL)
	}
	for _, stmt := range statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			tx.Rollback()
			return false, fmt.Errorf("migration v%d (%s) failed: %w", m.Version, m.Name, err)
		}
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO "_spine_migrations" (version, name, applied_at) VALUES (`+b.ph(1)+`, `+b.ph(2)+`, `+b.ph(3)+`)`, m.Version, m.Name, nowStr); err != nil {
		tx.Rollback()
		// TOCTOU: a concurrent applier inserted the version row between our
		// COUNT and this INSERT — the migration is effectively applied.
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to record migration v%d: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit migration v%d: %w", m.Version, err)
	}

	return true, nil
}

// GetAppliedMigrations retrieves all applied migrations.
func (b *Bus) GetAppliedMigrations() ([]Migration, error) {
	if err := b.initMigrationsTable(); err != nil {
		return nil, err
	}

	rows, err := b.db.Query(`SELECT version, name FROM "_spine_migrations" ORDER BY version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Migration
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.Version, &m.Name); err == nil {
			result = append(result, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading applied migrations failed: %w", err)
	}
	return result, nil
}
