package engine

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// geo.address_complete tests — the action is key-gated by env, so every
// test installs a fake AddressComplete provider via ADDRESSCOMPLETE_API_BASE.

func acStep(config map[string]string) func(payload map[string]interface{}) error {
	if config == nil {
		config = map[string]string{}
	}
	return func(payload map[string]interface{}) error {
		b := &Bus{}
		step := &manifest.RouteStep{Action: "geo.address_complete", Config: config}
		return b.geoAddressComplete(step, "ADDRESS_LOOKUP", payload)
	}
}

func acFakeServer(t *testing.T, wantPath string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantPath != "" && !strings.Contains(strings.ToLower(r.URL.Path), strings.ToLower(wantPath)) {
			t.Errorf("unexpected endpoint path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAC_SilentNoOpWithoutKey(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "")
	payload := map[string]interface{}{"search": "24 john st"}
	// No provider reachable at the default URL — but without a key the
	// action must return nil WITHOUT any network call.
	if err := acStep(map[string]string{"search": "$event.payload.search"})(payload); err != nil {
		t.Fatalf("no key must be a silent no-op, got: %v", err)
	}
	if _, has := payload["address_suggestions"]; has {
		t.Fatal("no-key no-op must not write results into the payload")
	}
}

func TestAC_Find_ResultsAndPayloadKey(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	srv := acFakeServer(t, "/Find/", `[{"Id":"0","Text":"24 John St, Kingston ON"},{"Id":"CAN|P||K7L|24|JOHN|ST|A","Text":"24 John St"}]`)
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	payload := map[string]interface{}{"q": "24 john"}
	err := acStep(map[string]string{
		"search":     "$event.payload.q",
		"max_results": "5",
	})(payload)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	res, ok := payload["address_suggestions"].([]map[string]interface{})
	if !ok || len(res) != 2 {
		t.Fatalf("expected 2 suggestions in address_suggestions, got %#v", payload["address_suggestions"])
	}
	if res[0]["id"] != "0" || res[0]["text"] != "24 John St, Kingston ON" {
		t.Fatalf("wrong first suggestion: %#v", res[0])
	}
}

func TestAC_Find_CustomResultKeyAndNextFlag(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	srv := acFakeServer(t, "/Find/", `[{"Id":"c1","Text":"JOHN ST","Next":"true"}]`)
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	payload := map[string]interface{}{"q": "john"}
	err := acStep(map[string]string{
		"search":     "$event.payload.q",
		"result_key": "ac_hits",
	})(payload)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	res, ok := payload["ac_hits"].([]map[string]interface{})
	if !ok || len(res) != 1 {
		t.Fatalf("expected 1 item under ac_hits, got %#v", payload["ac_hits"])
	}
	if res[0]["next"] != true {
		t.Fatalf("container item must carry next:true, got %#v", res[0])
	}
}

func TestAC_Retrieve_StructuredAddress(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	srv := acFakeServer(t, "/Retrieve/", `[{"Street":"24 John St","City":"Kingston","ProvinceName":"Ontario","ProvinceCode":"ON","PostalCode":"K7L 1A4"}]`)
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	payload := map[string]interface{}{"picked_id": "CAN|P||K7L|24|JOHN|ST|A"}
	err := acStep(map[string]string{
		"mode":       "retrieve",
		"id":         "$event.payload.picked_id",
		"result_key": "address",
	})(payload)
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	res, ok := payload["address"].([]map[string]interface{})
	if !ok || len(res) != 1 {
		t.Fatalf("expected 1 structured address, got %#v", payload["address"])
	}
	if res[0]["city"] != "Kingston" || res[0]["postal_code"] != "K7L 1A4" || res[0]["province_code"] != "ON" {
		t.Fatalf("wrong structured address: %#v", res[0])
	}
}

func TestAC_ProviderErrorSurfaces(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "WRONG-KEY")
	srv := acFakeServer(t, "", `[{"Error":"2","Description":"Unknown key","Cause":"The key you are using was not found.","Resolution":"Check the key."}]`)
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	err := acStep(map[string]string{"search": "$event.payload.q"})(map[string]interface{}{"q": "24 john"})
	if err == nil {
		t.Fatal("provider error item must fail the step")
	}
	if !strings.Contains(err.Error(), "key you are using was not found") {
		t.Fatalf("error should carry the provider cause, got: %v", err)
	}
	if strings.Contains(err.Error(), "24 john") {
		t.Fatal("error must never echo the shopper's search text")
	}
}

func TestAC_MaxResultsCap(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	items := `[`
	for i := 0; i < 10; i++ {
		if i > 0 {
			items += `,`
		}
		items += `{"Id":"` + string(rune('a'+i)) + `","Text":"hit"}`
	}
	items += `]`
	srv := acFakeServer(t, "", items)
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	payload := map[string]interface{}{"q": "x"}
	if err := acStep(map[string]string{"search": "$event.payload.q", "max_results": "3"})(payload); err != nil {
		t.Fatalf("find failed: %v", err)
	}
	res := payload["address_suggestions"].([]map[string]interface{})
	if len(res) != 3 {
		t.Fatalf("max_results cap failed: got %d items", len(res))
	}
}

func TestAC_ConfigAndInputGuards(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	t.Setenv("ADDRESSCOMPLETE_API_BASE", "http://127.0.0.1:1") // unreachable; must never be hit

	// Bad mode
	if err := acStep(map[string]string{"mode": "delete", "search": "$event.payload.q"})(map[string]interface{}{"q": "x"}); err == nil {
		t.Fatal("bad mode must fail loudly")
	}
	// Empty resolved search
	if err := acStep(map[string]string{"search": "$event.payload.missing"})(map[string]interface{}{}); err == nil {
		t.Fatal("empty search must fail loudly")
	}
	// Retrieve without id
	if err := acStep(map[string]string{"mode": "retrieve"})(map[string]interface{}{}); err == nil {
		t.Fatal("retrieve without id must fail loudly")
	}
}

func TestAC_QueryCarriesKeyAndSearchTerm(t *testing.T) {
	t.Setenv("ADDRESSCOMPLETE_KEY", "AA11-AA11-AA11-AA11")
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	t.Setenv("ADDRESSCOMPLETE_API_BASE", srv.URL)

	if err := acStep(map[string]string{"search": "$event.payload.q", "country": "can"})(map[string]interface{}{"q": "K7L"}); err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if got.Get("Key") != "AA11-AA11-AA11-AA11" {
		t.Fatalf("key missing from query: %v", got)
	}
	if got.Get("SearchTerm") != "K7L" {
		t.Fatalf("search term missing: %v", got)
	}
	if got.Get("Country") != "CAN" {
		t.Fatalf("country should default/uppercase to CAN: %v", got)
	}
}
