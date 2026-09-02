package middleware

import (
	"net/http"
	"os"
	"strings"
)

// DefaultCSP is the baseline Content-Security-Policy for the engine's own
// surface (API + admin SPA). It assumes the Vite-built SPA: bundled scripts
// only (no inline <script>, no eval — verified against web/src), Google
// Fonts for typography, and data: URLs for product images.
//
//	script-src 'self'          — the load-bearing directive: no inline script,
//	                             no eval → stored XSS cannot execute
//	style-src … 'unsafe-inline'— React style= attributes need it; style
//	                             injection is cosmetic, not code execution
//	font-src fonts.gstatic.com — font files behind the Google Fonts CSS
//	img-src  'self' data:      — data-URL product images are a feature
//	connect-src 'self'         — SPA talks to same-origin /emit /tables /ws
//	frame-ancestors 'none'     — CSP-level frame busting (XFO is legacy)
//	base-uri 'self'            — blocks <base> hijack of relative URLs
//	form-action 'self'         — forms can't be pointed off-origin
const DefaultCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// cspPolicy composes the effective CSP for a request. SPINE_CSP_EXTRA
// (a directive source, e.g. "script-src https://widget.example.com") is
// appended so operators can open a directive for legitimate third-party
// embeds without rebuilding. Read at request time (env-at-request-time
// law) so tests and hot reconfiguration apply without restart.
//
// The extra source is validated, not just scrubbed: control characters,
// quotes, and semicolons can't break out of the header, and an extra that
// contains anything outside the URL/keyword token alphabet is rejected
// outright — falling back to the untouched baseline. A stricter allowlist
// beats a smarter scrubber: operators adding embeds use URLs.
func cspPolicy() string {
	extra := strings.TrimSpace(os.Getenv("SPINE_CSP_EXTRA"))
	if extra == "" {
		return DefaultCSP
	}
	for _, r := range extra {
		allowed := r == ' ' || r == '-' || r == '.' || r == ':' || r == '/' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !allowed {
			return DefaultCSP // hostile or malformed — ignore entirely
		}
	}
	// Reject policy-disabling tokens even in safe alphabet.
	if extra == "'none'" || strings.Contains(extra, "default-src") {
		return DefaultCSP
	}
	return DefaultCSP + " " + extra
}

// SecurityHeadersMiddleware sets standard security headers on HTTP responses.
func SecurityHeadersMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", cspPolicy())
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		next(w, r)
	}
}
