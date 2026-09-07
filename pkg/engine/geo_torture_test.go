package engine

import (
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Torture tests for geo.check_address — hostile inputs, config misuse,
// concurrency. Complements geo_internal_test.go's happy-path coverage.

func tortureStep(config map[string]string) *geoStepBuilder { return &geoStepBuilder{cfg: config} }

func TestGeoTorture_WhitespaceAndCaseBombs(t *testing.T) {
	cfg := map[string]string{
		"postal": "$event.payload.zip", "city": "$event.payload.city", "province": "$event.payload.province",
	}
	// Leading/trailing whitespace, tabs, newlines around a valid code
	for _, zip := range []string{"  K7L 1A4  ", "\tK7L1A4\n", "k7l 1a4", "K7l-1a4"} {
		payload := map[string]interface{}{"zip": zip, "city": "kingston", "province": "on"}
		if err := tortureStep(cfg).run(payload); err != nil {
			t.Fatalf("tolerant match failed for %q: %v", zip, err)
		}
	}
}

func TestGeoTorture_InjectionAttempts(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip", "city": "$event.payload.city", "province": "$event.payload.province"}
	bombs := []string{
		"K7L 1A4'; DROP TABLE orders;--",
		"$event.payload.zip",               // recursive variable expansion
		"K7L 1A4\x00null-byte",             // null byte
		"../../../../etc/passwd",           // path traversal shape
		strings.Repeat("K", 10000),         // oversized input
		"K7L 1A4\r\nBCC: victim@example.com", // header injection shape
	}
	for _, bomb := range bombs {
		err := tortureStep(cfg).run(map[string]interface{}{"zip": bomb, "city": "Kingston", "province": "ON"})
		if err == nil {
			t.Fatalf("hostile postal accepted: %q", bomb[:min(30, len(bomb))])
		}
		// Error must not echo more than a bounded amount of input
		if len(err.Error()) > 300 {
			t.Fatalf("error message leaks unbounded input (%d chars): %.80s", len(err.Error()), err.Error())
		}
	}
}

func TestGeoTorture_NilAndWrongTypes(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip"}
	// Missing key entirely
	if err := tortureStep(cfg).run(map[string]interface{}{}); err == nil {
		t.Fatal("missing zip should fail")
	}
	// nil payload map
	if err := tortureStep(cfg).run(nil); err == nil {
		t.Fatal("nil payload should fail")
	}
	// Non-string payload values (numeric zip from a JSON number)
	if err := tortureStep(cfg).run(map[string]interface{}{"zip": 12345}); err == nil {
		t.Fatal("numeric zip should fail with format error, not panic")
	}
	// nil value under the key
	if err := tortureStep(cfg).run(map[string]interface{}{"zip": nil}); err == nil {
		t.Fatal("nil zip should fail cleanly")
	}
}

func TestGeoTorture_ConfigErrors(t *testing.T) {
	b := &Bus{}
	// Missing postal config
	step := &manifest.RouteStep{Action: "geo.check_address", Config: map[string]string{}}
	if err := b.geoCheckAddress(step, "PLACE_ORDER", map[string]interface{}{"zip": "K7L 1A4"}); err == nil {
		t.Fatal("missing postal config should error")
	}
	// Config pointing at a non-existent payload path resolves to empty → required error
	step2 := &manifest.RouteStep{Action: "geo.check_address", Config: map[string]string{"postal": "$event.payload.nope"}}
	if err := b.geoCheckAddress(step2, "PLACE_ORDER", map[string]interface{}{"zip": "K7L 1A4"}); err == nil {
		t.Fatal("unresolvable path should yield required-error")
	}
}

func TestGeoTorture_ProvinceMismatch(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip", "city": "$event.payload.city", "province": "$event.payload.province"}
	// Kingston city name but wrong province → must NOT match (there could be
	// a Kingston in another province in the dataset)
	err := tortureStep(cfg).run(map[string]interface{}{"zip": "K7L 1A4", "city": "Kingston", "province": "BC"})
	if err == nil {
		t.Fatal("city-name match with wrong province accepted")
	}
}

func TestGeoTorture_ConcurrentAccess(t *testing.T) {
	// fsaDB is package-level and shared — exercise concurrent reads to
	// surface any data race (run with -race in CI).
	cfg := map[string]string{"postal": "$event.payload.zip", "city": "$event.payload.city", "province": "$event.payload.province"}
	done := make(chan struct{})
	for i := 0; i < 32; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			city := "Kingston"
			if n%2 == 0 {
				city = "Belleville" // guaranteed mismatch path
			}
			_ = tortureStep(cfg).run(map[string]interface{}{"zip": "K7L 1A4", "city": city, "province": "ON"})
		}(i)
	}
	for i := 0; i < 32; i++ {
		<-done
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
