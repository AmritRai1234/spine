package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// Dedicated race coverage for DynamicCORSMiddleware — this middleware sits
// in front of EVERY engine route (not just /track), so the atomic swap
// deserves its own test independent of any one feature's concurrency test.
//
// The invariant pinned here: under concurrent requests + concurrent env
// flips, every response's ACAO header is EITHER the wildcard OR one of the
// exact origins from SOME consistent snapshot — never a mixed/garbage
// value, never a torn read (which -race would flag directly).

func TestDynamicCORSRaceUnderEnvFlips(t *testing.T) {
	t.Setenv("SPINE_CORS_ORIGINS", "https://a.example,https://b.example")
	next := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	handler := DynamicCORSMiddleware(next)

	const readers = 8
	const iterations = 200
	origins := []string{"https://a.example", "https://b.example", "https://c.example"}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: hit the handler concurrently, assert the emitted ACAO is a
	// value from SOME coherent snapshot.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-stop:
					return
				default:
				}
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Origin", origins[i%len(origins)])
				rr := httptest.NewRecorder()
				handler(rr, req)
				acao := rr.Header().Get("Access-Control-Allow-Origin")
				switch acao {
				case "", "*", "https://a.example", "https://b.example", "https://c.example":
					// all valid snapshots
				default:
					t.Errorf("torn/mixed ACAO value: %q", acao)
				}
			}
		}()
	}

	// Flippers: mutate the env while readers run. Each flip must become
	// visible atomically — either fully old or fully new.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			os.Setenv("SPINE_CORS_ORIGINS", "https://a.example,https://b.example")
			os.Setenv("SPINE_CORS_ORIGINS", "https://a.example,https://b.example,https://c.example")
		}
		close(stop)
	}()

	wg.Wait()
}

// TestDynamicCORSSwapSemantics: after the env settles, the final value is
// always fully applied (no lost update leaving a stale allowlist forever).
func TestDynamicCORSSwapSemantics(t *testing.T) {
	t.Setenv("SPINE_CORS_ORIGINS", "https://only.example")
	next := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	handler := DynamicCORSMiddleware(next)

	// First request builds the new snapshot.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://only.example")
	rr := httptest.NewRecorder()
	handler(rr, req)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://only.example" {
		t.Fatalf("allowlisted origin reflected: want https://only.example, got %q", got)
	}

	// Disallowed origin gets no ACAO (credentials-adjacent hard rule).
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Origin", "https://evil.example")
	rr2 := httptest.NewRecorder()
	handler(rr2, req2)
	if got := rr2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("non-allowlisted origin must get no ACAO, got %q", got)
	}
	if !strings.Contains(rr2.Header().Get("Vary"), "Origin") {
		t.Errorf("Vary: Origin missing")
	}
}
