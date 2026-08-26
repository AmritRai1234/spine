package engine

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// hexChars is used for zero-allocation UUID hex encoding.
const hexChars = "0123456789abcdef"

// generateUUID generates a standard v4 UUID string using stack-allocated hex encoding.
// Avoids fmt.Sprintf overhead (5 intermediate []byte slices from %x formatting).
func generateUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant 10

	var buf [36]byte
	hexEncode(buf[0:8], uuid[0:4])
	buf[8] = '-'
	hexEncode(buf[9:13], uuid[4:6])
	buf[13] = '-'
	hexEncode(buf[14:18], uuid[6:8])
	buf[18] = '-'
	hexEncode(buf[19:23], uuid[8:10])
	buf[23] = '-'
	hexEncode(buf[24:36], uuid[10:16])
	return string(buf[:])
}

// hexEncode encodes src bytes into dst as lowercase hexadecimal.
func hexEncode(dst []byte, src []byte) {
	for i, b := range src {
		dst[i*2] = hexChars[b>>4]
		dst[i*2+1] = hexChars[b&0x0f]
	}
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
// $now_epoch -> current UTC unix seconds (integer string)
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
	if input == "$now_epoch" {
		return strconv.FormatInt(time.Now().UTC().Unix(), 10)
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
			case token == "$now_epoch":
				res.WriteString(strconv.FormatInt(time.Now().UTC().Unix(), 10))
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

// ResolveVariablesStrict is like ResolveVariables but returns an error when a
// $event.payload.PATH token cannot be resolved (missing payload field). Where
// clauses use it: an unresolvable value previously became '' silently, making
// "WHERE col = ''" match (or not match) rows with no indication of the bug.
func ResolveVariablesStrict(input string, eventName string, payload map[string]interface{}) (string, error) {
	if input == "" || strings.IndexByte(input, '$') == -1 {
		return input, nil
	}
	var res strings.Builder
	idx := 0
	for idx < len(input) {
		if input[idx] != '$' {
			res.WriteByte(input[idx])
			idx++
			continue
		}
		end := idx + 1
		for end < len(input) && (input[end] == '_' || input[end] == '.' || (input[end] >= 'a' && input[end] <= 'z') || (input[end] >= 'A' && input[end] <= 'Z') || (input[end] >= '0' && input[end] <= '9')) {
			end++
		}
		token := input[idx:end]
		switch {
		case token == "$now", token == "$now_epoch", token == "$uuid", token == "$event.name":
			res.WriteString(ResolveVariables(token, eventName, payload))
		case strings.HasPrefix(token, "$env."):
			res.WriteString(os.Getenv(token[5:]))
		case strings.HasPrefix(token, "$event.payload."):
			path := token[len("$event.payload."):]
			if val, ok := resolvePath(payload, path); ok {
				res.WriteString(fmt.Sprintf("%v", val))
			} else {
				return "", fmt.Errorf("unresolvable variable %q (missing payload field)", token)
			}
		default:
			res.WriteString(token)
		}
		idx = end
	}
	return res.String(), nil
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
