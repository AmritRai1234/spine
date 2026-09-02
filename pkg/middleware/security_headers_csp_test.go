package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cspTestHandler(next http.HandlerFunc) http.HandlerFunc {
	return SecurityHeadersMiddleware(next)
}

// TestCSPHeaderPresent asserts every wrapped response carries the baseline
// CSP and its load-bearing directives.
func TestCSPHeaderPresent(t *testing.T) {
	rr := httptest.NewRecorder()
	SecurityHeadersMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	got := rr.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Fatal("Content-Security-Policy missing from response")
	}
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
	} {
		if !strings.Contains(got, directive) {
			t.Errorf("CSP missing %q in: %s", directive, got)
		}
	}
	// The load-bearing property: no inline script, no eval.
	if strings.Contains(got, "unsafe-eval") {
		t.Errorf("CSP must not allow eval: %s", got)
	}
	if !strings.Contains(got, "script-src 'self'") || strings.Contains(got, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("script-src must be exactly 'self' (no inline): %s", got)
	}

	if pp := rr.Header().Get("Permissions-Policy"); pp == "" {
		t.Error("Permissions-Policy missing")
	}
}

// TestCSPExtraEnv appends a third-party source for legit embeds.
func TestCSPExtraEnv(t *testing.T) {
	t.Setenv("SPINE_CSP_EXTRA", "script-src https://widget.example.com")
	got := cspPolicy()
	if !strings.Contains(got, "script-src https://widget.example.com") {
		t.Errorf("SPINE_CSP_EXTRA not appended: %s", got)
	}
	// Baseline directives still present.
	if !strings.Contains(got, "default-src 'self'") {
		t.Errorf("baseline lost when extra set: %s", got)
	}
}

// TestCSPExtraEnvSanitized rejects injection attempts: control chars, quotes,
// directive-terminating semicolons, and 'none' (which would disable nothing
// but signals a misconfiguration worth ignoring rather than appending).
func TestCSPExtraEnvSanitized(t *testing.T) {
	cases := []struct {
		name string
		env  string
	}{
		{"semicolon injection", "script-src evil.com; default-src *"},
		{"quote injection", `script-src 'unsafe-eval'`},
		{"none disables", "'none'"},
		{"default-src override", "default-src *"},
		{"control chars", "script-src\nevil.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SPINE_CSP_EXTRA", c.env)
			got := cspPolicy()
			if got != DefaultCSP {
				t.Errorf("hostile SPINE_CSP_EXTRA %q changed policy:\n got: %s\nwant: %s", c.env, got, DefaultCSP)
			}
		})
	}
}
