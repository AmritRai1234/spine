package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Per-route rate limiting composes with (never replaces) the global manager.
// The threat model: a new public endpoint adds a TIGHTER per-route bucket —
// that bucket must sit ON TOP of the global limiter so both are independently
// enforceable (global trips even when under the route cap, and vice versa).

// TestRouteLimiterTighterThanGlobal: under the per-route cap the request
// passes both gates; exceeding the ROUTE cap trips even though the global
// budget is untouched/fresh.
func TestRouteLimiterRouteCapTripsBeforeGlobal(t *testing.T) {
	global := NewRateLimitManager(100, 100) // effectively unlimited for this test
	defer global.Close()
	route := NewRateLimitManager(0.5, 2) // 2 immediate, then 1 per 2s
	defer route.Close()

	inner := route.Middleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Global OUTSIDE route (same order as the engine chain: global is
	// applied first, so it wraps the route-limited handler).
	handler := global.Middleware(inner)

	ok := 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/track", nil)
		req.RemoteAddr = "10.1.1.1:4444"
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code == http.StatusOK {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("route cap: want exactly 2 ok, got %d", ok)
	}
}

// TestRouteLimiterGlobalStillEnforced: the global limit trips even when the
// per-route bucket is generous — proving the route bucket did not REPLACE
// the global gate (the "client narrows, never widens" shape).
func TestRouteLimiterGlobalStillEnforced(t *testing.T) {
	global := NewRateLimitManager(0.5, 2) // 2 immediate requests total
	defer global.Close()
	route := NewRateLimitManager(100, 100) // generous route bucket
	defer route.Close()

	inner := route.Middleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := global.Middleware(inner)

	ok := 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/track", nil)
		req.RemoteAddr = "10.2.2.2:4444"
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code == http.StatusOK {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("global cap must trip under a generous route bucket: want 2 ok, got %d", ok)
	}
}

// TestRouteLimiterBucketsIndependent: a global Allow() consumed for /track
// must not drain a different route's budget, and each manager's per-IP
// buckets are keyed separately (two managers = two independent token buckets
// for the same IP — composition means BOTH must allow).
func TestRouteLimiterBucketsIndependent(t *testing.T) {
	global := NewRateLimitManager(10, 5)
	defer global.Close()
	route := NewRateLimitManager(10, 5)
	defer route.Close()

	ip := "10.3.3.3" // middleware ExtractIP strips the port — key on bare IP
	// Drain the global bucket fully.
	for i := 0; i < 5; i++ {
		if !global.Allow(ip) {
			t.Fatalf("global drain failed at %d", i)
		}
	}
	if global.Allow(ip) {
		t.Fatal("global bucket should be drained")
	}
	// The route bucket is untouched — its tokens are its own. But
	// composition requires the GLOBAL gate to pass first, so a request now
	// is rejected by the global layer even though the route layer would
	// allow it. That is exactly the double-gate semantics we pin.
	inner := route.Middleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := global.Middleware(inner)
	req := httptest.NewRequest("POST", "/track", nil)
	req.RemoteAddr = ip
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected global 429 with drained global bucket, got %d", rr.Code)
	}
}
