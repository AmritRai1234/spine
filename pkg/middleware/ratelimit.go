package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type rateLimiter struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// RateLimitManager manages IP-based rate limiting buckets.
type RateLimitManager struct {
	mu         sync.Mutex
	limiters   map[string]*rateLimiter
	limit      float64
	burst      float64
	cleanupTicker *time.Ticker
}

// NewRateLimitManager creates a rate limit manager allowing `rps` requests/sec with `burst` capacity.
func NewRateLimitManager(rps float64, burst float64) *RateLimitManager {
	m := &RateLimitManager{
		limiters:   make(map[string]*rateLimiter),
		limit:      rps,
		burst:      burst,
	}
	return m
}

// Allow checks whether a request from the given client IP is permitted.
func (m *RateLimitManager) Allow(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	lim, exists := m.limiters[ip]
	now := time.Now()

	if !exists {
		m.limiters[ip] = &rateLimiter{
			tokens:     m.burst - 1,
			maxTokens:  m.burst,
			refillRate: m.limit,
			lastRefill: now,
		}
		return true
	}

	elapsed := now.Sub(lim.lastRefill).Seconds()
	lim.tokens += elapsed * lim.refillRate
	if lim.tokens > lim.maxTokens {
		lim.tokens = lim.maxTokens
	}
	lim.lastRefill = now

	if lim.tokens >= 1 {
		lim.tokens -= 1
		return true
	}

	return false
}

// RateLimitMiddleware returns an HTTP handler middleware enforcing IP rate limits.
func (m *RateLimitManager) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := ""
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			ip = strings.TrimSpace(parts[0])
		} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
			ip = strings.TrimSpace(xri)
		} else {
			var err error
			ip, _, err = net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
		}

		if !m.Allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "too_many_requests: rate limit exceeded",
			})
			return
		}

		next(w, r)
	}
}
