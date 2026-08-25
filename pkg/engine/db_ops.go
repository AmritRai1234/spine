package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// sqlTemplate holds a pre-built SQL string and the deterministic column order.
// Cached per table+columns fingerprint to eliminate per-call string building.
type sqlTemplate struct {
	sql      string   // e.g. INSERT INTO "x" ("a", "b") VALUES (?, ?)
	colOrder []string // sanitized column names in sorted order
	colDefs  []string // column definitions for ensureTable
}

// sanitizeIdent strips anything that isn't alphanumeric or underscore.
func sanitizeIdent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// sanitizeIdentCached returns sanitized identifier with caching for repeated names.
// Results are cached in identCache sync.Map for repeated column/table names.
func (b *Bus) sanitizeIdentCached(s string) string {
	if cached, ok := b.identCache.Load(s); ok {
		return cached.(string)
	}
	result := sanitizeIdent(s)
	b.identCache.Store(s, result)
	return result
}

// ensureTable creates the table and auto-generates column indexes or adds missing columns.
// Uses knownTable sync.Map to skip SQL when the same column set has been seen before.
func (b *Bus) ensureTable(table string, colDefs []string) error {
	// Build a fingerprint of the column definitions for this call
	colKey := table + "|" + strings.Join(colDefs, ",")

	// Fast path: this exact table+columns combo already ensured — skip all SQL
	if _, known := b.knownTable.Load(colKey); known {
		return nil
	}

	// Use _spine_id as the internal auto-increment PK to avoid colliding
	// with user payload fields named "id" (which would cause datatype mismatch).
	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (_spine_id %s, %s)`,
		table, b.dialect.autoIncPK, strings.Join(colDefs, ", "))
	if _, err := b.db.Exec(createSQL); err != nil {
		return fmt.Errorf("create table failed: %w", err)
	}

	for _, colDef := range colDefs {
		parts := strings.Fields(colDef)
		if len(parts) > 0 {
			colName := strings.Trim(parts[0], `"`)
			if colName != "_spine_id" {
				alterSQL := fmt.Sprintf(`ALTER TABLE "%s" ADD COLUMN %s`, table, colDef)
				_, _ = b.db.Exec(alterSQL)
			}

			if strings.HasSuffix(colName, "_id") || colName == "id" || colName == "email" || colName == "status" || colName == "state" {
				idxSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "idx_%s_%s" ON "%s"("%s")`,
					table, colName, table, colName)
				_, _ = b.db.Exec(idxSQL)
			}
		}
	}

	b.knownTable.Store(colKey, true)
	return nil
}

func normalizeParam(v interface{}, eventName string, payload map[string]interface{}) interface{} {
	if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
		return ResolveVariables(strVal, eventName, payload)
	}
	switch val := v.(type) {
	case map[string]interface{}, []interface{}, []string, map[string]string:
		bytes, err := json.Marshal(val)
		if err == nil {
			return string(bytes)
		}
	}
	return v
}

// sqliteType maps a manifest field type to the corresponding SQLite column type.
func sqliteType(fieldType string) string {
	switch strings.ToLower(fieldType) {
	case "number", "float":
		return "REAL"
	case "int", "integer":
		return "INTEGER"
	case "boolean", "bool":
		return "INTEGER"
	default:
		return "TEXT"
	}
}

func (b *Bus) dbInsert(table string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n == 0 {
		return nil
	}

	table = b.sanitizeIdentCached(table)

	// Deterministic key ordering: sort payload keys for stable SQL + caching
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build cache fingerprint from sorted sanitized column names
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.insertSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions from manifest field types
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		// Ensure table exists (only on first encounter of this fingerprint)
		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Build SQL string once
		var sb strings.Builder
		sb.Grow(64 + len(table) + n*12)
		sb.WriteString(`INSERT INTO "`)
		sb.WriteString(table)
		sb.WriteString(`" (`)
		for i, safe := range sanitizedKeys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteByte('"')
		}
		sb.WriteString(") VALUES (")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(b.ph(i + 1))
		}
		sb.WriteByte(')')

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.insertSQLCache.Store(fingerprint, tmpl)
	}

	// Build values in deterministic column order
	values := make([]interface{}, n)
	for i, k := range keys {
		values[i] = normalizeParam(payload[k], eventName, payload)
	}

	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: values}) {
		// All shards full — async overflow
		go func(t dbTask) {
			b.writer.submit(table, t)
		}(dbTask{query: tmpl.sql, params: values})
	}

	return nil
}

func (b *Bus) dbUpdate(table string, whereExpr string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n == 0 {
		return nil
	}

	table = b.sanitizeIdentCached(table)

	// Deterministic key ordering: sort payload keys for stable SQL + caching
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Resolve the WHERE clause. An explicit `where:` expression is parsed and
	// parameterized (safe for template-interpolated values). Without one, the
	// update falls back to matching on the payload's "id" field (or the
	// alphabetically first key when "id" is absent).
	var whereCol, whereOp string
	var whereVal interface{}
	explicitWhere := whereExpr != ""
	if explicitWhere {
		col, op, val, err := parseWhereCondition(whereExpr, eventName, payload)
		if err != nil {
			return fmt.Errorf("db.update: %w", err)
		}
		whereCol = b.sanitizeIdentCached(col)
		whereOp = op
		whereVal = val
	} else {
		if n < 2 {
			return nil
		}
		whereKey := keys[0]
		if _, ok := payload["id"]; ok {
			whereKey = "id"
		}
		whereCol = b.sanitizeIdentCached(whereKey)
		whereOp = "="
		whereVal = normalizeParam(payload[whereKey], eventName, payload)
	}

	// Build cache fingerprint: table + where target + mode + sorted columns
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + len(whereCol) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	fpBuf.WriteString(whereCol)
	fpBuf.WriteByte('|')
	fpBuf.WriteString(whereOp)
	fpBuf.WriteByte('|')
	if explicitWhere {
		fpBuf.WriteString("expr")
	} else {
		fpBuf.WriteString("auto")
	}
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.updateSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions from manifest field types
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		// Ensure table exists (only on first encounter of this fingerprint)
		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Build SQL string once: UPDATE "table" SET "col1" = $1, ... WHERE "whereCol" op $n
		var sb strings.Builder
		sb.Grow(64 + len(table) + n*12)
		sb.WriteString(`UPDATE "`)
		sb.WriteString(table)
		sb.WriteString(`" SET `)
		first := true
		paramN := 0
		for _, safe := range sanitizedKeys {
			// Fallback mode: the where key identifies the row, so it is not SET.
			// Explicit where mode: every payload field is SET.
			if !explicitWhere && safe == whereCol {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			paramN++
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteString(`" = `)
			sb.WriteString(b.ph(paramN))
			first = false
		}
		sb.WriteString(` WHERE "`)
		sb.WriteString(whereCol)
		sb.WriteString(`" `)
		sb.WriteString(whereOp)
		sb.WriteString(` `)
		sb.WriteString(b.ph(paramN + 1))

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.updateSQLCache.Store(fingerprint, tmpl)
	}

	// Build params in deterministic column order (SET values first, then WHERE value)
	params := make([]interface{}, 0, n)
	for _, k := range keys {
		if !explicitWhere && b.sanitizeIdentCached(k) == whereCol {
			continue
		}
		params = append(params, normalizeParam(payload[k], eventName, payload))
	}
	params = append(params, whereVal)

	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: params}) {
		if err := execWithRetry(b.db, tmpl.sql, params...); err != nil {
			return fmt.Errorf("update failed: %w", err)
		}
	}

	return nil
}

// parseWhereCondition parses a where expression into column, operator, and value components.
// Template variables in the value are resolved. The column name is sanitized.
// This prevents SQL injection by using parameterized queries instead of string interpolation.
//
// Supported formats:
//
//	"column = value"
//	"column = '$event.payload.field'"
//	"column != value"
//	"column > 100"
func parseWhereCondition(expr string, eventName string, payload map[string]interface{}) (column string, op string, value string, err error) {
	// Supported operators in order of specificity (multi-char first to avoid partial matches)
	ops := []string{"!=", ">=", "<=", "==", "=", ">", "<"}

	// Scan for operators outside of quoted strings to avoid matching
	// operators that appear inside quoted values
	inQuote := byte(0)
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if c == '\'' || c == '"' {
			if inQuote == 0 {
				inQuote = c
			} else if inQuote == c {
				inQuote = 0
			}
			continue
		}
		if inQuote != 0 {
			continue
		}

		// Check for each operator at this position (requires space-delimited: " op ")
		for _, o := range ops {
			pattern := " " + o + " "
			if i+len(pattern) <= len(expr) && expr[i:i+len(pattern)] == pattern {
				column = strings.TrimSpace(expr[:i])
				value = strings.TrimSpace(expr[i+len(pattern):])

				// Resolve template variables in the value
				value = ResolveVariables(value, eventName, payload)

				// Strip surrounding quotes from value if present
				if len(value) >= 2 {
					if (value[0] == '\'' && value[len(value)-1] == '\'') ||
						(value[0] == '"' && value[len(value)-1] == '"') {
						value = value[1 : len(value)-1]
					}
				}

				// Normalize == to SQL =
				actualOp := o
				if actualOp == "==" {
					actualOp = "="
				}
				return column, actualOp, value, nil
			}
		}
	}

	return "", "", "", fmt.Errorf("unsupported where expression format: '%s' (expected 'column op value', e.g. \"email = $event.payload.email\")", expr)
}

func (b *Bus) dbDelete(table string, whereExpr string, eventName string, payload map[string]interface{}) error {
	table = b.sanitizeIdentCached(table)

	var deleteSQL string
	var params []interface{}

	if whereExpr == "" {
		if idVal, ok := payload["id"]; ok {
			deleteSQL = fmt.Sprintf(`DELETE FROM "%s" WHERE "id" = %s`, table, b.ph(1))
			params = []interface{}{idVal}
		} else if len(payload) > 0 {
			// Fallback: use first payload key as delete condition
			for k, v := range payload {
				safeK := b.sanitizeIdentCached(k)
				deleteSQL = fmt.Sprintf(`DELETE FROM "%s" WHERE "%s" = %s`, table, safeK, b.ph(1))
				params = []interface{}{v}
				break
			}
		} else {
			return fmt.Errorf("db.delete requires 'where' condition or non-empty payload")
		}
	} else {
		// Parse where expression into column/operator/value and use parameterized query
		// to prevent SQL injection from resolved template variables.
		col, op, val, err := parseWhereCondition(whereExpr, eventName, payload)
		if err != nil {
			return fmt.Errorf("db.delete: %w", err)
		}
		safeCol := b.sanitizeIdentCached(col)
		deleteSQL = fmt.Sprintf(`DELETE FROM "%s" WHERE "%s" %s %s`, table, safeCol, op, b.ph(1))
		params = []interface{}{val}
	}

	if !b.writer.submit(table, dbTask{query: deleteSQL, params: params}) {
		if err := execWithRetry(b.db, deleteSQL, params...); err != nil {
			return fmt.Errorf("delete failed: %w", err)
		}
	}

	return nil
}

// dbSum computes SUM(column) over a table with an optional parameterized where
// clause and injects the result into the payload under `as` (default "sum_result").
// Reads go straight to the DB pool (safe under WAL) instead of the write queue.
func (b *Bus) dbSum(table string, column string, whereExpr string, as string, eventName string, payload map[string]interface{}) error {
	safeTable := b.sanitizeIdentCached(table)
	safeCol := b.sanitizeIdentCached(column)

	if safeTable == "" || safeCol == "" {
		return fmt.Errorf("db.sum requires 'table' and 'column'")
	}
	if as == "" {
		as = "sum_result"
	}

	query := fmt.Sprintf(`SELECT COALESCE(SUM("%s"), 0) FROM "%s"`, safeCol, safeTable)
	var params []interface{}
	if whereExpr != "" {
		col, op, val, err := parseWhereCondition(whereExpr, eventName, payload)
		if err != nil {
			return fmt.Errorf("db.sum: %w", err)
		}
		safeWCol := b.sanitizeIdentCached(col)
		query += fmt.Sprintf(` WHERE "%s" %s %s`, safeWCol, op, b.ph(1))
		params = append(params, val)
	}

	var sum float64
	if err := b.db.QueryRow(query, params...).Scan(&sum); err != nil {
		return fmt.Errorf("db.sum failed: %w", err)
	}
	payload[as] = sum
	return nil
}

// ensureUniqueIndex guarantees the ON CONFLICT target column carries a unique
// index before any upsert runs (PostgreSQL rejects ON CONFLICT with SQLSTATE
// 42P10 otherwise; SQLite tolerates it only by accident of PK semantics).
// Checked on every call via a lock-free sync.Map — common case is one atomic
// load. Failure is NOT cached: a dropped/failed index self-heals on the next
// upsert instead of leaving a permanently broken cached template.
func (b *Bus) ensureUniqueIndex(table, col string) {
	fp := table + "|" + col
	if _, ok := b.uniqueIdx.Load(fp); ok {
		return
	}
	idxSQL := fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS "uq_%s_%s" ON "%s"("%s")`,
		table, col, table, col)
	if _, err := b.db.Exec(idxSQL); err != nil {
		log.Printf("[upsert] ensure unique index uq_%s_%s failed: %v", table, col, err)
		return
	}
	b.uniqueIdx.Store(fp, struct{}{})
}

func (b *Bus) dbUpsert(table string, conflictKey string, eventName string, payload map[string]interface{}) error {
	n := len(payload)
	if n == 0 {
		return nil
	}

	if _, exists := payload[conflictKey]; !exists {
		return fmt.Errorf("db.upsert failed: conflict key '%s' not present in payload", conflictKey)
	}

	table = b.sanitizeIdentCached(table)
	safeConflictKey := b.sanitizeIdentCached(conflictKey)
	b.ensureUniqueIndex(table, safeConflictKey)

	// Deterministic key ordering
	keys := make([]string, 0, n)
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build cache fingerprint
	var fpBuf strings.Builder
	fpBuf.Grow(len(table) + len(safeConflictKey) + n*10)
	fpBuf.WriteString(table)
	fpBuf.WriteByte('|')
	fpBuf.WriteString(safeConflictKey)
	fpBuf.WriteByte('|')
	sanitizedKeys := make([]string, n)
	for i, k := range keys {
		safe := b.sanitizeIdentCached(k)
		sanitizedKeys[i] = safe
		if i > 0 {
			fpBuf.WriteByte(',')
		}
		fpBuf.WriteString(safe)
	}
	fingerprint := fpBuf.String()

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.upsertSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
		// Build typed column definitions
		fieldTypes := b.GetRegistry().GetFieldTypes(eventName)
		colDefs := make([]string, n)
		for i, safe := range sanitizedKeys {
			sqlType := "TEXT"
			if fieldTypes != nil {
				if ft, ok := fieldTypes[keys[i]]; ok {
					sqlType = sqliteType(ft)
				}
			}
			colDefs[i] = `"` + safe + `" ` + sqlType
		}

		if err := b.ensureTable(table, colDefs); err != nil {
			return err
		}

		// Build: INSERT INTO "t" ("a","b") VALUES (?,?) ON CONFLICT("key") DO UPDATE SET "a"=excluded."a"
		var sb strings.Builder
		sb.Grow(128 + len(table) + n*24)
		sb.WriteString(`INSERT INTO "`)
		sb.WriteString(table)
		sb.WriteString(`" (`)
		for i, safe := range sanitizedKeys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteByte('"')
		}
		sb.WriteString(") VALUES (")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(b.ph(i + 1))
		}
		sb.WriteString(`) ON CONFLICT("`)
		sb.WriteString(safeConflictKey)
		sb.WriteString(`") DO UPDATE SET `)
		first := true
		for _, safe := range sanitizedKeys {
			if safe == safeConflictKey {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			sb.WriteByte('"')
			sb.WriteString(safe)
			sb.WriteString(`" = excluded."`)
			sb.WriteString(safe)
			sb.WriteByte('"')
			first = false
		}

		tmpl = &sqlTemplate{
			sql:      sb.String(),
			colOrder: sanitizedKeys,
			colDefs:  colDefs,
		}
		b.upsertSQLCache.Store(fingerprint, tmpl)
	}

	// Build values in deterministic column order
	values := make([]interface{}, n)
	for i, k := range keys {
		values[i] = normalizeParam(payload[k], eventName, payload)
	}

	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: values}) {
		go func(t dbTask) {
			b.writer.submit(table, t)
		}(dbTask{query: tmpl.sql, params: values})
	}

	return nil
}
