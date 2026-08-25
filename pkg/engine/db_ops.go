package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
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
				if _, err := b.db.Exec(alterSQL); err != nil && !isColumnExistsErr(err) {
					// "duplicate column" is the expected idempotent path when
					// the table predates this manifest; anything else is a
					// real failure (permissions, disk, malformed definition)
					// that would otherwise resurface later as confusing
					// insert errors far from the cause.
					return fmt.Errorf("add column %s.%s failed: %w", table, colName, err)
				}
			}

			if strings.HasSuffix(colName, "_id") || colName == "id" || colName == "email" || colName == "status" || colName == "state" {
				idxSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "idx_%s_%s" ON "%s"("%s")`,
					table, colName, table, colName)
				if _, err := b.db.Exec(idxSQL); err != nil {
					return fmt.Errorf("create index idx_%s_%s failed: %w", table, colName, err)
				}
			}
		}
	}

	b.knownTable.Store(colKey, true)
	return nil
}

// isColumnExistsErr reports whether err is the benign "column already exists"
// response from an idempotent ALTER TABLE ADD COLUMN across supported dialects.
func isColumnExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || // SQLite
		strings.Contains(msg, "already exists") // Postgres / others
}

func normalizeParam(v interface{}, eventName string, payload map[string]interface{}) interface{} {	if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
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

// dbInsert persists the payload as a new row. By default the write goes
// through the async sharded batch writer; with config `sync: "true"` it runs
// synchronously via execWithRetry so a subsequent synchronous read (db.sum,
// db.lookup, db.adjust) in the SAME route chain — or the next route — is
// guaranteed to observe the row. Commerce uses this for order lines: the
// server-side subtotal at PLACE_ORDER must never race the line insert.
func (b *Bus) dbInsert(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	table := step.Table
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

	if step.Config["sync"] == "true" {
		// Synchronous write: errors surface to the route (on_failure fires)
		// and the row is durable before the next step runs.
		if err := execWithRetry(b.db, tmpl.sql, values...); err != nil {
			return fmt.Errorf("insert failed: %w", err)
		}
		return nil
	}

	if !b.writer.submit(table, dbTask{query: tmpl.sql, params: values}) {
		// All shards full — synchronous fallback so the write is never
		// silently dropped and failures surface to the route (on_failure).
		if err := execWithRetry(b.db, tmpl.sql, values...); err != nil {
			return fmt.Errorf("insert failed: %w", err)
		}
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
			return fmt.Errorf("db.update requires at least 2 payload fields when no explicit 'where' is given (one where-key + one value to set), got %d", n)
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

// dbLookup finds a single row matching key_column = value_expr (a resolvable
// expression, e.g. "$event.payload.product_id") and merges every column of the
// row into the event payload. An optional "as" config prefixes merged keys
// ("as: product_" turns price → product_price) to avoid clobbering event fields.
// Config "optional: true" tolerates a missing row (nothing merges, step passes)
// for notification-style lookups; default behaviour errors so route on_failure
// machinery handles it.
func (b *Bus) dbLookup(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	keyColumn := step.Config["key_column"]
	valueExpr := step.Config["value_expr"]
	if keyColumn == "" || valueExpr == "" {
		return fmt.Errorf("db.lookup requires 'key_column' and 'value_expr' config")
	}

	safeTable := b.sanitizeIdentCached(step.Table)
	safeKeyCol := b.sanitizeIdentCached(keyColumn)
	if safeTable == "" || safeKeyCol == "" {
		return fmt.Errorf("db.lookup requires 'table' and a valid 'key_column'")
	}

	value := ResolveValue(valueExpr, eventName, payload)
	query := fmt.Sprintf(`SELECT * FROM "%s" WHERE "%s" = %s LIMIT 1`, safeTable, safeKeyCol, b.ph(1))
	rows, err := b.queryRows(query, value)
	if err != nil {
		return fmt.Errorf("db.lookup failed on '%s': %w", safeTable, err)
	}
	if len(rows) == 0 {
		if step.Config["optional"] == "true" {
			return nil
		}
		return fmt.Errorf("db.lookup: no row in '%s' where %s = %v", safeTable, safeKeyCol, value)
	}

	prefix := step.Config["as"]
	for k, v := range rows[0] {
		if k == "_spine_id" || k == "_error_context" {
			continue
		}
		payload[prefix+k] = v
	}
	return nil
}

// dbAdjust performs an atomic relative update: SET col = col + delta WHERE ...
// The delta ("by" config) is a resolvable integer expression (may be negative)
// and is always bound as a parameter — never interpolated into the SQL text.
//
// Optional config:
//   - floor: minimum allowed resulting value; when set, the UPDATE gains an
//     AND col + ? >= floor guard. Zero affected rows → step fails, so
//     insufficient stock / balance aborts the route via on_failure.
//
// Runs synchronously against the DB pool (not the batched writer): read-
// modify-write steps must complete before downstream reads and must surface
// errors to the route pipeline.
func (b *Bus) dbAdjust(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	column := step.Config["column"]
	byExpr := step.Config["by"]
	if column == "" || byExpr == "" {
		return fmt.Errorf("db.adjust requires 'column' and 'by' config")
	}
	if step.Where == "" {
		return fmt.Errorf("db.adjust requires a parameterized 'where' condition")
	}

	delta, err := resolveInt(byExpr, eventName, payload)
	if err != nil {
		return fmt.Errorf("db.adjust invalid 'by' expression '%s': %w", byExpr, err)
	}

	whereCol, whereOp, whereVal, err := parseWhereCondition(step.Where, eventName, payload)
	if err != nil {
		return fmt.Errorf("db.adjust: %w", err)
	}

	safeTable := b.sanitizeIdentCached(step.Table)
	safeCol := b.sanitizeIdentCached(column)
	safeWhereCol := b.sanitizeIdentCached(whereCol)
	if safeTable == "" || safeCol == "" || safeWhereCol == "" {
		return fmt.Errorf("db.adjust: table, column and where column must be valid identifiers")
	}

	// Ensure the adjusted column exists (no-op for established tables).
	if err := b.ensureTable(safeTable, []string{`"` + safeCol + `" INTEGER`}); err != nil {
		return fmt.Errorf("db.adjust ensure column failed: %w", err)
	}

	var query string
	var params []interface{}
	if floorStr := step.Config["floor"]; floorStr != "" {
		floor, ferr := resolveInt(floorStr, eventName, payload)
		if ferr != nil {
			return fmt.Errorf("db.adjust invalid 'floor' expression '%s': %w", floorStr, ferr)
		}
		// Placeholders are strictly positional: delta appears twice (SET + guard).
		query = fmt.Sprintf(`UPDATE "%s" SET "%s" = "%s" + %s WHERE "%s" %s %s AND "%s" + %s >= %s`,
			safeTable, safeCol, safeCol, b.ph(1), safeWhereCol, whereOp, b.ph(2), safeCol, b.ph(3), b.ph(4))
		params = []interface{}{delta, whereVal, delta, floor}
	} else {
		query = fmt.Sprintf(`UPDATE "%s" SET "%s" = "%s" + %s WHERE "%s" %s %s`,
			safeTable, safeCol, safeCol, b.ph(1), safeWhereCol, whereOp, b.ph(2))
		params = []interface{}{delta, whereVal}
	}

	res, err := execWithRetryResult(b.db, query, params...)
	if err != nil {
		return fmt.Errorf("db.adjust failed: %w", err)
	}
	if step.Config["floor"] != "" {
		if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
			// Distinguish between row-not-found and floor rejection.
			// Run a cheap SELECT 1 existence check on the same WHERE condition.
			var exists int
			checkQuery := fmt.Sprintf(`SELECT 1 FROM "%s" WHERE "%s" %s %s LIMIT 1`,
				safeTable, safeWhereCol, whereOp, b.ph(1))
			if err := b.db.QueryRow(checkQuery, whereVal).Scan(&exists); err != nil {
				return fmt.Errorf("db.adjust rejected: row not found in '%s' where %s %s %v",
					safeTable, safeWhereCol, whereOp, whereVal)
			}
			return fmt.Errorf("db.adjust rejected: adjustment of '%s' by %+d would cross floor %s",
				safeCol, delta, step.Config["floor"])
		}
	}
	return nil
}

// resolveInt evaluates an integer expression: literal, or $-prefixed template.
// Accepts int/int64/float64 payloads and negative literals like "-$event.payload.qty".
func resolveInt(expr string, eventName string, payload map[string]interface{}) (int64, error) {
	switch v := ResolveValue(expr, eventName, payload).(type) {
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint64:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	default:
		return 0, fmt.Errorf("not an integer expression")
	}
}

// resolveFloat evaluates a numeric expression: literal or $-prefixed template.
// Accepts the same payload kinds as resolveInt plus decimal strings — dollar
// amounts arrive as JSON numbers but manifest literals are strings.
func resolveFloat(expr string, eventName string, payload map[string]interface{}) (float64, error) {
	switch v := ResolveValue(expr, eventName, payload).(type) {
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case float64:
		return v, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, fmt.Errorf("not a numeric expression")
	}
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

	// Build typed column definitions from the manifest field types.
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

	// Ensure the table (and thus the conflict column) exists BEFORE creating
	// its unique index: PostgreSQL rejects CREATE UNIQUE INDEX on a missing
	// column (SQLSTATE 42703), and SQLite rejects ON CONFLICT without a
	// matching unique constraint. Doing this outside the cache branch also
	// fixes first-upsert on a fresh table.
	if err := b.ensureTable(table, colDefs); err != nil {
		return err
	}
	b.ensureUniqueIndex(table, safeConflictKey)

	// Lookup or build the SQL template
	var tmpl *sqlTemplate
	if cached, ok := b.upsertSQLCache.Load(fingerprint); ok {
		tmpl = cached.(*sqlTemplate)
	} else {
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
		// All shards full — synchronous fallback so the write is never
		// silently dropped and failures surface to the route (on_failure).
		if err := execWithRetry(b.db, tmpl.sql, values...); err != nil {
			return fmt.Errorf("upsert failed: %w", err)
		}
	}

	return nil
}
