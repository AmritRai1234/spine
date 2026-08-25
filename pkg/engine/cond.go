package engine

import (
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

// EvaluateCondition evaluates an "if:" condition expression against an event payload.
// Returns true if the condition holds, or if cond is empty.
func EvaluateCondition(cond string, eventName string, payload map[string]interface{}) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}

	// Remove outer quotes if present
	if (strings.HasPrefix(cond, `"`) && strings.HasSuffix(cond, `"`)) ||
		(strings.HasPrefix(cond, `'`) && strings.HasSuffix(cond, `'`)) {
		cond = cond[1 : len(cond)-1]
	}

	// Helper to resolve operand
	resolve := func(operand string) string {
		operand = strings.TrimSpace(operand)
		if (strings.HasPrefix(operand, `"`) && strings.HasSuffix(operand, `"`)) ||
			(strings.HasPrefix(operand, `'`) && strings.HasSuffix(operand, `'`)) {
			return operand[1 : len(operand)-1]
		}
		if strings.HasPrefix(operand, "$") {
			return ResolveVariables(operand, eventName, payload)
		}
		return operand
	}

	// Operators to check in order of specificity
	ops := []string{"==", "!=", ">=", "<=", ">", "<", "contains", "exists"}

	for _, op := range ops {
		if op == "exists" {
			if strings.HasSuffix(cond, " exists") {
				fieldRef := strings.TrimSuffix(cond, " exists")
				val := resolve(fieldRef)
				return val != "" && val != "<nil>"
			}
			continue
		}

		idx := strings.Index(cond, " "+op+" ")
		if idx != -1 {
			leftStr := resolve(cond[:idx])
			rightStr := resolve(cond[idx+len(op)+2:])

			switch op {
			case "==":
				return equalsValue(leftStr, rightStr)
			case "!=":
				return !equalsValue(leftStr, rightStr)
			case "contains":
				return strings.Contains(leftStr, rightStr)
			case ">", ">=", "<", "<=":
				leftNum, err1 := strconv.ParseFloat(leftStr, 64)
				rightNum, err2 := strconv.ParseFloat(rightStr, 64)
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
				// String fallback comparison
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
		}
	}

	// Default: if non-empty boolean expression string, check if truthy
	val := resolve(cond)
	return val == "true" || val == "1"
}
