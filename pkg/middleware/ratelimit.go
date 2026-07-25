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

// RateLimitManager manages IP-based rate limiting buckets with trusted proxy validation.
type RateLimitManager struct {
	mu             sync.Mutex
	limiters       map[string]*rateLimiter
	limit          float64
	burst          float64
	trustedProxies map[string]bool
	stopCh         chan struct{}
}

// NewRateLimitManager creates a rate limit manager allowing `rps` requests/sec with `burst` capacity.
// Starts a background eviction goroutine that removes stale IP entries every 60 seconds.
func NewRateLimitManager(rps float64, burst float64) *RateLimitManager {
	m := &RateLimitManager{
		limiters:       make(map[string]*rateLimiter),
		limit:          rps,
		burst:          burst,
		trustedProxies: make(map[string]bool),
		stopCh:         make(chan struct{}),
	}
	go m.evictStaleEntries()
	return m
}

// Close stops the background eviction goroutine.
func (m *RateLimitManager) Close() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// evictStaleEntries removes rate limiter entries that haven't been used in 5 minutes.
// Runs every 60 seconds to prevent unbounded memory growth from unique IP addresses.
func (m *RateLimitManager) evictStaleEntries() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	const staleDuration = 5 * time.Minute

	for {
		select {
		case <-m.stopCh:
			return
		case now := <-ticker.C:
			m.mu.Lock()
			for ip, lim := range m.limiters {
				if now.Sub(lim.lastRefill) > staleDuration {
					delete(m.limiters, ip)
				}
			}
			m.mu.Unlock()
		}
	}
}

// SetTrustedProxies configures trusted reverse proxy IPs (e.g., "127.0.0.1", "10.0.0.1").
// Use "*" to trust all upstream proxies.
func (m *RateLimitManager) SetTrustedProxies(proxies []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustedProxies = make(map[string]bool)
	for _, p := range proxies {
		m.trustedProxies[strings.TrimSpace(p)] = true
	}
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

// ExtractIP extracts the client IP address, validating X-Forwarded-For headers against trusted proxies.
func (m *RateLimitManager) ExtractIP(r *http.Request) string {
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	m.mu.Lock()
	trustAll := m.trustedProxies["*"]
	isTrusted := trustAll || m.trustedProxies[remoteIP]
	m.mu.Unlock()

	// If request comes from a trusted proxy (or trust mode is wildcard), parse proxy headers
	if isTrusted || len(m.trustedProxies) == 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			clientIP := strings.TrimSpace(parts[0])
			if clientIP != "" {
				return clientIP
			}
		} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
			clientIP := strings.TrimSpace(xri)
			if clientIP != "" {
				return clientIP
			}
		}
	}

	return remoteIP
}

// Middleware returns an HTTP handler middleware enforcing IP rate limits.
func (m *RateLimitManager) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := m.ExtractIP(r)

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
