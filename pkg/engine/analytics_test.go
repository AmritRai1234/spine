package engine

// /track ingest tests — black-box over the real HTTP surface.
// The threat model in analytics.go: public unauthenticated endpoint, so
// validation is fail-closed for DATA (drop anything malformed) while
// fail-open for the SHOPPER (never 5xx on bad input).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// waitForRows polls until the table has n rows (the async-path wait helper
// in tests/testhelpers can't be imported here — it imports the root spine
// package, which would cycle back into pkg/engine).
func waitForRows(t *testing.T, eng *Engine, table string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rowCount(eng, table)
		if err == nil && got == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n == 0 {
		// Zero-row waits only need the table to exist and be empty.
		if got, err := rowCount(eng, table); err == nil && got == 0 {
			return
		}
		t.Logf("note: table %s not yet queryable or already checked", table)
		return
	}
	t.Fatalf("timeout waiting for %d rows in %s", n, table)
}

func rowCount(eng *Engine, table string) (int, error) {
	rows, err := eng.Bus.DB().Query("SELECT COUNT(*) FROM " + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, rows.Err()
	}
	var got int
	if err := rows.Scan(&got); err != nil {
		return 0, err
	}
	return got, nil
}

const trackManifest = `spine_version: 1
database:
  tables:
    - analytics_events
    - analytics_sessions
`

func trackEngine(t *testing.T) (*Engine, http.Handler, func()) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "analytics.spine")
	if err := os.WriteFile(manifestPath, []byte(trackManifest), 0644); err != nil {
		t.Fatal(err)
	}
	schema, err := manifest.ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	eng, err := New(schema, filepath.Join(dir, "analytics.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng, eng.HTTPHandler(), func() { _ = eng.Close() }
}

func trackPost(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/track", strings.NewReader(body))
	req.RemoteAddr = "10.9.9.9:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func validBatch(n int) string {
	events := make([]string, n)
	for i := range events {
		events[i] = fmt.Sprintf(
			`{"event_type":"pageview","visitor_id":"v-uuid-%d","session_id":"s-uuid-%d","page_path":"/products/x","page_title":"X","referrer":"https://google.com/","utm_source":"google","utm_medium":"cpc","utm_campaign":"summer","viewport_w":1440,"viewport_h":900}`,
			i, i)
	}
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func countEvents(t *testing.T, eng *Engine) int {
	t.Helper()
	rows, err := eng.Bus.DB().Query(`SELECT COUNT(*) FROM analytics_events`)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer rows.Close()
	rows.Next()
	var n int
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTrackHappyBatch(t *testing.T) {
	eng, handler, cleanup := trackEngine(t)
	defer cleanup()

	rr := trackPost(handler, validBatch(3))
	if rr.Code != 204 {
		t.Fatalf("happy batch: want 204, got %d %s", rr.Code, rr.Body.String())
	}

	// The row is written before the response (Task 2 synchronous ingest) —
	// but use the same wait helper the async paths use for consistency.
	waitForRows(t, eng, "analytics_events", 3)
	if got := countEvents(t, eng); got != 3 {
		t.Fatalf("want 3 rows, got %d", got)
	}
}

func TestTrackRejectsMethodAndSize(t *testing.T) {
	_, handler, cleanup := trackEngine(t)
	defer cleanup()

	// GET → 405.
	req := httptest.NewRequest("GET", "/track", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 405 {
		t.Fatalf("GET /track: want 405, got %d", rr.Code)
	}

	// Oversized body → 413, nothing written. Must be valid JSON prefix
	// (a huge garbage body 400s at the parser instead — also fine, but we
	// want to pin the size gate specifically).
	big := `{"events":[` + strings.Repeat(`"a",`, 32<<10) + `]}`
	if code := trackPost(handler, big).Code; code != 413 {
		t.Fatalf("oversized body: want 413, got %d", code)
	}

	// Malformed JSON → 204 (dropped, never 5xx).
	if code := trackPost(handler, `{"events":[not-json`).Code; code != 204 {
		t.Fatalf("malformed json: want 204, got %d", code)
	}
}

func TestTrackValidationDrops(t *testing.T) {
	eng, handler, cleanup := trackEngine(t)
	defer cleanup()

	cases := []string{
		`{"events":[{"event_type":"steal","visitor_id":"v1","session_id":"s1","page_path":"/x"}]}`, // unknown type
		`{"events":[{"event_type":"click","visitor_id":"","session_id":"s1","page_path":"/x"}]}`,   // empty visitor
		`{"events":[{"event_type":"click","visitor_id":"v é","session_id":"s1","page_path":"/x"}]}`, // non-ascii id
		`{"events":[{"event_type":"click","visitor_id":"v1","session_id":"s1","page_path":"http://evil"}]}`, // path not local
		`{"events":[{"event_type":"click","visitor_id":"v1","session_id":"s1","page_path":"/x","x":-5}]}`,   // absurd coords
		`{"events":[{"event_type":"click","visitor_id":"v1","session_id":"s1","page_path":"/x","scroll_pct":999}]}`, // scroll out of range
	}
	for i, body := range cases {
		if code := trackPost(handler, body).Code; code != 204 {
			t.Errorf("case %d: want 204 (drop), got %d", i, code)
		}
	}
	waitForRows(t, eng, "analytics_events", 0)
	if got := countEvents(t, eng); got != 0 {
		t.Fatalf("invalid events must write zero rows, got %d", got)
	}
}

func TestTrackBatchCapAndFieldCapping(t *testing.T) {
	eng, handler, cleanup := trackEngine(t)
	defer cleanup()

	// 51 events → 413 (whole batch rejected before any DB work).
	if code := trackPost(handler, validBatch(51)).Code; code != 413 {
		t.Fatalf("over-cap batch: want 413, got %d", code)
	}
	waitForRows(t, eng, "analytics_events", 0)

	// Long fields are capped, not dropped: 1 pageview with a 10 KB title.
	longTitle := strings.Repeat("x", 10000)
	body := `{"events":[{"event_type":"pageview","visitor_id":"v1","session_id":"s1","page_path":"/p","page_title":"` + longTitle + `"}]}`
	if code := trackPost(handler, body).Code; code != 204 {
		t.Fatalf("long field: want 204, got %d", code)
	}
	waitForRows(t, eng, "analytics_events", 1)
	rows, err := eng.Bus.DB().Query(`SELECT page_title FROM analytics_events LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row")
	}
	var title string
	if err := rows.Scan(&title); err != nil {
		t.Fatal(err)
	}
	if len(title) > trackMaxStr {
		t.Fatalf("title not capped: %d > %d", len(title), trackMaxStr)
	}
}

func TestTrackResponseShapeIsJSONSilence(t *testing.T) {
	_, handler, cleanup := trackEngine(t)
	defer cleanup()
	rr := trackPost(handler, validBatch(1))
	if rr.Code != 204 {
		t.Fatalf("want 204, got %d", rr.Code)
	}
	// 204 must carry no body — the snippet reads nothing.
	if rr.Body.Len() != 0 {
		t.Errorf("204 should have empty body, got: %s", rr.Body.String())
	}
}
