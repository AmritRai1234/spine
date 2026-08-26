package main

import (
	"strings"
	"testing"
)

// TestRedactDSN verifies that database DSNs printed to stdout/logs never
// include credentials: turso/libsql URLs embed auth tokens in the query
// string, and a boot log line would otherwise leak them to stdout/journald.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"spine.db", "spine.db"},
		{"/var/lib/spine/data.db", "/var/lib/spine/data.db"},
		{"turso://db.turso.io?authToken=super-secret-token-42", "turso://db.turso.io"},
		{"libsql://acme-db.turso.io?authToken=another-secret", "libsql://acme-db.turso.io"},
		{"turso://user:pass@db.turso.io?authToken=xyz", "turso://db.turso.io"},
		{"postgres://user:secret@localhost:5432/spine", "postgres://localhost:5432/spine"},
	}

	for _, tc := range cases {
		got := redactDSN(tc.in)
		if got != tc.want {
			t.Errorf("redactDSN(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "authToken") || strings.Contains(got, "auth_token") {
			t.Errorf("redactDSN(%q) still contains credentials: %q", tc.in, got)
		}
	}
}
