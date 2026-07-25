package engine

import (
	"strconv"
	"strings"
)

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
				return leftStr == rightStr
			case "!=":
				return leftStr != rightStr
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
