package engine

// /track — first-party analytics ingest (analytics_events / analytics_sessions).
//
// Threat model: this is a PUBLIC, unauthenticated POST endpoint fed by the
// storefront snippet. It is therefore wrapped in BOTH the engine's global
// rate limiter (request-flood protection shared with every other endpoint)
// AND its own tighter per-route bucket (events/sec ceiling specific to
// analytics) — composition, not replacement. The global gate wraps the
// route gate, so a request must pass both.
//
// Ingest is fail-open by design for the SHOPPER (tracking must never break
// the storefront): invalid payloads are dropped with a log line and a 204.
// But every field is validated and length-capped BEFORE any DB write, and
// oversized bodies are rejected before parsing.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/AmritRai1234/spine/pkg/middleware"
)

const (
	// /track rate-limit ceiling: tighter than the global per-IP bucket.
	// A real storefront sends ~1 request / 3s per visitor (batched), so
	// 5 req/s with burst 15 is generous headroom per IP while still
	// bounding DB writes under flood.
	trackRouteRPS   = 5.0
	trackRouteBurst = 15.0
)

const (
	trackMaxEvents      = 50       // max events per batch
	trackMaxStr         = 300      // generic string field cap
	trackMaxPath        = 200      // page_path cap
	trackMaxElementText = 80       // clicked-element text snippet cap
	trackBodyLimit      = 32 << 10 // 32 KB — far below the 1 MB global body cap
)

// trackEvent is one client-reported behavior event after validation.
// ip_hash / device / user_agent are resolved SERVER-SIDE (Task 4) — the
// client never sends identity signals (trust boundary).
type trackEvent struct {
	VisitorID        string  `json:"visitor_id"`
	SessionID        string  `json:"session_id"`
	EventType        string  `json:"event_type"`
	PagePath         string  `json:"page_path"`
	PageTitle        string  `json:"page_title"`
	Referrer         string  `json:"referrer"`
	UTMSource        string  `json:"utm_source"`
	UTMMedium        string  `json:"utm_medium"`
	UTMCampaign      string  `json:"utm_campaign"`
	X                float64 `json:"x"`
	Y                float64 `json:"y"`
	ScrollPct        float64 `json:"scroll_pct"`
	ViewportW        int     `json:"viewport_w"`
	ViewportH        int     `json:"viewport_h"`
	ElementSelector  string  `json:"element_selector"`
	ElementText      string  `json:"element_text"`
}

var trackEventTypes = map[string]bool{
	"pageview": true,
	"click":    true,
	"scroll":   true,
	"custom":   true,
}

// wrapTrackMiddleware composes the /track chain: the engine's global rate
// limiter OUTSIDE (shared request-flood gate — never bypassed by having a
// route bucket), the tighter per-route analytics bucket INSIDE, then the
// standard hardening layers. A request must pass both limiters.
func (e *Engine) wrapTrackMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	h := handler

	// Tighter per-route bucket (inner gate): analytics-specific events/sec
	// ceiling, using the same proven RateLimitManager machinery.
	if e.trackLimiter == nil {
		e.trackLimiter = middleware.NewRateLimitManager(trackRouteRPS, trackRouteBurst)
	}
	h = e.trackLimiter.Middleware(h)

	// Global limiter OUTSIDE the route bucket.
	if e.rateLimiter != nil {
		h = e.rateLimiter.Middleware(h)
	}

	h = middleware.BodyLimitMiddleware(trackBodyLimit, h)
	h = middleware.SecurityHeadersMiddleware(h)
	h = middleware.DynamicCORSMiddleware(h)
	h = middleware.LoggingMiddleware(h)
	h = middleware.RecoveryMiddleware(h)
	return h
}

func (e *Engine) handleTrack(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Method gate: POST only. GET /track from a stray link/prefetch is 405.
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		w.Write([]byte(`{"status":"error","error":"method_not_allowed"}`))
		return
	}

	body, readErr := io.ReadAll(io.LimitReader(r.Body, trackBodyLimit+1))
	// MaxBytesReader (applied by BodyLimitMiddleware upstream) aborts the
	// read with an error once the cap is hit — treat both an explicit
	// over-cap read and a read error as the 413 path.
	if readErr != nil || len(body) > trackBodyLimit {
		w.WriteHeader(413)
		w.Write([]byte(`{"status":"error","error":"payload_too_large"}`))
		return
	}
	if len(body) > trackBodyLimit {
		// Body cap hit — no parse, no write. 413 tells the client its
		// batch was oversized; the snippet drops the batch silently.
		w.WriteHeader(413)
		w.Write([]byte(`{"status":"error","error":"payload_too_large"}`))
		return
	}

	var batch struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		log.Printf("[analytics] /track: malformed JSON dropped")
		w.WriteHeader(204)
		return
	}
	if len(batch.Events) > trackMaxEvents {
		log.Printf("[analytics] /track: batch of %d exceeds cap %d — dropped", len(batch.Events), trackMaxEvents)
		w.WriteHeader(413)
		w.Write([]byte(`{"status":"error","error":"too_many_events"}`))
		return
	}

	valid := make([]trackEvent, 0, len(batch.Events))
	for i, raw := range batch.Events {
		var ev trackEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			log.Printf("[analytics] /track: event %d unparseable — dropped", i)
			continue
		}
		if !trackEventTypes[ev.EventType] {
			log.Printf("[analytics] /track: event %d unknown type %q — dropped", i, ev.EventType)
			continue
		}
		if ev.VisitorID == "" || len(ev.VisitorID) > 64 || !isASCIIID(ev.VisitorID) {
			continue
		}
		if ev.SessionID == "" || len(ev.SessionID) > 64 || !isASCIIID(ev.SessionID) {
			continue
		}
		ev.PagePath = capStr(ev.PagePath, trackMaxPath)
		if !strings.HasPrefix(ev.PagePath, "/") {
			continue
		}
		ev.PageTitle = capStr(ev.PageTitle, trackMaxStr)
		ev.Referrer = capStr(ev.Referrer, trackMaxStr)
		ev.UTMSource = capStr(ev.UTMSource, trackMaxStr)
		ev.UTMMedium = capStr(ev.UTMMedium, trackMaxStr)
		ev.UTMCampaign = capStr(ev.UTMCampaign, trackMaxStr)
		ev.ElementSelector = capStr(ev.ElementSelector, trackMaxStr)
		ev.ElementText = capStr(ev.ElementText, trackMaxElementText)
		// Coordinate sanity: x/y are viewport pixels; scroll_pct 0-100.
		// Negative or absurd values mean tampered/synthetic data — drop.
		if ev.X < 0 || ev.X > 100000 || ev.Y < 0 || ev.Y > 500000 {
			continue
		}
		// Scroll percentage out of range is tampered data, not a clamp
		// candidate — accept only a genuine 0-100 measurement.
		if ev.ScrollPct < 0 || ev.ScrollPct > 100 {
			continue
		}
		if ev.ViewportW < 0 || ev.ViewportW > 20000 || ev.ViewportH < 0 || ev.ViewportH > 20000 {
			continue
		}
		valid = append(valid, ev)
	}

	if len(valid) == 0 {
		w.WriteHeader(204)
		return
	}

	// Buffered async writer (Task 3) — respond immediately; a slow DB must
	// never hold a shopper request open. The writer owns dedup/flush.
	e.trackIngest(valid)

	w.WriteHeader(204)
}

// isASCIIID: visitor/session ids are client-generated UUID-ish strings.
// Restrict to a conservative charset — no unicode tricks, no whitespace.
func isASCIIID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func capStr(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// trackIngest persists validated events into analytics_events. Task 2
// ships a direct synchronous insert (one transaction per batch) so the
// endpoint is complete and testable; Task 3 moves the flush into a buffered
// background writer without changing the handler's contract.
//
// Failure posture: a DB error is logged and swallowed — tracking must never
// produce a 5xx that a storefront could surface, and the client has already
// been answered 204 by the time this runs in the Task 3 writer.
func (e *Engine) trackIngest(events []trackEvent) {
	if len(events) == 0 || e.Bus == nil {
		return
	}
	db := e.Bus.DB()

	// Ensure the full analytics schema (tables are pre-created by
	// EnsureTables with only _spine_id + created_at; these ALTERs add the
	// real columns idempotently — same mechanism db.insert uses).
	ensure := func(table string) error {
		return e.Bus.ensureTable(table, []string{
			`"id" TEXT`, `"visitor_id" TEXT`, `"session_id" TEXT`, `"event_type" TEXT`,
			`"page_path" TEXT`, `"page_title" TEXT`, `"referrer" TEXT`,
			`"utm_source" TEXT`, `"utm_medium" TEXT`, `"utm_campaign" TEXT`,
			`"x" REAL`, `"y" REAL`, `"scroll_pct" REAL`,
			`"viewport_w" INTEGER`, `"viewport_h" INTEGER`,
			`"element_selector" TEXT`, `"element_text" TEXT`,
			`"ip_hash" TEXT`, `"user_agent" TEXT`, `"device" TEXT`,
			`"country" TEXT`, `"created_at" INTEGER`,
		})
	}
	if err := ensure("analytics_events"); err != nil {
		log.Printf("[analytics] ingest: ensure schema: %v", err)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[analytics] ingest: begin tx: %v", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT INTO analytics_events
		(id, visitor_id, session_id, event_type, page_path, page_title, referrer,
		 utm_source, utm_medium, utm_campaign, x, y, scroll_pct, viewport_w, viewport_h,
		 element_selector, element_text, ip_hash, user_agent, device, country, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		log.Printf("[analytics] ingest: prepare: %v", err)
		return
	}
	defer stmt.Close()

	now := nowMillis()
	for _, ev := range events {
		// Server-resolved fields stay empty until Task 4 (ip_hash, UA).
		if _, err := stmt.Exec(generateUUID(), ev.VisitorID, ev.SessionID, ev.EventType,
			ev.PagePath, ev.PageTitle, ev.Referrer,
			ev.UTMSource, ev.UTMMedium, ev.UTMCampaign, ev.X, ev.Y, ev.ScrollPct,
			ev.ViewportW, ev.ViewportH, ev.ElementSelector, ev.ElementText,
			"", "", "", "", now); err != nil {
			log.Printf("[analytics] ingest: insert: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[analytics] ingest: commit: %v", err)
	}
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
