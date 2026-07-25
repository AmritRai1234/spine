package engine

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"time"
)

// generateUUID generates a standard v4 UUID string.
func generateUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}

// resolvePath extracts nested values from map[string]interface{}.
func resolvePath(data map[string]interface{}, path string) (interface{}, bool) {
	if path == "" {
		return data, true
	}
	parts := strings.Split(path, ".")
	var current interface{} = data

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		val, exists := m[part]
		if !exists {
			return nil, false
		}
		current = val
	}
	return current, true
}

// ResolveVariables evaluates template expressions in a string.
// Supported expressions:
// - $now -> current UTC ISO timestamp
// - $uuid -> random UUID v4
// - $event.name -> event name
// - $env.KEY -> environment variable
// - $event.payload[.path] -> field from payload
func ResolveVariables(input string, eventName string, payload map[string]interface{}) string {
	if input == "" {
		return ""
	}

	// Fast-path: no variable token in input string
	if strings.IndexByte(input, '$') == -1 {
		return input
	}

	// Exact string replacements for standalone tokens
	if input == "$now" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	if input == "$uuid" {
		return generateUUID()
	}
	if input == "$event.name" {
		return eventName
	}
	if input == "$event.payload" {
		// Return raw or JSON if needed, handled by caller
		return input
	}

	// Replace tokens within larger string
	// Check for $env.VAR
	var res strings.Builder
	idx := 0
	for idx < len(input) {
		if input[idx] == '$' {
			// Find token boundary (space, comma, quote, or end)
			end := idx + 1
			for end < len(input) && (input[end] == '_' || input[end] == '.' || (input[end] >= 'a' && input[end] <= 'z') || (input[end] >= 'A' && input[end] <= 'Z') || (input[end] >= '0' && input[end] <= '9')) {
				end++
			}
			token := input[idx:end]
			switch {
			case token == "$now":
				res.WriteString(time.Now().UTC().Format(time.RFC3339))
				idx = end
			case token == "$uuid":
				res.WriteString(generateUUID())
				idx = end
			case token == "$event.name":
				res.WriteString(eventName)
				idx = end
			case strings.HasPrefix(token, "$env."):
				envKey := token[5:]
				res.WriteString(os.Getenv(envKey))
				idx = end
			case strings.HasPrefix(token, "$event.payload."):
				path := token[15:]
				if val, ok := resolvePath(payload, path); ok {
					res.WriteString(fmt.Sprintf("%v", val))
				}
				idx = end
			default:
				res.WriteString(token)
				idx = end
			}
		} else {
			res.WriteByte(input[idx])
			idx++
		}
	}

	return res.String()
}

// ResolveValue resolves a value from payload or variable expression for SQL insertion/execution.
func ResolveValue(expr string, eventName string, payload map[string]interface{}) interface{} {
	if expr == "$now" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	if expr == "$uuid" {
		return generateUUID()
	}
	if expr == "$event.name" {
		return eventName
	}
	if strings.HasPrefix(expr, "$env.") {
		return os.Getenv(expr[5:])
	}
	if strings.HasPrefix(expr, "$event.payload.") {
		path := expr[15:]
		if val, ok := resolvePath(payload, path); ok {
			return val
		}
		return nil
	}
	return ResolveVariables(expr, eventName, payload)
}
