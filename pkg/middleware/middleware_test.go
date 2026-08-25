package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddleware(t *testing.T) {
	opts := DefaultCORSOptions()
	handler := CORSMiddleware(opts, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Test 1: OPTIONS Preflight with defaults (wildcard, credentials OFF) →
	// wildcard origin, no credentials header, literal Max-Age.
	reqOpt := httptest.NewRequest(http.MethodOptions, "/emit", nil)
	reqOpt.Header.Set("Origin", "http://localhost:3000")
	recOpt := httptest.NewRecorder()

	handler(recOpt, reqOpt)
	if recOpt.Code != http.StatusNoContent {
		t.Errorf("Expected status 204 No Content for preflight, got %d", recOpt.Code)
	}
	if recOpt.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Expected wildcard Allow-Origin without credentials, got %q", recOpt.Header().Get("Access-Control-Allow-Origin"))
	}
	if recOpt.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("AllowCredentials must be OFF by default, got %q", recOpt.Header().Get("Access-Control-Allow-Credentials"))
	}
	if recOpt.Header().Get("Access-Control-Max-Age") != "86400" {
		t.Errorf("Expected literal Max-Age '86400', got %q", recOpt.Header().Get("Access-Control-Max-Age"))
	}

	// Test 2: Standard Request (no credentials)
	reqGet := httptest.NewRequest(http.MethodGet, "/tables", nil)
	reqGet.Header.Set("Origin", "http://localhost:3000")
	recGet := httptest.NewRecorder()

	handler(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recGet.Code)
	}

	// Test 3: Credentials + explicit allowlist — echoes ONLY allowed origin.
	credOpts := DefaultCORSOptions()
	credOpts.AllowCredentials = true
	credOpts.AllowedOrigins = []string{"https://app.example.com"}
	credHandler := CORSMiddleware(credOpts, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	reqAllowed := httptest.NewRequest(http.MethodGet, "/tables", nil)
	reqAllowed.Header.Set("Origin", "https://app.example.com")
	recAllowed := httptest.NewRecorder()
	credHandler(recAllowed, reqAllowed)
	if recAllowed.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("Expected allowed origin echoed with credentials, got %q", recAllowed.Header().Get("Access-Control-Allow-Origin"))
	}
	if recAllowed.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("Expected Allow-Credentials true, got %q", recAllowed.Header().Get("Access-Control-Allow-Credentials"))
	}

	// Test 4: Credentials + DISALLOWED origin → no ACAO header at all
	// (the browser blocks; the server still serves the request).
	reqEvil := httptest.NewRequest(http.MethodGet, "/tables", nil)
	reqEvil.Header.Set("Origin", "https://evil.example")
	recEvil := httptest.NewRecorder()
	credHandler(recEvil, reqEvil)
	if recEvil.Code != http.StatusOK {
		t.Errorf("Expected status 200 (CORS is browser-enforced), got %d", recEvil.Code)
	}
	if got := recEvil.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Expected NO Allow-Origin for disallowed origin with credentials, got %q", got)
	}
}

// TestRateLimitXFFNotTrustedByDefault verifies the X-Forwarded-For spoofing
// fix: without configured trusted proxies, client-supplied XFF is ignored so
// rate limiting keys on the real RemoteAddr.
func TestRateLimitXFFNotTrustedByDefault(t *testing.T) {
	m := NewRateLimitManager(1, 1)
	defer m.Close()

	req1 := httptest.NewRequest(http.MethodPost, "/emit", nil)
	req1.RemoteAddr = "10.0.0.5:1234"
	req1.Header.Set("X-Forwarded-For", "1.2.3.4")

	req2 := httptest.NewRequest(http.MethodPost, "/emit", nil)
	req2.RemoteAddr = "10.0.0.5:1234"
	req2.Header.Set("X-Forwarded-For", "5.6.7.8")

	ip1 := m.ExtractIP(req1)
	ip2 := m.ExtractIP(req2)
	if ip1 != "10.0.0.5" || ip2 != "10.0.0.5" {
		t.Fatalf("XFF must be ignored without trusted proxies: got %q and %q", ip1, ip2)
	}
	if !m.Allow(ip1) {
		t.Fatal("first request should be allowed")
	}
	if m.Allow(ip2) {
		t.Fatal("second request must be rate-limited (same real client despite spoofed XFF)")
	}

	// With an explicit trusted proxy, XFF from that peer IS honored.
	m.SetTrustedProxies([]string{"10.0.0.5"})
	req3 := httptest.NewRequest(http.MethodPost, "/emit", nil)
	req3.RemoteAddr = "10.0.0.5:1234"
	req3.Header.Set("X-Forwarded-For", "9.9.9.9")
	if ip3 := m.ExtractIP(req3); ip3 != "9.9.9.9" {
		t.Fatalf("expected XFF honored from trusted proxy, got %q", ip3)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	panicHandler := func(w http.ResponseWriter, r *http.Request) {
		panic("simulated engine unexpected failure")
	}

	handler := RecoveryMiddleware(panicHandler)
	req := httptest.NewRequest(http.MethodPost, "/emit", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500 on panic, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("internal_server_error")) {
		t.Errorf("Expected response body to contain internal_server_error, got %s", rec.Body.String())
	}
}

func TestLoggingMiddleware(t *testing.T) {
	dummyHandler := func(w http.ResponseWriter, r *http.Request) {
		reqID := GetRequestID(r.Context())
		if reqID == "" {
			t.Errorf("Expected non-empty RequestID in context")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("logged"))
	}

	handler := LoggingMiddleware(dummyHandler)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Errorf("Expected X-Request-ID header to be present in response")
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	dummyHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	handler := SecurityHeadersMiddleware(dummyHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("Expected nosniff header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("Expected DENY header")
	}
}

func TestBodyLimitMiddleware(t *testing.T) {
	dummyHandler := func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 100)
		_, err := r.Body.Read(buf)
		if err != nil && err.Error() != "EOF" {
			WritePayloadTooLarge(w)
			return
		}
		w.WriteHeader(http.StatusOK)
	}

	// Max 10 bytes allowed
	handler := BodyLimitMiddleware(10, dummyHandler)

	// Send 50 bytes (oversized payload)
	largeData := bytes.Repeat([]byte("a"), 50)
	req := httptest.NewRequest(http.MethodPost, "/emit", bytes.NewReader(largeData))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Expected 413 Payload Too Large, got %d", rec.Code)
	}
}

func TestCustomContextMiddleware(t *testing.T) {
	mgr := NewCustomContextManager()
	mgr.SetStaticAttribute("region", "us-west-2")

	// Custom extractor for location and temperature headers
	mgr.AddExtractor(func(r *http.Request) map[string]interface{} {
		return map[string]interface{}{
			"location":    r.Header.Get("X-Location"),
			"temperature": r.Header.Get("X-Device-Temp"),
		}
	})

	dummyHandler := func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]interface{}{"user": "alice"}
		merged := MergeCustomContextIntoPayload(r.Context(), payload)

		if merged["location"] != "San Francisco, CA" {
			t.Errorf("Expected location 'San Francisco, CA', got %v", merged["location"])
		}
		if merged["temperature"] != "72F" {
			t.Errorf("Expected temperature '72F', got %v", merged["temperature"])
		}
		if merged["region"] != "us-west-2" {
			t.Errorf("Expected region 'us-west-2', got %v", merged["region"])
		}
		w.WriteHeader(http.StatusOK)
	}

	handler := mgr.Middleware(dummyHandler)
	req := httptest.NewRequest(http.MethodPost, "/emit", nil)
	req.Header.Set("X-Location", "San Francisco, CA")
	req.Header.Set("X-Device-Temp", "72F")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}
