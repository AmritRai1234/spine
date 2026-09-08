package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// geo_ac.go — geo.address_complete engine action.
//
// Door-level Canadian address lookup via Canada Post AddressComplete
// (the SOAP-era hosted service at ws1.postescanada-canadapost.ca).
// The store owner supplies their own AddressComplete API key — the engine
// never ships credentials. Complements geo.check_address: that action is
// the offline FSA-level wall (does Canada Post serve the area?); this one
// is the interactive upgrade (exact street address, city, postal) for
// stores that bought a key.
//
// Environment configuration:
//
//	ADDRESSCOMPLETE_KEY / CANADA_POST_AC_KEY  the API key (AA11-AA11-AA11-AA11)
//	                                          (unset ⇒ action is a silent no-op,
//	                                          matching email/stripe dev semantics —
//	                                          stores without a key keep using
//	                                          geo.check_address offline)
//	ADDRESSCOMPLETE_API_BASE                  override for tests & proxies
//
// Step config:
//
//	search      required (find mode) — payload path to the shopper's typed
//	            query ("24 john", "K7L 1A", …)
//	mode        "find" (default) | "retrieve" — retrieve expands a Find Id
//	            into the full structured address
//	id          required (retrieve mode) — payload path to the Find Id
//	country     ISO country, default "CAN"
//	max_results cap on returned suggestions, default 7
//	result_key  payload key the results are written to (default
//	            "address_suggestions") so later steps/WS broadcast read them
//
// On success the payload gains $result_key: [{id, text, next?}] for find
// (Next=true means "retrieve this container, not a final address") or
// [{street, city, province, province_code, postal_code}] for retrieve.
//
// Provider error items (unknown key, exhausted credit) surface as step
// errors with the provider's cause — routes handle them via on_failure
// like any other action. The shopper's raw search text is never echoed in
// an error (attacker-controlled, same rule as geo.check_address).

const acDefaultAPIBase = "https://ws1.postescanada-canadapost.ca"

// acActiveKey resolves the effective AddressComplete key: either env name,
// trimmed. Empty ⇒ feature disabled.
func acActiveKey() string {
	for _, env := range []string{"ADDRESSCOMPLETE_KEY", "CANADA_POST_AC_KEY"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

// acItem is the common shape of both Find and Retrieve response items.
// Provider error items carry Error/Description/Cause/Resolution.
type acItem struct {
	Error       string `json:"Error"`
	Description string `json:"Description"`
	Cause       string `json:"Cause"`
	Resolution  string `json:"Resolution"`

	// Find results
	Id   string `json:"Id"`
	Text string `json:"Text"`
	Next string `json:"Next"` // "true" when the item is a container (e.g. a street), not a final address

	// Retrieve results
	Street       string `json:"Street"`
	City         string `json:"City"`
	ProvinceCode string `json:"ProvinceCode"`
	ProvinceName string `json:"ProvinceName"`
	PostalCode   string `json:"PostalCode"`
}

func (b *Bus) geoAddressComplete(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	mode := strings.ToLower(strings.TrimSpace(step.Config["mode"]))
	if mode == "" {
		mode = "find"
	}
	if mode != "find" && mode != "retrieve" {
		return fmt.Errorf("geo.address_complete 'mode' must be find or retrieve, got %q", mode)
	}

	key := acActiveKey()
	if key == "" {
		// Dev/offline semantics: no key ⇒ no-op. The store keeps its
		// FSA-level geo.check_address validation; suggestions simply
		// never appear.
		return nil
	}

	endpoint := mode
	q := url.Values{}
	q.Set("Key", key)
	q.Set("Country", strings.ToUpper(strings.TrimSpace(step.Config["country"])))
	if q.Get("Country") == "" {
		q.Set("Country", "CAN")
	}
	if mode == "find" {
		search := strings.TrimSpace(ResolveVariables(step.Config["search"], eventName, payload))
		if search == "" {
			return fmt.Errorf("address lookup needs a street name, unit, or postal code to search")
		}
		q.Set("SearchTerm", search)
	} else {
		id := strings.TrimSpace(ResolveVariables(step.Config["id"], eventName, payload))
		if id == "" {
			return fmt.Errorf("geo.address_complete retrieve mode requires 'id' config resolving to a Find result Id")
		}
		q.Set("Id", id)
	}

	body, err := acCall(endpoint, q)
	if err != nil {
		return fmt.Errorf("address lookup unavailable right now: %w", err)
	}
	var items []acItem
	if err := json.Unmarshal(body, &items); err != nil {
		return fmt.Errorf("address lookup returned an unreadable response: %w", err)
	}
	for _, it := range items {
		if it.Error != "" {
			// Never echo the shopper's search text; the provider's cause
			// is enough ("Unknown key", "Credit exhausted", …).
			cause := it.Cause
			if cause == "" {
				cause = it.Description
			}
			return fmt.Errorf("address lookup rejected by Canada Post (%s) — check the AddressComplete key and credit", cause)
		}
	}

	maxResults := 7
	if raw := strings.TrimSpace(step.Config["max_results"]); raw != "" {
		if n, perr := strconv.Atoi(raw); perr == nil && n > 0 && n <= 50 {
			maxResults = n
		}
	}
	if len(items) > maxResults {
		items = items[:maxResults]
	}

	resultKey := strings.TrimSpace(step.Config["result_key"])
	if resultKey == "" {
		resultKey = "address_suggestions"
	}

	if mode == "find" {
		results := make([]map[string]interface{}, 0, len(items))
		for _, it := range items {
			r := map[string]interface{}{"id": it.Id, "text": it.Text}
			if it.Next != "" && it.Next != "false" {
				r["next"] = true
			}
			results = append(results, r)
		}
		payload[resultKey] = results
	} else {
		results := make([]map[string]interface{}, 0, len(items))
		for _, it := range items {
			results = append(results, map[string]interface{}{
				"street":        it.Street,
				"city":          it.City,
				"province":      it.ProvinceName,
				"province_code": it.ProvinceCode,
				"postal_code":   it.PostalCode,
			})
		}
		payload[resultKey] = results
	}
	return nil
}

// acCall performs the GET against the AddressComplete json3.ws endpoint.
// The service is GET-with-querystring by design (browser-integration API),
// so the key is in the URL — same trust model as the official JS widget,
// where the key is public and access control is by domain + credit.
func acCall(endpoint string, q url.Values) ([]byte, error) {
	apiBase := os.Getenv("ADDRESSCOMPLETE_API_BASE")
	if apiBase == "" {
		apiBase = acDefaultAPIBase
	}
	target := apiBase + "/AddressComplete/Interactive/" + endpoint + "/json3.ws?" + q.Encode()

	resp, err := sharedHTTPClient.Get(target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}
