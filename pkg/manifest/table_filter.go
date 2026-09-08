package manifest

import (
	"fmt"
	"strings"
)

// ValidateTableFilter syntax-checks a per-table read-scope filter at parse
// time so a malformed scope fails startup instead of erroring (or worse,
// failing open) at request time. It mirrors the engine's where-condition
// grammar (single `column op value` comparison, engine/db_ops.go) without
// importing the engine package — the engine imports this one.
//
// Differences from the runtime parser are deliberate and documented:
//   - $event.payload.* / $env.* / $now references are ALLOWED here. The
//     runtime access filter resolves against an empty payload, so payload
//     references would match no rows — but rejecting them at parse time
//     would forbid legitimate $now-based scopes (e.g. "visible_until > $now").
//     Payload refs are instead rejected by the engine at request time via
//     the strict resolver (empty payload → unresolvable → error).
//   - Only syntax is checked: the column/value contract with the actual
//     table schema is the manifest author's responsibility (and a typo'd
//     column yields empty results, never open access — fail safe).
func ValidateTableFilter(filter string) error {
	if strings.TrimSpace(filter) == "" {
		return nil // empty filter = read all rows of the table (still table-scoped)
	}

	ops := []string{"!=", ">=", "<=", "==", "=", ">", "<"}
	inQuote := byte(0)
	for i := 0; i < len(filter); i++ {
		c := filter[i]
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
		for _, o := range ops {
			pattern := " " + o + " "
			if i+len(pattern) <= len(filter) && filter[i:i+len(pattern)] == pattern {
				column := strings.TrimSpace(filter[:i])
				value := strings.TrimSpace(filter[i+len(pattern):])
				if column == "" {
					return fmt.Errorf("filter '%s': empty column before operator", filter)
				}
				// Compound conditions would silently bind "1 AND b = 2" as one
				// parameter — same failure mode as the runtime parser rejects.
				if hasUnquotedAndOr(value) {
					return fmt.Errorf("filter '%s': compound conditions are not supported (single column-comparison only)", filter)
				}
				// Trailing AND/OR: "email = 'x' OR" — the 2-char word at the very
				// end escapes the 3-char window scan above (the engine shares this
				// blind spot), but binding "'x' OR" as a parameter is equally
				// broken, so reject it explicitly.
				if hasTrailingAndOr(value) {
					return fmt.Errorf("filter '%s': compound conditions are not supported (trailing AND/OR)", filter)
				}
				if value == "" {
					return fmt.Errorf("filter '%s': empty value after operator", filter)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("filter '%s': expected 'column op value' (e.g. \"email = $env.SHOPPER_EMAIL\")", filter)
}

// hasUnquotedAndOr reports whether s contains AND/OR as space-delimited
// words outside quoted regions — mirrors the engine's parser.
func hasUnquotedAndOr(s string) bool {
	upper := strings.ToUpper(s)
	inQuote := byte(0)
	for i := 0; i+3 <= len(s); i++ {
		c := s[i]
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
		if (upper[i:i+3] == "AND" || upper[i:i+3] == "OR") &&
			(i == 0 || s[i-1] == ' ') &&
			(i+3 >= len(s) || s[i+3] == ' ') {
			return true
		}
	}
	return false
}

// hasTrailingAndOr reports whether s ends with a dangling AND/OR — a
// 2-char word the 3-char window scan cannot see at end-of-string.
func hasTrailingAndOr(s string) bool {
	upper := strings.ToUpper(s)
	return strings.HasSuffix(upper, " AND") || strings.HasSuffix(upper, " OR") ||
		upper == "AND" || upper == "OR"
}
