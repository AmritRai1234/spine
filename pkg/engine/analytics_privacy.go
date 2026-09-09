package engine

// Analytics privacy primitives (Task 4): salted daily-rotating IP hash,
// user-agent → device classification, bot filtering.
//
// Design notes:
//
//   - The salt lives in ANALYTICS_IP_SALT (env). If unset, a per-process
//     random salt is generated at startup, so a deployment without the env
//     var never emits identical hashes across restarts (and therefore
//     never enables cross-restart IP correlation by accident).
//   - Rotation: hash = SHA-256(salt + YYYYMMDD + ip)[0:16]. The date
//     component rotates daily with zero infrastructure; the operator can
//     force-immediate rotation by changing ANALYTICS_IP_SALT.
//   - TRADE-OFF (intentional): because the hash rotates daily, the same
//     real IP produces a different hash each day. "Unique visitors by
//     ip_hash" is therefore NOT meaningful across day boundaries — all
//     unique-visitor metrics must key off visitor_id (the client's
//     first-party localStorage UUID), never ip_hash. ip_hash exists solely
//     for coarse abuse/flood forensics within a single day and for the
//     approximate conversion join (Task 6, also time-windowed).
//   - ip_hash is truncated to 16 hex chars (64 bits): enough entropy to
//     distinguish visitors within a day, not enough to brute-force back
//     to the IP offline without the salt.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	analyticsSaltOnce sync.Once
	analyticsSalt     string
)

// analyticsIPSalt returns the configured-or-generated salt. sync.Once so
// the fallback is generated once per process, not per request.
func analyticsIPSalt() string {
	analyticsSaltOnce.Do(func() {
		analyticsSalt = os.Getenv("ANALYTICS_IP_SALT")
		if analyticsSalt == "" {
			// No operator salt: generate 32 random bytes so hashes are
			// still keyed (fail-closed for privacy, not identical across
			// restarts).
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				// crypto/rand failing is catastrophic anyway; a fixed
				// fallback beats a panic in the request path.
				analyticsSalt = "spine-fallback-salt"
			} else {
				analyticsSalt = hex.EncodeToString(buf)
			}
		}
	})
	return analyticsSalt
}

// hashIP produces the daily-rotating salted hash stored in ip_hash.
func hashIP(ip string, day string) string {
	h := sha256.Sum256([]byte(analyticsIPSalt() + day + ip))
	return hex.EncodeToString(h[:8]) // 16 hex chars = 64 bits
}

// analyticsDay is the date component of the rotation. Separated for
// testability (inject a fixed day in tests).
func analyticsDay(t time.Time) string {
	return t.UTC().Format("20060102")
}

// classifyDevice maps a parsed user agent to a coarse device family.
// Returns "" for empty UAs (filled by the caller) and for known bots,
// which are filtered rather than stored (bot traffic must not pollute
// the behavior analytics the heatmap is built from).
func classifyDevice(ua string) string {
	l := strings.ToLower(ua)
	if l == "" {
		return ""
	}
	// Bots first: a bot matching "mobile" must still be filtered.
	for _, marker := range []string{"bot", "crawl", "spider", "slurp", "headless", "lighthouse", "pingdom", "uptime", "curl/", "wget", "python-requests", "go-http-client"} {
		if strings.Contains(l, marker) {
			return ""
		}
	}
	if strings.Contains(l, "ipad") || strings.Contains(l, "tablet") {
		return "tablet"
	}
	if strings.Contains(l, "mobile") || strings.Contains(l, "iphone") || strings.Contains(l, "android") {
		return "mobile"
	}
	return "desktop"
}

// resolveTrackContext fills the server-side fields of each event batch:
// ip_hash (salted, daily-rotating), user_agent family, device. The client
// never sends these — trust boundary.
func (e *Engine) resolveTrackContext(r *http.Request, events []trackEvent) {
	var ip, ua string
	if e.trackLimiter != nil {
		// Reuse the rate limiter's ExtractIP: trusted-proxy aware
		// (X-Forwarded-For honored only from trusted proxies).
		ip = e.trackLimiter.ExtractIP(r)
	} else {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip = host
	}
	ua = r.UserAgent()
	device := classifyDevice(ua)
	day := analyticsDay(time.Now())
	ipHash := ""
	if device != "" {
		// Bots get no ip_hash at all — nothing to correlate.
		ipHash = hashIP(ip, day)
	}
	for i := range events {
		events[i].IPHash = ipHash
		events[i].UserAgent = uaFamily(ua)
		events[i].Device = device
		events[i].Country = geoCountryHeader(r)
	}
}

// uaFamily reduces the UA string to a stable browser family token
// (e.g. "firefox", "chrome", "safari", "edge"). We keep the family only —
// full UA strings are fingerprinting surfaces; the family is all the
// dashboard needs.
func uaFamily(ua string) string {
	if ua == "" {
		return ""
	}
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "edg/"), strings.Contains(l, "edge/"):
		return "edge"
	case strings.Contains(l, "opr/") || strings.Contains(l, "opera"):
		return "opera"
	case strings.Contains(l, "chrome") && !strings.Contains(l, "chromium"):
		return "chrome"
	case strings.Contains(l, "firefox"):
		return "firefox"
	case strings.Contains(l, "safari"):
		return "safari"
	default:
		return "other"
	}
}

// geoCountryHeader reads a country from CDN geo headers when present
// (Cloudflare/Vercel-style). Empty string when absent — the store is
// Canada-only anyway, this is informational.
func geoCountryHeader(r *http.Request) string {
	for _, h := range []string{"CF-IPCountry", "X-Vercel-IP-Country"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return ""
}
