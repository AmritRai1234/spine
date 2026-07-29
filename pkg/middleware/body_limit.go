package middleware

import (
	"encoding/json"
	"net/http"
)

// BodyLimitMiddleware restricts the maximum size (in bytes) of an incoming HTTP request body.
// Rejects oversized payloads with HTTP 413 (Payload Too Large).
func BodyLimitMiddleware(maxBytes int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if maxBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}

		next(w, r)
	}
}

// WritePayloadTooLarge returns a standardized 413 error response when payload exceeds limit.
func WritePayloadTooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "error",
		"error":  "payload_too_large: request body exceeds maximum allowed size",
	})
}
