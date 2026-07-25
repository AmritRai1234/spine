package spine

import (
	"encoding/json"
	"net/http"
	"strings"
)

// AuthMiddleware wraps an HTTP handler and validates the API key if key is configured.
func AuthMiddleware(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiKey == "" {
			next(w, r)
			return
		}

		clientKey := r.Header.Get("X-API-Key")
		if clientKey == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				clientKey = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if clientKey != apiKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "unauthorized: invalid or missing API key",
			})
			return
		}

		next(w, r)
	}
}
