package middleware

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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

// DefaultCORSOptions returns production defaults for CORS.
//
// Credentials are OFF by default with the wildcard origin. Setting
// SPINE_CORS_ORIGINS (comma-separated explicit origins) switches to
// credentialed cross-origin with an allowlist — the only safe combination,
// since reflecting an arbitrary Origin with Access-Control-Allow-Credentials
// lets any website read authenticated responses (the rs/cors security fix,
// #55/#57).
func DefaultCORSOptions() CORSOptions {
	opts := CORSOptions{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-API-Key", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           86400, // 24 hours
	}

	if env := os.Getenv("SPINE_CORS_ORIGINS"); env != "" {
		opts.AllowedOrigins = nil
		for _, o := range strings.Split(env, ",") {
			if o = strings.TrimSpace(o); o != "" {
				opts.AllowedOrigins = append(opts.AllowedOrigins, o)
			}
		}
		opts.AllowCredentials = true
	}
	return opts
}

// DynamicCORSMiddleware wraps CORSMiddleware so SPINE_CORS_ORIGINS is
// re-read per request (L6): hot-reloaded env changes take effect without a
// restart, matching WS origins semantics. The handler is rebuilt only when
// the allowlist actually changes, so the per-request cost is one env read
// + string compare.
func DynamicCORSMiddleware(next http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	lastEnv := "\x00" // sentinel ≠ "" so the first request builds
	wrapped := CORSMiddleware(DefaultCORSOptions(), next)
	return func(w http.ResponseWriter, r *http.Request) {
		env := os.Getenv("SPINE_CORS_ORIGINS")
		if env != lastEnv {
			mu.Lock()
			if env != lastEnv { // double-check under lock
				wrapped = CORSMiddleware(DefaultCORSOptions(), next)
				lastEnv = env
			}
			mu.Unlock()
		}
		wrapped(w, r)
	}
}

// CORSMiddleware wraps an HTTP handler with CORS headers and preflight (OPTIONS) resolution.
func CORSMiddleware(opts CORSOptions, next http.HandlerFunc) http.HandlerFunc {
	methodsStr := strings.Join(opts.AllowedMethods, ", ")
	headersStr := strings.Join(opts.AllowedHeaders, ", ")
	exposedStr := strings.Join(opts.ExposedHeaders, ", ")
	maxAgeStr := strconv.Itoa(opts.MaxAge) // decimal string, not rune-encoded

	explicitlyAllowed := func(origin string) bool {
		for _, o := range opts.AllowedOrigins {
			if strings.EqualFold(o, origin) {
				return true
			}
		}
		return false
	}

	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Vary on Origin so caches don't serve one origin's CORS headers to
		// another client.
		if origin != "" {
			w.Header().Add("Vary", "Origin")
		}

		allowed := false
		if origin == "" {
			// Non-browser request — CORS does not constrain it.
			allowed = true
		} else {
			for _, o := range opts.AllowedOrigins {
				if o == "*" || strings.EqualFold(o, origin) {
					allowed = true
					break
				}
			}
		}

		if allowed && origin != "" {
			// Credentials + wildcard: never emit ACAO for an arbitrary
			// reflected origin. Only an explicitly allowlisted origin may
			// carry credentials; anything else gets no CORS headers and the
			// browser blocks it.
			if opts.AllowCredentials && !explicitlyAllowed(origin) {
				// no Access-Control-Allow-Origin header
			} else if !opts.AllowCredentials && len(opts.AllowedOrigins) > 0 && opts.AllowedOrigins[0] == "*" {
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

		// Handle OPTIONS preflight request
		if r.Method == http.MethodOptions {
			if allowed && origin != "" {
				w.Header().Set("Access-Control-Allow-Methods", methodsStr)
				w.Header().Set("Access-Control-Allow-Headers", headersStr)
				if opts.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", maxAgeStr)
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}
