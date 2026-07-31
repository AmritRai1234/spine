package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CalculateJSONDepth calculates the maximum nesting depth of a JSON payload.
func CalculateJSONDepth(data []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	maxDepth := 0
	currDepth := 0

	for {
		t, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}

		switch delim := t.(type) {
		case json.Delim:
			if delim == '{' || delim == '[' {
				currDepth++
				if currDepth > maxDepth {
					maxDepth = currDepth
				}
			} else if delim == '}' || delim == ']' {
				currDepth--
			}
		}
	}
	return maxDepth, nil
}

// DepthLimitMiddleware limits JSON request payload nesting depth to prevent stack overflow / JSON bomb DOS attacks.
func DepthLimitMiddleware(maxAllowedDepth int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if r.Body != nil {
				bodyBytes, err := io.ReadAll(r.Body)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{
						"status": "error",
						"error":  "invalid_request_body",
					})
					return
				}
				// Restore body for downstream handlers
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

				if len(bodyBytes) > 0 {
					depth, err := CalculateJSONDepth(bodyBytes)
					if err == nil && depth > maxAllowedDepth {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						json.NewEncoder(w).Encode(map[string]string{
							"status": "error",
							"error":  fmt.Sprintf("payload depth limit exceeded: depth %d exceeds max allowed depth %d", depth, maxAllowedDepth),
						})
						return
					}
				}
			}
		}
		next(w, r)
	}
}
