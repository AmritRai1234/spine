package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Pool for JSON encoding buffers used in audit logging
var auditBufPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 256))
	},
}

// Pre-built constant SQL for audit insert — avoids string allocation per emit
const auditInsertSQL = `INSERT INTO "_spine_events" (event_name, payload, emitted_states, created_at) VALUES (?, ?, ?, ?)`

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

	query := fmt.Sprintf(`SELECT * FROM "%s" ORDER BY _spine_id DESC LIMIT %d OFFSET %d`, table, limit, offset)
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

// QueryWhere returns rows from a table filtered by a single column equality condition.
// Both table and column names are sanitized; the value uses a parameterized query.
func (b *Bus) QueryWhere(table, column, value string, limit, offset int) ([]map[string]interface{}, error) {
	table = sanitizeIdent(table)
	column = sanitizeIdent(column)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := fmt.Sprintf(`SELECT * FROM "%s" WHERE "%s" = ? ORDER BY _spine_id DESC LIMIT %d OFFSET %d`,
		table, column, limit, offset)
	rows, err := b.db.Query(query, value)
	if err != nil {
		return nil, fmt.Errorf("failed to query table '%s' where %s = '%s': %w", table, column, value, err)
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

// GetTableRowsWithFilter returns rows with an access-level row filter applied.
// The accessFilter is parsed into a parameterized WHERE clause for safety.
func (b *Bus) GetTableRowsWithFilter(table string, limit, offset int, accessFilter string) ([]map[string]interface{}, error) {
	table = sanitizeIdent(table)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	col, op, val, err := parseWhereCondition(accessFilter, "", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid access filter '%s': %w", accessFilter, err)
	}
	safeCol := b.sanitizeIdentCached(col)

	query := fmt.Sprintf(`SELECT * FROM "%s" WHERE "%s" %s ? ORDER BY _spine_id DESC LIMIT %d OFFSET %d`,
		table, safeCol, op, limit, offset)
	return b.queryRows(query, val)
}

// QueryWhereWithAccess returns rows filtered by both a user WHERE param and an access filter.
func (b *Bus) QueryWhereWithAccess(table, column, value string, limit, offset int, accessFilter string) ([]map[string]interface{}, error) {
	table = sanitizeIdent(table)
	column = sanitizeIdent(column)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	if accessFilter == "" {
		// No access filter — delegate to standard QueryWhere
		return b.QueryWhere(table, column, value, limit, offset)
	}

	col, op, val, err := parseWhereCondition(accessFilter, "", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid access filter '%s': %w", accessFilter, err)
	}
	safeFilterCol := b.sanitizeIdentCached(col)

	query := fmt.Sprintf(`SELECT * FROM "%s" WHERE "%s" = ? AND "%s" %s ? ORDER BY _spine_id DESC LIMIT %d OFFSET %d`,
		table, column, safeFilterCol, op, limit, offset)
	return b.queryRows(query, value, val)
}

// GetTableRowsCursor returns rows from a table using keyset cursor pagination (`_spine_id < cursor`).
// Prevents offset drift under concurrent inserts.
func (b *Bus) GetTableRowsCursor(table string, lastID int64, limit int, accessFilter string) ([]map[string]interface{}, int64, error) {
	table = sanitizeIdent(table)
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	var query string
	var args []interface{}

	if accessFilter != "" {
		col, op, val, err := parseWhereCondition(accessFilter, "", nil)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid access filter '%s': %w", accessFilter, err)
		}
		safeFilterCol := b.sanitizeIdentCached(col)
		if lastID > 0 {
			query = fmt.Sprintf(`SELECT * FROM "%s" WHERE _spine_id < ? AND "%s" %s ? ORDER BY _spine_id DESC LIMIT %d`, table, safeFilterCol, op, limit)
			args = []interface{}{lastID, val}
		} else {
			query = fmt.Sprintf(`SELECT * FROM "%s" WHERE "%s" %s ? ORDER BY _spine_id DESC LIMIT %d`, table, safeFilterCol, op, limit)
			args = []interface{}{val}
		}
	} else {
		if lastID > 0 {
			query = fmt.Sprintf(`SELECT * FROM "%s" WHERE _spine_id < ? ORDER BY _spine_id DESC LIMIT %d`, table, limit)
			args = []interface{}{lastID}
		} else {
			query = fmt.Sprintf(`SELECT * FROM "%s" ORDER BY _spine_id DESC LIMIT %d`, table, limit)
		}
	}

	rows, err := b.queryRows(query, args...)
	if err != nil {
		return nil, 0, err
	}

	var nextCursor int64 = 0
	if len(rows) > 0 {
		lastRow := rows[len(rows)-1]
		if idVal, ok := lastRow["_spine_id"]; ok {
			switch v := idVal.(type) {
			case int64:
				nextCursor = v
			case float64:
				nextCursor = int64(v)
			}
		}
	}

	return rows, nextCursor, nil
}

// QueryMultiWhere queries a table matching multiple column=value filter conditions.
func (b *Bus) QueryMultiWhere(table string, filters map[string]string, limit, offset int, accessFilter string) ([]map[string]interface{}, error) {
	table = sanitizeIdent(table)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var whereClauses []string
	var args []interface{}

	for col, val := range filters {
		safeCol := sanitizeIdent(col)
		whereClauses = append(whereClauses, fmt.Sprintf(`"%s" = ?`, safeCol))
		args = append(args, val)
	}

	if accessFilter != "" {
		col, op, val, err := parseWhereCondition(accessFilter, "", nil)
		if err != nil {
			return nil, fmt.Errorf("invalid access filter '%s': %w", accessFilter, err)
		}
		safeFilterCol := b.sanitizeIdentCached(col)
		whereClauses = append(whereClauses, fmt.Sprintf(`"%s" %s ?`, safeFilterCol, op))
		args = append(args, val)
	}

	whereStmt := ""
	if len(whereClauses) > 0 {
		whereStmt = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`SELECT * FROM "%s" %s ORDER BY _spine_id DESC LIMIT %d OFFSET %d`, table, whereStmt, limit, offset)
	return b.queryRows(query, args...)
}

// queryRows is a helper that executes a query and scans all result rows into maps.
func (b *Bus) queryRows(query string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := b.db.Query(query, args...)
	if err != nil {
		return nil, err
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
// Uses pooled buffer for JSON encoding and pre-built constant SQL.
func (b *Bus) logEventAudit(event string, payload map[string]interface{}, emitted []string) {
	buf := auditBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	json.NewEncoder(buf).Encode(payload)
	// Remove trailing newline from Encode
	payloadStr := strings.TrimRight(buf.String(), "\n")
	auditBufPool.Put(buf)

	statesStr := strings.Join(emitted, ",")
	nowStr := time.Now().UTC().Format(time.RFC3339)

	params := []interface{}{event, payloadStr, statesStr, nowStr}

	b.writer.submitAny(dbTask{query: auditInsertSQL, params: params})
	// Non-blocking: submitAny silently drops if all shards saturated
}
