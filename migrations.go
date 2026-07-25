package spine

import (
	"fmt"
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
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version INTEGER UNIQUE NOT NULL,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	);`
	_, err := b.db.Exec(query)
	return err
}

// ApplyMigration applies a versioned migration if not already applied.
func (b *Bus) ApplyMigration(m Migration) (bool, error) {
	if err := b.initMigrationsTable(); err != nil {
		return false, fmt.Errorf("failed to init migrations table: %w", err)
	}

	var count int
	err := b.db.QueryRow(`SELECT COUNT(1) FROM "_spine_migrations" WHERE version = ?`, m.Version).Scan(&count)
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

	if _, err := tx.Exec(m.SQL); err != nil {
		tx.Rollback()
		return false, fmt.Errorf("migration v%d (%s) failed: %w", m.Version, m.Name, err)
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO "_spine_migrations" (version, name, applied_at) VALUES (?, ?, ?)`, m.Version, m.Name, nowStr); err != nil {
		tx.Rollback()
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
	return result, nil
}
