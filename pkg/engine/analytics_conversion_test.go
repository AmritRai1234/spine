package engine

// Task 6 conversion join tests: visitor_id is the authoritative key,
// ip_hash+window only a fallback, at most one session marked per order,
// approximate conversions distinguishable in the data.

import (
	"fmt"
	"testing"
	"time"
)

// seedSession inserts a session rollup directly (the conversion join reads
// rollups, not raw events).
func seedSession(t *testing.T, eng *Engine, sid, vid string, startedAt int64) {
	t.Helper()
	if err := eng.ensureAnalyticsSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Bus.DB().Exec(`INSERT INTO analytics_sessions
		(session_id, visitor_id, started_at, ended_at, pageviews, entry_path, exit_path, converted, device, conversion_key)
		VALUES (?,?,?,?,?, '/entry', '/exit', 0, 'desktop', '')`,
		sid, vid, startedAt, startedAt, 1); err != nil {
		t.Fatalf("seed session %s: %v", sid, err)
	}
}

func seedEventWithIP(t *testing.T, eng *Engine, vid, ipHash string, createdAt int64) {
	t.Helper()
	if err := eng.ensureAnalyticsSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Bus.DB().Exec(`INSERT INTO analytics_events
		(id, visitor_id, session_id, event_type, page_path, ip_hash, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		generateUUID(), vid, "s-"+vid, "pageview", "/p", ipHash, createdAt); err != nil {
		t.Fatalf("seed event: %v", err)
	}
}

func sessionConverted(t *testing.T, eng *Engine, sid string) (int, string) {
	t.Helper()
	row := eng.Bus.DB().QueryRow(`SELECT converted, conversion_key FROM analytics_sessions WHERE session_id = ?`, sid)
	var converted int
	var key string
	if err := row.Scan(&converted, &key); err != nil {
		t.Fatalf("read session %s: %v", sid, err)
	}
	return converted, key
}

func TestConversionVisitorIDAuthoritative(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()

	seedSession(t, eng, "sess-a", "visitor-A", now)
	seedSession(t, eng, "sess-b", "visitor-B", now) // another visitor, same ip_hash later

	// Order event carrying the visitor UUID.
	eng.conversionJoin(map[string]interface{}{"visitor_id": "visitor-A", "ip_hash": "sharedhash"})

	if c, key := sessionConverted(t, eng, "sess-a"); c != 1 || key != "visitor" {
		t.Errorf("sess-a: want converted=1 key=visitor, got %d/%q", c, key)
	}
	// The other visitor's session is NOT marked despite sharing the ip.
	if c, _ := sessionConverted(t, eng, "sess-b"); c != 0 {
		t.Errorf("sess-b: visitor_id match must not leak to other visitors' sessions")
	}
}

func TestConversionIPFallbackApproximate(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()

	// A visitor with NO matching session order (storage cleared) — the
	// only trace is the raw event's ip_hash.
	seedEventWithIP(t, eng, "visitor-lost", "cgnat-hash", now)
	seedSession(t, eng, "sess-lost", "visitor-lost", now)
	seedSession(t, eng, "sess-other", "visitor-other", now)

	eng.conversionJoin(map[string]interface{}{"ip_hash": "cgnat-hash"})

	if c, key := sessionConverted(t, eng, "sess-lost"); c != 1 || key != "ip" {
		t.Errorf("fallback: want converted=1 key=ip, got %d/%q", c, key)
	}
	if c, _ := sessionConverted(t, eng, "sess-other"); c != 0 {
		t.Errorf("sess-other must not be marked by the fallback")
	}
}

func TestConversionFallbackWindow24h(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()
	old := now - 25*int64(time.Hour/time.Millisecond) // 25h ago — outside window

	seedEventWithIP(t, eng, "visitor-old", "hash-25h", old)
	seedSession(t, eng, "sess-old", "visitor-old", old)

	eng.conversionJoin(map[string]interface{}{"ip_hash": "hash-25h"})
	if c, _ := sessionConverted(t, eng, "sess-old"); c != 0 {
		t.Errorf("session outside the 24h window must not convert")
	}

	// And one inside the window does.
	recent := now - int64(time.Hour/time.Millisecond)
	seedEventWithIP(t, eng, "visitor-recent", "hash-1h", recent)
	seedSession(t, eng, "sess-recent", "visitor-recent", recent)
	eng.conversionJoin(map[string]interface{}{"ip_hash": "hash-1h"})
	if c, key := sessionConverted(t, eng, "sess-recent"); c != 1 || key != "ip" {
		t.Errorf("in-window fallback: want converted=1 key=ip, got %d/%q", c, key)
	}
}

func TestConversionVisitorWinsOverFallback(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()

	seedSession(t, eng, "sess-visitor", "visitor-V", now)
	// An unrelated older session that would also match the ip fallback.
	seedEventWithIP(t, eng, "visitor-W", "shared-hash", now)
	seedSession(t, eng, "sess-ip", "visitor-W", now)

	// Payload has BOTH keys — the exact match must win and the fallback
	// must not also fire (one conversion, one session).
	eng.conversionJoin(map[string]interface{}{"visitor_id": "visitor-V", "ip_hash": "shared-hash"})

	if c, key := sessionConverted(t, eng, "sess-visitor"); c != 1 || key != "visitor" {
		t.Errorf("exact match: want 1/visitor, got %d/%q", c, key)
	}
	if c, _ := sessionConverted(t, eng, "sess-ip"); c != 0 {
		t.Errorf("fallback must not also fire when the exact match won")
	}
}

func TestConversionNoDoubleCount(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()

	seedSession(t, eng, "sess-one", "visitor-one", now)
	// Two orders from the same visitor: the second must not create a
	// second conversion mark (converted=0 filter makes the UPDATE a no-op).
	eng.conversionJoin(map[string]interface{}{"visitor_id": "visitor-one"})
	eng.conversionJoin(map[string]interface{}{"visitor_id": "visitor-one"})

	rows, err := eng.Bus.DB().Query(`SELECT COUNT(*) FROM analytics_sessions WHERE visitor_id = 'visitor-one' AND converted = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	rows.Next()
	var n int
	_ = rows.Scan(&n)
	if n != 1 {
		t.Errorf("converted session count: want 1, got %d", n)
	}
}

func TestConversionHookWiredOnOrderCreated(t *testing.T) {
	// The hook must actually fire via the Bus on ORDER_CREATED, not just
	// when conversionJoin is called directly.
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()
	seedSession(t, eng, "sess-hook", "visitor-hook", now)

	if _, err := eng.Bus.Emit("ORDER_CREATED", map[string]interface{}{
		"visitor_id": "visitor-hook", "email": "x@example.com",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Hook is synchronous (called inline in Emit), so no wait needed —
	// but poll briefly to be robust.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := sessionConverted(t, eng, "sess-hook"); c == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ORDER_CREATED hook did not mark the session converted")
}

func TestConversionJoinNoKeys(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	now := time.Now().UnixMilli()
	seedSession(t, eng, "sess-x", "visitor-x", now)

	// Neither key present → no-op, no error, nothing marked.
	eng.conversionJoin(map[string]interface{}{"email": "x@example.com"})
	eng.conversionJoin(map[string]interface{}{})
	if c, _ := sessionConverted(t, eng, "sess-x"); c != 0 {
		t.Errorf("no-key join must be a no-op")
	}
	_ = fmt.Sprint() // keep fmt import if assertions change
}
