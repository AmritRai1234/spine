package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TableInfo describes a database table and its row count.
type TableInfo struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// GetTables lists all tables in the database along with their current row count.
func (b *Bus) GetTables() ([]TableInfo, error) {
	rows, err := b.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		// Fallback for non-SQLite / Turso drivers if sqlite_master isn't available
		var res []TableInfo
		b.knownTable.Range(func(key, value interface{}) bool {
			if name, ok := key.(string); ok {
				var count int64
				_ = b.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, name)).Scan(&count)
				res = append(res, TableInfo{Name: name, Rows: count})
			}
			return true
		})
		return res, nil
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			var count int64
			_ = b.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, name)).Scan(&count)
			tables = append(tables, TableInfo{Name: name, Rows: count})
		}
	}
	return tables, nil
}

// GetTableRows returns rows from a table with pagination limit and offset.
func (b *Bus) GetTableRows(table string, limit, offset int) ([]map[string]interface{}, error) {
	table = sanitizeIdent(table)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := fmt.Sprintf(`SELECT * FROM "%s" ORDER BY id DESC LIMIT %d OFFSET %d`, table, limit, offset)
	rows, err := b.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query table '%s': %w", table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		rowMap := make(map[string]interface{})
		for i, col := range cols {
			val := values[i]
			if bVal, ok := val.([]byte); ok {
				rowMap[col] = string(bVal)
			} else {
				rowMap[col] = val
			}
		}
		results = append(results, rowMap)
	}

	if results == nil {
		results = []map[string]interface{}{}
	}
	return results, nil
}

// GetEventLogs queries recent event audit records from the _spine_events table.
func (b *Bus) GetEventLogs(eventName string, limit, offset int) ([]map[string]interface{}, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var query string
	var args []interface{}

	if eventName != "" {
		query = `SELECT id, event_name, payload, emitted_states, created_at FROM "_spine_events" WHERE event_name = ? ORDER BY id DESC LIMIT ? OFFSET ?`
		args = []interface{}{eventName, limit, offset}
	} else {
		query = `SELECT id, event_name, payload, emitted_states, created_at FROM "_spine_events" ORDER BY id DESC LIMIT ? OFFSET ?`
		args = []interface{}{limit, offset}
	}

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return []map[string]interface{}{}, nil
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var id int64
		var name, payloadStr, statesStr, createdAt string
		if err := rows.Scan(&id, &name, &payloadStr, &statesStr, &createdAt); err == nil {
			var payload map[string]interface{}
			_ = json.Unmarshal([]byte(payloadStr), &payload)

			states := strings.Split(statesStr, ",")

			events = append(events, map[string]interface{}{
				"id":             id,
				"event":          name,
				"payload":        payload,
				"emitted_states": states,
				"created_at":     createdAt,
			})
		}
	}

	if events == nil {
		events = []map[string]interface{}{}
	}
	return events, nil
}

// initEventTable creates the _spine_events system table for automatic event audit logging.
func (b *Bus) initEventTable() {
	query := `CREATE TABLE IF NOT EXISTS "_spine_events" (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_name TEXT NOT NULL,
		payload TEXT NOT NULL,
		emitted_states TEXT,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS "idx_spine_events_name" ON "_spine_events"("event_name");`
	b.db.Exec(query)
}

// logEventAudit enqueues an event emission to the _spine_events audit table.
func (b *Bus) logEventAudit(event string, payload map[string]interface{}, emitted []string) {
	payloadBytes, _ := json.Marshal(payload)
	statesStr := strings.Join(emitted, ",")
	nowStr := time.Now().UTC().Format(time.RFC3339)

	insertSQL := `INSERT INTO "_spine_events" (event_name, payload, emitted_states, created_at) VALUES (?, ?, ?, ?)`
	params := []interface{}{event, string(payloadBytes), statesStr, nowStr}

	select {
	case b.writeChan <- dbTask{query: insertSQL, params: params}:
	default:
		// Non-blocking drop on high-volume burst saturation
	}
}
