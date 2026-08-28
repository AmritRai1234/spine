package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestTrackingRegister17track(t *testing.T) {
	var gotPath, gotToken, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("17token")
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"accepted": []string{"RR123456789CN"}}})
	}))
	defer srv.Close()

	b := &Bus{}
	payload := map[string]interface{}{"tracking_number": "RR123456789CN"}
	err := b.trackingRegister(&manifest.RouteStep{Config: map[string]string{
		"url":     srv.URL + "/track/v2.4/register",
		"numbers": "$event.payload.tracking_number",
		"headers": "17token: tok123",
	}}, "TEST", payload)
	if err != nil {
		t.Fatalf("tracking.register: %v", err)
	}
	if gotPath != "/track/v2.4/register" || gotToken != "tok123" {
		t.Fatalf("bad request: path=%q token=%q", gotPath, gotToken)
	}
	if gotBody != `[{"number":"RR123456789CN"}]` {
		t.Fatalf("bad body: %q", gotBody)
	}
	resp, ok := payload["tracking_response"].(map[string]interface{})
	if !ok || resp["code"].(float64) != 0 {
		t.Fatalf("response not parsed into payload: %v", payload["tracking_response"])
	}
}

func TestTrackingRegisterCustomBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()

	b := &Bus{}
	payload := map[string]interface{}{"tn": "1Z999", "carrier": "ups"}
	err := b.trackingRegister(&manifest.RouteStep{Config: map[string]string{
		"url":     srv.URL,
		"numbers": "$event.payload.tn",
		"body":    `{"tracking_number":"{{numbers}}","carrier":"$event.payload.carrier"}`,
	}}, "TEST", payload)
	if err != nil {
		t.Fatalf("tracking.register: %v", err)
	}
	if gotBody != `{"tracking_number":["1Z999"],"carrier":"ups"}` {
		t.Fatalf("bad custom body: %q", gotBody)
	}
}

func TestTrackingRegisterOptional(t *testing.T) {
	b := &Bus{}
	// No URL + optional → silent no-op
	if err := b.trackingRegister(&manifest.RouteStep{Config: map[string]string{
		"numbers": "$event.payload.tn", "optional": "true",
	}}, "TEST", map[string]interface{}{"tn": "X"}); err != nil {
		t.Fatalf("optional missing url should be no-op, got %v", err)
	}
	// No URL, not optional → error
	if err := b.trackingRegister(&manifest.RouteStep{Config: map[string]string{
		"numbers": "$event.payload.tn",
	}}, "TEST", map[string]interface{}{"tn": "X"}); err == nil {
		t.Fatal("expected error when url missing and not optional")
	}
	// Numbers resolve empty + optional → no-op
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("should not be called") }))
	defer srv.Close()
	if err := b.trackingRegister(&manifest.RouteStep{Config: map[string]string{
		"url": srv.URL, "numbers": "$event.payload.missing", "optional": "true",
	}}, "TEST", map[string]interface{}{}); err != nil {
		t.Fatalf("optional empty numbers should be no-op, got %v", err)
	}
}
