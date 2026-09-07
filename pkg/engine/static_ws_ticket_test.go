package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// M4 tests — hardened SPA static serving.

func newStaticTestEngine(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	// Re-point the handler at the temp root by chdir (spaHandler reads
	// rootDir passed in — use the real signature directly).
	_ = root
	return e
}

func TestSPAHandler_ServesFiles(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(root, "assets", "app.abc123.js"), []byte("console.log(1)"), 0o644)

	e := &Engine{}
	h := e.spaHandler(root)

	// Root serves index.html
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("root: %d %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("HTML cache-control = %q, want no-cache", cc)
	}

	// Hashed asset gets immutable caching
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/assets/app.abc123.js", nil))
	if rec.Code != 200 {
		t.Fatalf("asset: %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "max-age=31536000, immutable" {
		t.Fatalf("asset cache-control = %q", cc)
	}

	// SPA fallback: unknown route serves index.html (client routing)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/some/client/route", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("SPA fallback: %d %s", rec.Code, rec.Body.String())
	}

	// Missing asset 404s (never HTML fallback — would fail JS parse)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/assets/nope.999.js", nil))
	if rec.Code != 404 {
		t.Fatalf("missing asset should 404, got %d", rec.Code)
	}

	// Traversal attempt blocked (mux 400s ".." or our handler 404s —
	// both are rejections; the only failure is serving source)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/../engine.go", nil))
	if rec.Code == 200 && strings.Contains(rec.Body.String(), "package engine") {
		t.Fatal("traversal served source file!")
	}
	if rec.Code != 400 && rec.Code != 404 {
		t.Fatalf("traversal: unexpected %d", rec.Code)
	}

	// POST rejected
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/", strings.NewReader("x")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should 405, got %d", rec.Code)
	}
}

// M5 tests — WS one-time tickets.

func TestWSTicket_IssueConsumeReplay(t *testing.T) {
	e := &Engine{APIKey: "secret-key-1"}
	// authFailClosed defaults false — zero value fine.

	// No key → 401
	rec := httptest.NewRecorder()
	e.handleWSTicket(rec, httptest.NewRequest("GET", "/ws-ticket", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthenticated issue: %d", rec.Code)
	}

	// Wrong key → 401
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws-ticket", nil)
	req.Header.Set("X-API-Key", "wrong")
	e.handleWSTicket(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong key issue: %d", rec.Code)
	}

	// Correct key → ticket
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/ws-ticket", nil)
	req.Header.Set("X-API-Key", "secret-key-1")
	e.handleWSTicket(rec, req)
	if rec.Code != 200 {
		t.Fatalf("issue: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Ticket string }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Ticket) != 64 { // 32 bytes hex
		t.Fatalf("ticket entropy: %q", resp.Ticket)
	}

	// Consume via wsAuthCheck with ?ticket=
	r := httptest.NewRequest("GET", "/ws?ticket="+resp.Ticket, nil)
	ok, _ := e.wsAuthCheck(r)
	if !ok {
		t.Fatal("valid ticket rejected")
	}

	// Replay → rejected (single use)
	r = httptest.NewRequest("GET", "/ws?ticket="+resp.Ticket, nil)
	ok, _ = e.wsAuthCheck(r)
	if ok {
		t.Fatal("ticket replay accepted!")
	}
}

func TestWSTicket_ExpiryAndTamper(t *testing.T) {
	e := &Engine{APIKey: "k"}

	// Tampered ticket rejected
	r := httptest.NewRequest("GET", "/ws?ticket=deadbeef", nil)
	if ok, _ := e.wsAuthCheck(r); ok {
		t.Fatal("unknown ticket accepted")
	}

	// Expired ticket rejected
	tk := &wsTicket{expires: time.Now().Add(-time.Second)}
	e.wsTickets.Store("expired-one", tk)
	r = httptest.NewRequest("GET", "/ws?ticket=expired-one", nil)
	if ok, _ := e.wsAuthCheck(r); ok {
		t.Fatal("expired ticket accepted")
	}
	// Consumed even though expired
	if _, stillThere := e.wsTickets.Load("expired-one"); stillThere {
		t.Fatal("expired ticket not cleaned up")
	}

	// Ticket does NOT fall through to header auth when invalid
	r = httptest.NewRequest("GET", "/ws?ticket=bogus", nil)
	r.Header.Set("X-API-Key", "k")
	if ok, _ := e.wsAuthCheck(r); ok {
		t.Fatal("invalid ticket fell through to header key")
	}

	// Header path still works alongside tickets
	r = httptest.NewRequest("GET", "/ws", nil)
	r.Header.Set("X-API-Key", "k")
	if ok, _ := e.wsAuthCheck(r); !ok {
		t.Fatal("header key rejected")
	}
}

func TestWSTicket_SweepRemovesExpired(t *testing.T) {
	e := &Engine{APIKey: "k"}
	e.wsTickets.Store("old", &wsTicket{expires: time.Now().Add(-time.Hour)})
	e.wsTickets.Store("fresh", &wsTicket{expires: time.Now().Add(time.Minute)})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws-ticket", nil)
	req.Header.Set("X-API-Key", "k")
	e.handleWSTicket(rec, req)
	if rec.Code != 200 {
		t.Fatalf("issue: %d", rec.Code)
	}
	if _, ok := e.wsTickets.Load("old"); ok {
		t.Fatal("expired ticket survived sweep")
	}
	if _, ok := e.wsTickets.Load("fresh"); !ok {
		t.Fatal("fresh ticket wrongly swept")
	}
}
