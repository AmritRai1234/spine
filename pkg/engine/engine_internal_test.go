package engine

import (
	"strings"
	"testing"
)

// TestMaxBodyBytesFromEnv pins the SPINE_MAX_BODY_BYTES parsing: valid
// positive values win, everything else (empty, garbage, zero, negative)
// falls back to the 1 MB fail-closed default.
func TestMaxBodyBytesFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int64
	}{
		{"", 1 << 20},
		{"4194304", 4 << 20},
		{"1024", 1024},
		{"not-a-number", 1 << 20},
		{"0", 1 << 20},
		{"-5", 1 << 20},
	}
	for _, c := range cases {
		if got := maxBodyBytesFromEnv(c.env); got != c.want {
			t.Errorf("maxBodyBytesFromEnv(%q) = %d, want %d", c.env, got, c.want)
		}
	}
}

// TestACMERejectsWildcardDomains pins the fail-loud behavior for wildcard
// ACME domains: autocert.HostWhitelist matches exact hostnames only, so a
// "*.example.com" entry would silently fail for every subdomain.
func TestACMERejectsWildcardDomains(t *testing.T) {
	_, err := newACMEManager(&TLSConfig{Domains: []string{"*.example.com"}})
	if err == nil {
		t.Fatal("wildcard ACME domain must be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("expected wildcard rejection message, got: %v", err)
	}

	// Exact domains still work.
	m, err := newACMEManager(&TLSConfig{Domains: []string{"shop.example.com"}})
	if err != nil {
		t.Fatalf("exact domain must be accepted, got: %v", err)
	}
	if m == nil {
		t.Fatal("expected a manager for exact domains")
	}
}
