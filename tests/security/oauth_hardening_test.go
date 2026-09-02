package security

// M2 hardening tests: the /oauth/<platform>/callback route now runs through
// the public-browser middleware chain (rate limit, security headers, logging,
// recovery) and its redirect guard is a parsed-URL host check, not a prefix
// check.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

// oauthTestEngine builds a minimal engine exposing the /oauth route.
func oauthTestEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	manifest := `spine_version: 3

database:
  tables:
    - posts

routes:
  - on: CONNECT_START
    emit: SOCIAL_AUTH
    steps:
      - action: social.connect
`
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestOAuthCallbackSecurityHeaders(t *testing.T) {
	eng := oauthTestEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/oauth/unknown/callback")
	if err != nil {
		t.Fatalf("GET /oauth: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("X-Content-Type-Options") == "" {
		t.Errorf("oauth response missing X-Content-Type-Options — middleware chain not applied")
	}
	if resp.Header.Get("Referrer-Policy") == "" {
		t.Errorf("oauth response missing Referrer-Policy — middleware chain not applied")
	}
}

func TestSafeRedirectURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool // want == accepted
	}{
		{"https://admin.example.com/social", true},
		{"http://admin.example.com/social", true},
		{"", false},
		{"   ", false},
		{"javascript:alert(1)", false},
		{"data:text/html;base64,PHNjcmlwdD4=", false},
		{"//evil.com", false},            // scheme-relative: no scheme
		{"/relative/path", false},        // no host
		{"https://evil.com@shop.example.com/x", false}, // userinfo credentials
		{"ftp://files.example.com", false},
		{"https://", false}, // no host
	}
	for _, c := range cases {
		got := engine.SafeRedirectURL(c.in)
		if c.want && got == "" {
			t.Errorf("safeRedirectURL(%q) = rejected, want accepted", c.in)
		}
		if !c.want && got != "" {
			t.Errorf("safeRedirectURL(%q) = %q, want rejected", c.in, got)
		}
	}
}

// TestOAuthCallbackUnknownPlatformShape checks the route still 400s (not 5xx)
// for a garbage callback — the middleware chain must not change semantics.
func TestOAuthCallbackUnknownPlatformShape(t *testing.T) {
	eng := oauthTestEngine(t)
	server := httptest.NewServer(eng.HTTPHandler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/oauth/facebook/callback?code=x&state=unknownstate")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("unknown state: expected 400, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("expected JSON error body, got Content-Type %q", resp.Header.Get("Content-Type"))
	}
}
