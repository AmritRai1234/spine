package engine

import "testing"

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
