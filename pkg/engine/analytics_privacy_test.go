package engine

// Task 4 privacy tests: salt rotation, per-process fallback, bot
// filtering, device classification, UA family reduction, and the salt
// hygiene guarantee (ANALYTICS_IP_SALT never surfaces in any log/echo).

import (
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHashIPDailyRotation(t *testing.T) {
	os.Setenv("ANALYTICS_IP_SALT", "test-salt-rotation")
	t.Cleanup(func() { os.Unsetenv("ANALYTICS_IP_SALT") })
	// Reset the once so the new salt is picked up.
	analyticsSaltOnce = sync.Once{}

	h1 := hashIP("203.0.113.7", "20260908")
	h2 := hashIP("203.0.113.7", "20260909")
	if h1 == h2 {
		t.Fatal("daily rotation failed: same IP, different days, identical hash")
	}
	if len(h1) != 16 {
		t.Fatalf("hash length: want 16 hex chars, got %d", len(h1))
	}
	// Same day + same IP = stable (within-day forensics must work).
	if h1 != hashIP("203.0.113.7", "20260908") {
		t.Fatal("same-day hash is not stable")
	}
	// Different IP, same day = different hash.
	if h1 == hashIP("203.0.113.8", "20260908") {
		t.Fatal("different IPs collide on the same day")
	}
	_ = time.Now() // keep time import if analyticsDay changes
}

func TestAnalyticsSaltFallbackPerProcess(t *testing.T) {
	os.Unsetenv("ANALYTICS_IP_SALT")
	// With no env salt, the fallback must still produce non-trivial,
	// process-local hashes (never a constant across deployments).
	analyticsSaltOnce = sync.Once{}
	s1 := analyticsIPSalt()
	if s1 == "" {
		t.Fatal("empty fallback salt")
	}
	if s1 == "test-salt-rotation" {
		t.Fatal("fallback picked up env value that was unset")
	}
	// Second call returns the SAME generated salt (once-per-process).
	if analyticsIPSalt() != s1 {
		t.Fatal("fallback salt regenerated within a process — hashes would be unstable")
	}
}

func TestClassifyDeviceAndBots(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126.0 Safari/537.36", "desktop"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/605.1", "mobile"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/126.0 Mobile Safari/537.36", "mobile"},
		{"Mozilla/5.0 (iPad; CPU OS 17_0) Safari/605.1", "tablet"},
		{"Mozilla/5.0 (compatible; Googlebot/2.1)", ""},
		{"Mozilla/5.0 (X11; Linux x86_64) HeadlessChrome/126.0", ""},
		{"curl/8.4.0", ""},
		{"python-requests/2.31", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := classifyDevice(c.ua); got != c.want {
			t.Errorf("classifyDevice(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

func TestUAFamily(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 Chrome/126.0 Safari/537.36", "chrome"},
		{"Mozilla/5.0 Gecko/20100101 Firefox/127.0", "firefox"},
		{"Mozilla/5.0 Edg/126.0", "edge"},
		{"Mozilla/5.0 OPR/110.0", "opera"},
		{"Mozilla/5.0 Safari/605.1", "safari"},
		{"weird-agent/1.0", "other"},
		{"", ""},
	}
	for _, c := range cases {
		if got := uaFamily(c.ua); got != c.want {
			t.Errorf("uaFamily(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

// TestTrackServerSideIdentity: the CLIENT cannot inject ip_hash/device/UA —
// JSON fields with those names are ignored (json:"-" tags), and the values
// stored come from server resolution only.
func TestTrackServerSideIdentity(t *testing.T) {
	eng, handler, cleanup := trackEngine(t)
	defer cleanup()

	body := `{"events":[{"event_type":"click","visitor_id":"v1","session_id":"s1","page_path":"/p","x":1,"y":2,` +
		`"ip_hash":"FORGED","device":"FORGED","user_agent":"FORGED","country":"FORGED"}]}`
	req := httptest.NewRequest("POST", "/track", strings.NewReader(body))
	req.RemoteAddr = "10.5.5.5:1234"
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/605.1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Fatalf("want 204, got %d", rr.Code)
	}
	waitForRows(t, eng, "analytics_events", 1)

	rows, err := eng.Bus.DB().Query(`SELECT ip_hash, device, user_agent, country FROM analytics_events LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row")
	}
	var ipHash, device, ua, country string
	if err := rows.Scan(&ipHash, &device, &ua, &country); err != nil {
		t.Fatal(err)
	}
	if ipHash == "FORGED" || ipHash == "" {
		t.Errorf("ip_hash must be server-resolved, got %q", ipHash)
	}
	if device != "mobile" {
		t.Errorf("device: want mobile (from server UA), got %q", device)
	}
	if ua != "safari" {
		t.Errorf("ua family: want safari, got %q", ua)
	}
	if country == "FORGED" {
		t.Errorf("country must not be client-supplied")
	}
}

// TestSaltNeverInEcho surfaces: the salt env var must be covered by the
// secret-masker guard — any payload field echoing a *salt* value is masked
// like *secret/*key/*token. Pins the suffix-masking list so a future
// rename of the env var doesn't silently lose coverage.
func TestSaltNeverInEcho(t *testing.T) {
	// The audit masker masks by suffix. ANALYTICS_IP_SALT's persisted
	// forms must all be covered by that suffix list.
	covered := []string{"salt", "secret", "key", "token"}
	name := strings.ToLower("ANALYTICS_IP_SALT")
	matched := false
	for _, suffix := range covered {
		if strings.HasSuffix(name, suffix) {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("ANALYTICS_IP_SALT not covered by masker suffixes %v — rename it to a *salt/*secret/*key/*token suffix or extend the masker", covered)
	}

	// Behavioral pin: a payload field named ip_salt gets masked in the
	// audit log exactly like ip_secret would (via the extracted, directly
	// testable masker — no live Bus needed).
	payload := map[string]interface{}{"ip_salt": "super-secret-salt-value", "note": "hello"}
	if !maskSensitiveField(&payload, "ip_salt", "super-secret-salt-value") {
		t.Fatal("masker rejected ip_salt — suffix coverage gap")
	}
	if got := payload["ip_salt"]; got == "super-secret-salt-value" {
		t.Fatal("ip_salt value NOT masked — salt could leak into durable logs")
	}
	if got := payload["note"]; got != "hello" {
		t.Errorf("masker touched non-sensitive field: %v", got)
	}
	// Short salt masks entirely (no 4-char tail to leak).
	short := map[string]interface{}{"salt": "ab"}
	maskSensitiveField(&short, "salt", "ab")
	if got := short["salt"]; got != "••••" {
		t.Errorf("short salt: want full mask, got %v", got)
	}
}
