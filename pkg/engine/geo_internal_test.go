package engine

import (
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// The action must exist, validate format, FSA deliverability, and the
// postal↔city cross-check — with shopper-safe error messages.

func geoStep(config map[string]string) *geoStepBuilder { return &geoStepBuilder{cfg: config} }

type geoStepBuilder struct{ cfg map[string]string }

func (g *geoStepBuilder) run(payload map[string]interface{}) error {
	b := &Bus{}
	step := &manifest.RouteStep{Action: "geo.check_address", Config: g.cfg}
	return b.geoCheckAddress(step, "PLACE_ORDER", payload)
}

func TestGeoCheckAddress_ValidKingston(t *testing.T) {
	cfg := map[string]string{
		"postal":   "$event.payload.zip",
		"city":     "$event.payload.city",
		"province": "$event.payload.province",
	}
	payload := map[string]interface{}{"zip": "K7L 1A4", "city": "Kingston", "province": "ON"}
	if err := geoStep(cfg).run(payload); err != nil {
		t.Fatalf("valid Kingston address rejected: %v", err)
	}
	// Lowercase + no space also fine
	payload["zip"] = "k7l1a4"
	if err := geoStep(cfg).run(payload); err != nil {
		t.Fatalf("lowercase/no-space postal rejected: %v", err)
	}
}

func TestGeoCheckAddress_BadFormat(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip"}
	for _, bad := range []string{"12345", "K7L 11A", "D7L 1A4", "K7L 1A", "hello"} {
		if err := geoStep(cfg).run(map[string]interface{}{"zip": bad}); err == nil {
			t.Fatalf("format %q accepted", bad)
		}
	}
}

func TestGeoCheckAddress_UndeliverableFSA(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip"}
	// X0A covers Nunavut post-office-box-only routes; X1B does not exist at all.
	err := geoStep(cfg).run(map[string]interface{}{"zip": "X1B 0A0"})
	if err == nil {
		t.Fatal("nonexistent FSA accepted")
	}
	if !strings.Contains(err.Error(), "deliverable") {
		t.Fatalf("error should mention deliverability: %v", err)
	}
}

func TestGeoCheckAddress_CityMismatchSuggests(t *testing.T) {
	cfg := map[string]string{
		"postal":   "$event.payload.zip",
		"city":     "$event.payload.city",
		"province": "$event.payload.province",
	}
	payload := map[string]interface{}{"zip": "K7L 1A4", "city": "Belleville", "province": "ON"}
	err := geoStep(cfg).run(payload)
	if err == nil {
		t.Fatal("postal/city mismatch accepted")
	}
	if !strings.Contains(err.Error(), "Kingston") {
		t.Fatalf("mismatch error should suggest Kingston: %v", err)
	}
}

func TestGeoCheckAddress_MissingPostalFailsLoud(t *testing.T) {
	cfg := map[string]string{"postal": "$event.payload.zip"}
	err := geoStep(cfg).run(map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing postal should fail loudly: %v", err)
	}
}

func TestGeoCheckAddress_EmptyCitySkipsCrossCheck(t *testing.T) {
	// International orders skip via if-guards in the manifest, but an empty
	// city in payload must not crash the cross-check either.
	cfg := map[string]string{
		"postal":   "$event.payload.zip",
		"city":     "$event.payload.city",
		"province": "$event.payload.province",
	}
	payload := map[string]interface{}{"zip": "K7L 1A4", "city": "", "province": ""}
	if err := geoStep(cfg).run(payload); err != nil {
		t.Fatalf("empty city should skip cross-check: %v", err)
	}
}
