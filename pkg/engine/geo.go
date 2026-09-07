package engine

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// geo.go — geo.check_address engine action.
//
// Validates a Canadian delivery address offline: postal-code format (Canada
// Post A1A 1A1 spec) and FSA deliverability (does Canada Post serve this
// 3-char prefix?) plus an optional postal↔city cross-check. The dataset is
// GeoNames' open Canadian postal database (CC-BY 4.0) compiled to FSA →
// [[city, province], …] — 1,662 FSAs covering every community Canada Post
// serves. Embedded at build time; no network calls, no external API.
//
// Commerce motivation: a storefront with volunteer/UPS delivery must not
// accept orders addressed to areas no carrier serves. Client-side checks
// are convenience; this action is the server-side wall, called from
// PLACE_ORDER before any row is written.
//
// Config:
//   postal   – payload path to the postal code (e.g. $event.payload.zip)
//   city     – optional payload path; when set (with province), the
//              postal↔city match is also asserted
//   province – optional payload path (2-letter code, e.g. ON)
//
// On failure the step error carries a precise, shopper-safe message
// ("postal code doesn't look deliverable", "postal code and city don't
// match — did you mean Kingston, ON?") which routes surface through
// on_failure like any other action.

//go:embed data/ca_fsa_cities.json
var geoFSABlob embed.FS

// fsaDB maps FSA prefix (e.g. "K7L") → [[city, province], …] sorted pairs.
var fsaDB map[string][][2]string

// postalRe is the Canada Post postal-code shape: LDL DLD. The letter set
// excludes D, F, I, O, Q, U per the spec.
var postalRe = regexp.MustCompile(`^([ABCEGHJ-NPRSTV-Z]\d[ABCEGHJ-NPRSTV-Z])\s?(\d[ABCEGHJ-NPRSTV-Z]\d)$`)

func init() {
	raw, err := geoFSABlob.ReadFile("data/ca_fsa_cities.json")
	if err != nil {
		// Fail loud at process start — a build that ships without the
		// dataset would silently accept undeliverable addresses.
		panic("geo: embedded ca_fsa_cities.json missing: " + err.Error())
	}
	var rawDB map[string][][2]string
	if err := json.Unmarshal(raw, &rawDB); err != nil {
		panic("geo: corrupt ca_fsa_cities.json: " + err.Error())
	}
	fsaDB = rawDB
}

// normalizePostal uppercases and strips spaces/separator hyphens.
func normalizePostal(raw string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(raw)))
}

// geoCheckAddress implements the `geo.check_address` action.
func (b *Bus) geoCheckAddress(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	postalKey := step.Config["postal"]
	if postalKey == "" {
		return fmt.Errorf("geo.check_address requires 'postal' config")
	}
	postal := strings.TrimSpace(ResolveVariables(postalKey, eventName, payload))
	if postal == "" {
		return fmt.Errorf("postal code is required to validate the delivery address")
	}
	normalized := normalizePostal(postal)

	m := postalRe.FindStringSubmatch(normalized)
	if m == nil {
		// Echo at most a short prefix — the raw value is attacker-controlled
		// and must not flood error surfaces, logs, or emails.
		echo := postal
		if len(echo) > 12 {
			echo = echo[:12] + "…"
		}
		return fmt.Errorf("postal code %q isn't a valid Canadian postal code — expected format A1A 1A1", echo)
	}
	fsa := m[1]

	pairs, ok := fsaDB[fsa]
	if !ok || len(pairs) == 0 {
		return fmt.Errorf("postal code %s doesn't look deliverable — Canada Post doesn't serve that area. Double-check it", fsa)
	}

	// Optional city/province cross-check: only enforced when both are
	// configured AND the payload carries non-empty values. International
	// orders skip this action in the manifest via if-guards.
	cityKey := step.Config["city"]
	provKey := step.Config["province"]
	if cityKey == "" || provKey == "" {
		return nil
	}
	city := strings.TrimSpace(ResolveVariables(cityKey, eventName, payload))
	prov := strings.ToUpper(strings.TrimSpace(ResolveVariables(provKey, eventName, payload)))
	if city == "" || prov == "" {
		return nil
	}

	matched := false
	suggestions := make([]string, 0, 2)
	for _, pair := range pairs {
		name, code := pair[0], pair[1]
		if strings.EqualFold(name, city) && code == prov {
			matched = true
			break
		}
		if len(suggestions) < 2 {
			suggestions = append(suggestions, fmt.Sprintf("%s, %s", name, code))
		}
	}
	if !matched {
		if len(suggestions) > 0 {
			return fmt.Errorf("postal code and city don't match — did you mean %s?", strings.Join(suggestions, " or "))
		}
		return fmt.Errorf("postal code %s and city %q don't match — double-check the address", fsa, city)
	}
	return nil
}
