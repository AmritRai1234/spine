package engine

import (
	"log"
	"strconv"
	"strings"
)

// equalsValue compares two resolved operands. When both parse as numbers,
// neither has a suspicious format (leading zeros like "007" or scientific
// notation like "1e3"), the comparison is numeric — this keeps guards
// correct across storage affinities (e.g. SQLite TEXT "15.0" compared to
// the event's float64(15)) while avoiding false positives.
func equalsValue(left, right string) bool {
	if left == right {
		return true
	}

	lTrim := strings.TrimSpace(left)
	rTrim := strings.TrimSpace(right)

	lf, lerr := strconv.ParseFloat(lTrim, 64)
	rf, rerr := strconv.ParseFloat(rTrim, 64)
	if lerr != nil || rerr != nil {
		return false
	}

	// Reject numeric comparison when either side has a suspicious format
	// that would lose information in float64: leading zeros (e.g. "007")
	// or scientific notation (e.g. "1e3").
	if hasSuspiciousNumericFormat(lTrim) || hasSuspiciousNumericFormat(rTrim) {
		return false
	}

	return lf == rf
}

// hasSuspiciousNumericFormat reports whether s could be a number in a format
// that would lose information when converted to float64 — specifically leading
// zeros (e.g. "007", "00" but not "0" or "0.5") or scientific notation.
func hasSuspiciousNumericFormat(s string) bool {
	// Scientific notation preserves different semantic than decimal
	if strings.Contains(s, "e") || strings.Contains(s, "E") {
		return true
	}
	// Strip leading sign for leading-zero detection
	body := s
	if len(body) > 0 && (body[0] == '+' || body[0] == '-') {
		body = body[1:]
	}
	// Leading zeros: more than one character, starts with '0', and the
	// next character is a digit (not '.').
	return len(body) > 1 && body[0] == '0' && body[1] >= '0' && body[1] <= '9'
}

// findOpOutsideQuotes returns the index of the first occurrence of " op "
// (space-delimited, like the where parser) that is NOT inside a quoted
// region, or -1. Scanning inside quotes previously split conditions like
// `title contains 'big > small'` at the '>' inside the quoted value.
func findOpOutsideQuotes(s, op string) int {
	pattern := " " + op + " "
	inQuote := byte(0)
	for i := 0; i+len(pattern) <= len(s); i++ {
		c := s[i]
		if c == '\'' || c == '"' {
			if inQuote == 0 {
				inQuote = c
			} else if inQuote == c {
				inQuote = 0
			}
			continue
		}
		if inQuote == 0 && s[i:i+len(pattern)] == pattern {
			return i
		}
	}
	return -1
}

// EvaluateCondition evaluates an "if:" condition expression against an event payload.
// Returns true if the condition holds, or if cond is empty.
func EvaluateCondition(cond string, eventName string, payload map[string]interface{}) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}

	// Remove outer quotes if present — but ONLY when the wrapper quote char
	// does not appear inside. The manifest parser only unquotes double
	// quotes, so a single-quoted whole condition ('status == "active"') needs
	// its wrapper stripped here; but "'abc' > '100'" has BOTH operands using
	// the wrapper char — stripping would corrupt the condition.
	if (strings.HasPrefix(cond, `"`) && strings.HasSuffix(cond, `"`)) ||
		(strings.HasPrefix(cond, `'`) && strings.HasSuffix(cond, `'`)) {
		inner := cond[1 : len(cond)-1]
		if strings.IndexByte(inner, cond[0]) == -1 {
			cond = inner
		}
	}

	// Helper to resolve operand. quoted reports whether the operand was
	// explicitly quoted in the manifest — the author's intent to compare
	// strings (numeric comparison of non-numbers is a guard bug, not a
	// fallback).
	resolve := func(operand string) (string, bool) {
		operand = strings.TrimSpace(operand)
		if (strings.HasPrefix(operand, `"`) && strings.HasSuffix(operand, `"`)) ||
			(strings.HasPrefix(operand, `'`) && strings.HasSuffix(operand, `'`)) {
			return operand[1 : len(operand)-1], true
		}
		if strings.HasPrefix(operand, "$") {
			return ResolveVariables(operand, eventName, payload), false
		}
		return operand, false
	}

	// Operators to check in order of specificity. Single "=" is the natural
	// alias for "==" (the where: syntax uses it); it must come after "==",
	// "!=", ">=", "<=" so multi-char operators win.
	ops := []string{"==", "!=", ">=", "<=", "=", ">", "<", "contains", "exists"}

	for _, op := range ops {
		if op == "exists" {
			if strings.HasSuffix(cond, " exists") {
				fieldRef := strings.TrimSuffix(cond, " exists")
				val, _ := resolve(fieldRef)
				return val != "" && val != "<nil>"
			}
			continue
		}

		idx := findOpOutsideQuotes(cond, op)
		if idx != -1 {
			leftStr, leftQuoted := resolve(cond[:idx])
			rightStr, rightQuoted := resolve(cond[idx+len(op)+2:])

			switch op {
			case "==", "=":
				return equalsValue(leftStr, rightStr)
			case "!=":
				return !equalsValue(leftStr, rightStr)
			case "contains":
				return strings.Contains(leftStr, rightStr)
			case ">", ">=", "<", "<=":
				leftNum, err1 := strconv.ParseFloat(strings.TrimSpace(leftStr), 64)
				rightNum, err2 := strconv.ParseFloat(strings.TrimSpace(rightStr), 64)
				if err1 == nil && err2 == nil {
					switch op {
					case ">":
						return leftNum > rightNum
					case ">=":
						return leftNum >= rightNum
					case "<":
						return leftNum < rightNum
					case "<=":
						return leftNum <= rightNum
					}
				}
				// Only compare as strings when the manifest author explicitly
				// quoted BOTH operands. A numeric comparison between
				// non-numeric operands was previously a silent lexicographic
				// fallback ("abc" > "100" → true) — a guard bug, not intent.
				if leftQuoted && rightQuoted {
					switch op {
					case ">":
						return leftStr > rightStr
					case ">=":
						return leftStr >= rightStr
					case "<":
						return leftStr < rightStr
					case "<=":
						return leftStr <= rightStr
					}
				}
				log.Printf("[cond] comparison '%s' between non-numeric operands — treating as false: left=%q right=%q (quote both operands to compare as strings)", op, leftStr, rightStr)
				return false
			}
		}
	}

	// Default: if non-empty boolean expression string, check if truthy
	val, _ := resolve(cond)
	return val == "true" || val == "1"
}
