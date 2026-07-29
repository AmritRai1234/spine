package middleware

import (
	"net/http"
	"strings"
)

// CORSOptions configures Cross-Origin Resource Sharing behavior.
type CORSOptions struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           int // MaxAge in seconds for preflight caching
}

// DefaultCORSOptions returns sensible production defaults for CORS.
func DefaultCORSOptions() CORSOptions {
	return CORSOptions{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization", "X-API-Key", "X-Request-ID"},
		ExposedHeaders: []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           86400, // 24 hours
	}
}

// CORSMiddleware wraps an HTTP handler with CORS headers and preflight (OPTIONS) resolution.
func CORSMiddleware(opts CORSOptions, next http.HandlerFunc) http.HandlerFunc {
	originsStr := strings.Join(opts.AllowedOrigins, ", ")
	methodsStr := strings.Join(opts.AllowedMethods, ", ")
	headersStr := strings.Join(opts.AllowedHeaders, ", ")
	exposedStr := strings.Join(opts.ExposedHeaders, ", ")

	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Origin matching
			allowed := false
			for _, o := range opts.AllowedOrigins {
				if o == "*" || strings.EqualFold(o, origin) {
					allowed = true
					break
				}
			}

			if allowed {
				if opts.AllowedOrigins[0] == "*" && !opts.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}

				if opts.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}

				if exposedStr != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposedStr)
				}
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", originsStr)
		}

		// Handle OPTIONS preflight request
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", methodsStr)
			w.Header().Set("Access-Control-Allow-Headers", headersStr)
			if opts.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", string(rune(opts.MaxAge)))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}
