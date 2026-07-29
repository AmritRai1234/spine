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

	// Test 1: OPTIONS Preflight
	reqOpt := httptest.NewRequest(http.MethodOptions, "/emit", nil)
	reqOpt.Header.Set("Origin", "http://localhost:3000")
	recOpt := httptest.NewRecorder()

	handler(recOpt, reqOpt)
	if recOpt.Code != http.StatusNoContent {
		t.Errorf("Expected status 204 No Content for preflight, got %d", recOpt.Code)
	}
	if recOpt.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Expected Allow-Origin header, got %s", recOpt.Header().Get("Access-Control-Allow-Origin"))
	}

	// Test 2: Standard Request
	reqGet := httptest.NewRequest(http.MethodGet, "/tables", nil)
	reqGet.Header.Set("Origin", "http://localhost:3000")
	recGet := httptest.NewRecorder()

	handler(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recGet.Code)
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
