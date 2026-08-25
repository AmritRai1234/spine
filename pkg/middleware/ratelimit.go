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

// Number of shards for the rate limiter — reduces mutex contention under high concurrency.
const rateLimitShards = 64

// rateLimiterShard holds a mutex-guarded map for a subset of IPs.
type rateLimiterShard struct {
	mu       sync.Mutex
	limiters map[string]*rateLimiter
}

// RateLimitManager manages IP-based rate limiting buckets with sharded locking
// and trusted proxy validation.
type RateLimitManager struct {
	shards         [rateLimitShards]rateLimiterShard
	limit          float64
	burst          float64
	trustedMu      sync.RWMutex
	trustedProxies map[string]bool
	stopCh         chan struct{}
}

// NewRateLimitManager creates a rate limit manager allowing `rps` requests/sec with `burst` capacity.
// Starts a background eviction goroutine that removes stale IP entries every 60 seconds.
func NewRateLimitManager(rps float64, burst float64) *RateLimitManager {
	m := &RateLimitManager{
		limit:          rps,
		burst:          burst,
		trustedProxies: make(map[string]bool),
		stopCh:         make(chan struct{}),
	}
	for i := range m.shards {
		m.shards[i].limiters = make(map[string]*rateLimiter)
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

// shardFor returns the shard index for a given IP using FNV-1a hash.
func shardFor(ip string) int {
	h := uint32(2166136261)
	for i := 0; i < len(ip); i++ {
		h ^= uint32(ip[i])
		h *= 16777619
	}
	return int(h % rateLimitShards)
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
			for i := range m.shards {
				m.shards[i].mu.Lock()
				for ip, lim := range m.shards[i].limiters {
					if now.Sub(lim.lastRefill) > staleDuration {
						delete(m.shards[i].limiters, ip)
					}
				}
				m.shards[i].mu.Unlock()
			}
		}
	}
}

// SetTrustedProxies configures trusted reverse proxy IPs (e.g., "127.0.0.1", "10.0.0.1").
// Use "*" to trust all upstream proxies.
func (m *RateLimitManager) SetTrustedProxies(proxies []string) {
	m.trustedMu.Lock()
	defer m.trustedMu.Unlock()
	m.trustedProxies = make(map[string]bool)
	for _, p := range proxies {
		m.trustedProxies[strings.TrimSpace(p)] = true
	}
}

// Allow checks whether a request from the given client IP is permitted.
// Uses sharded locking to reduce contention under high concurrency.
func (m *RateLimitManager) Allow(ip string) bool {
	idx := shardFor(ip)
	shard := &m.shards[idx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	lim, exists := shard.limiters[ip]
	now := time.Now()

	if !exists {
		shard.limiters[ip] = &rateLimiter{
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

	m.trustedMu.RLock()
	trustAll := m.trustedProxies["*"]
	isTrusted := trustAll || m.trustedProxies[remoteIP]
	m.trustedMu.RUnlock()

	// If request comes from a trusted proxy (or trust mode is wildcard), parse proxy headers.
	// Proxy headers are NEVER honored when no trusted proxies are configured:
	// X-Forwarded-For is trivially client-spoofable (MDN: security uses of
	// X-Forwarded-For "must only use IP addresses added by a trusted proxy"),
	// and honoring it by default lets anyone bypass or weaponize rate limits.
	if isTrusted {
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
