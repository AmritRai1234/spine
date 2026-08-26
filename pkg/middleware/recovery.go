package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
)

// RecoveryMiddleware wraps an HTTP handler to recover from panics, log the stack trace,
// and return a generic JSON 500 Internal Server Error without crashing the server.
// The panic value is logged server-side ONLY — echoing it to the client would
// disclose internal paths, SQL fragments, or other internals to (potentially
// unauthenticated) callers.
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
				})
			}
		}()

		next(w, r)
	}
}
