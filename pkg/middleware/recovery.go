package middleware

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
)

// RecoveryMiddleware wraps an HTTP handler to recover from panics, log the stack trace,
// and return a JSON 500 Internal Server Error without crashing the server.
func RecoveryMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := string(debug.Stack())
				log.Printf("[PANIC RECOVERY] HTTP Handler Panic: %v\nStack Trace:\n%s", err, stack)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "error",
					"error":  "internal_server_error",
					"detail": fmt.Sprintf("%v", err),
				})
			}
		}()

		next(w, r)
	}
}
