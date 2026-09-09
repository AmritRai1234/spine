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
// the storefront): invalid payloads are dropped with a log line, a counter,
// and a 204. But every field is validated and length-capped BEFORE any DB
// write, and oversized bodies are rejected before parsing.
//
// Observability: because every failure mode answers 204 (by design, so a
// broken snippet can't break shopping), the ONLY signal that drops are
// happening is the spine_analytics_dropped_events counter on /metrics and
// the periodic [analytics] summary log. A snippet bug that silently
// rejects every batch shows up as dropped==sent with ingested==0.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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

	// Buffered writer sizing. Channel capacity bounds memory under a
	// flood (beyond it, events are dropped and COUNTED, never blocking
	// the HTTP handler); the flush interval trades write latency for
	// batch efficiency the same way the storefront's snippet batches.
	trackChanCap    = 4096
	trackFlushEvery = 500 * time.Millisecond
)

// trackEvent is one client-reported behavior event after validation.
// ip_hash / device / user_agent are resolved SERVER-SIDE (Task 4) — the
// client never sends identity signals (trust boundary).
type trackEvent struct {
	VisitorID       string  `json:"visitor_id"`
	SessionID       string  `json:"session_id"`
	EventType       string  `json:"event_type"`
	PagePath        string  `json:"page_path"`
	PageTitle       string  `json:"page_title"`
	Referrer        string  `json:"referrer"`
	UTMSource       string  `json:"utm_source"`
	UTMMedium       string  `json:"utm_medium"`
	UTMCampaign     string  `json:"utm_campaign"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
	ScrollPct       float64 `json:"scroll_pct"`
	ViewportW       int     `json:"viewport_w"`
	ViewportH       int     `json:"viewport_h"`
	ElementSelector string  `json:"element_selector"`
	ElementText     string  `json:"element_text"`
	// Server-resolved (Task 4) — never client-supplied.
	IPHash    string `json:"-"`
	UserAgent string `json:"-"`
	Device    string `json:"-"`
	Country   string `json:"-"`
}

var trackEventTypes = map[string]bool{
	"pageview": true,
	"click":    true,
	"scroll":   true,
	"custom":   true,
}

// analyticsCounters is the observability surface for the fail-open ingest.
// 204-on-drop means the CLIENT never sees a failure — these counters are
// the operator's only window. Exposed on /metrics as
// spine_analytics_{ingested,dropped}_events and summarized in a periodic
// log line (every trackSummaryEvery) so a silently-broken snippet
// (dropped ≈ sent, ingested ≈ 0) is visible within minutes, not weeks.
type analyticsCounters struct {
	mu       sync.Mutex
	ingested uint64
	dropped  uint64 // validation rejects + overflow drops + DB failures
}

func (c *analyticsCounters) addIngested(n uint64) {
	c.mu.Lock()
	c.ingested += n
	c.mu.Unlock()
}

func (c *analyticsCounters) addDropped(n uint64) {
	c.mu.Lock()
	c.dropped += n
	c.mu.Unlock()
}

func (c *analyticsCounters) snapshot() (uint64, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ingested, c.dropped
}

const trackSummaryEvery = 5 * time.Minute

// Retention sweep cadence. Raw events are the privacy-sensitive tier;
// the sweep prunes them hourly (cheap DELETE on an indexed column).
const trackSweepEvery = time.Hour

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

	dropped := len(batch.Events) - len(valid)
	if dropped > 0 {
		e.trackCounters.addDropped(uint64(dropped))
	}

	if len(valid) == 0 {
		w.WriteHeader(204)
		return
	}

	// Server-side context resolution BEFORE enqueueing: ip_hash, UA
	// family, device. The client never supplies these fields.
	e.resolveTrackContext(r, valid)

	// Buffered async writer — respond immediately; a slow DB must never
	// hold a shopper request open. The writer owns the flush.
	select {
	case e.trackCh <- valid:
	default:
		// Channel saturated: drop rather than block the shopper, but
		// COUNT it — a saturated channel under normal traffic is a
		// sizing bug, invisible otherwise.
		e.trackCounters.addDropped(uint64(len(valid)))
		log.Printf("[analytics] /track: channel saturated, %d events dropped", len(valid))
	}

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

// startTrackWriter launches the background flush goroutine and the
// periodic observability summary. Called once from New.
func (e *Engine) startTrackWriter() {
	e.trackCh = make(chan []trackEvent, trackChanCap)
	e.trackCounters = &analyticsCounters{}
	e.trackWg.Add(2)

	// Flush loop: batches events into one transaction per flush.
	go func() {
		defer e.trackWg.Done()
		ticker := time.NewTicker(trackFlushEvery)
		defer ticker.Stop()
		pending := make([]trackEvent, 0, 256)
		for {
			select {
			case batch := <-e.trackCh:
				pending = append(pending, batch...)
			case <-ticker.C:
			case <-e.trackStop:
				// Drain what's left, then exit.
				for {
					select {
					case batch := <-e.trackCh:
						pending = append(pending, batch...)
					default:
						e.flushTrack(pending)
						return
					}
				}
			}
			if len(pending) > 0 {
				e.flushTrack(pending)
				pending = make([]trackEvent, 0, 256)
			}
		}
	}()

	// Summary loop: the operator's signal that drop behavior exists and
	// how often. A healthy deployment logs ingested ≈ sent, dropped ≈ 0.
	go func() {
		defer e.trackWg.Done()
		ticker := time.NewTicker(trackSummaryEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ing, drop := e.trackCounters.snapshot()
				log.Printf("[analytics] summary: ingested=%d dropped=%d", ing, drop)
			case <-e.trackStop:
				return
			}
		}
	}()

	// Retention sweep: raw events are PII-adjacent (ip_hash, UA family) —
	// they age out on a short leash. Session rollups are aggregates with
	// no direct identifier and are kept indefinitely. Runs hourly.
	go func() {
		defer e.trackWg.Done()
		ticker := time.NewTicker(trackSweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				e.sweepAnalytics()
			case <-e.trackStop:
				return
			}
		}
	}()
}

// analyticsRetentionDays returns the raw-event retention window. Default
// 180 days; ANALYTICS_RETENTION_DAYS overrides (0/negative/garbage →
// default, a misconfigured env var must not disable retention entirely —
// that would grow the table unboundedly).
func analyticsRetentionDays() int {
	if v := os.Getenv("ANALYTICS_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 180
}

// sweepAnalytics deletes raw events older than the retention window.
// Rollups (analytics_sessions) are kept — they're aggregates, not
// event-level data. Errors are logged; the next tick retries. Returns the
// number of rows deleted (for tests).
func (e *Engine) sweepAnalytics() int {
	if e.Bus == nil {
		return 0
	}
	days := analyticsRetentionDays()
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	res, err := e.Bus.DB().Exec(`DELETE FROM analytics_events WHERE created_at < ?`, cutoff)
	if err != nil {
		log.Printf("[analytics] sweep: %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[analytics] sweep: pruned %d events older than %d days", n, days)
	}
	return int(n)
}

func (e *Engine) stopTrackWriter() {
	if e.trackStop != nil {
		close(e.trackStop)
		e.trackWg.Wait()
	}
}

// flushTrack persists a batch of validated events + session rollups in one
// transaction. Failure posture: a DB error is logged, counted as dropped,
// and swallowed — tracking must never produce a 5xx that a storefront could
// surface, and the client has already been answered 204.
func (e *Engine) trackIngestDirect(events []trackEvent) { e.flushTrack(events) }

func (e *Engine) flushTrack(events []trackEvent) {
	if len(events) == 0 || e.Bus == nil {
		return
	}
	db := e.Bus.DB()

	if err := e.ensureAnalyticsSchema(); err != nil {
		log.Printf("[analytics] flush: ensure schema: %v", err)
		e.trackCounters.addDropped(uint64(len(events)))
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[analytics] flush: begin tx: %v", err)
		e.trackCounters.addDropped(uint64(len(events)))
		return
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT INTO analytics_events
		(id, visitor_id, session_id, event_type, page_path, page_title, referrer,
		 utm_source, utm_medium, utm_campaign, x, y, scroll_pct, viewport_w, viewport_h,
		 element_selector, element_text, ip_hash, user_agent, device, country, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		log.Printf("[analytics] flush: prepare: %v", err)
		e.trackCounters.addDropped(uint64(len(events)))
		return
	}
	defer stmt.Close()

	sStmt, err := tx.Prepare(`INSERT INTO analytics_sessions
		(session_id, visitor_id, started_at, ended_at, pageviews, entry_path, exit_path, converted, device)
		VALUES (?,?,?,?,?,?,?,0,'')
		ON CONFLICT(session_id) DO UPDATE SET
			ended_at = excluded.ended_at,
			pageviews = pageviews + excluded.pageviews,
			exit_path = excluded.exit_path`)
	if err != nil {
		log.Printf("[analytics] flush: prepare session: %v", err)
		e.trackCounters.addDropped(uint64(len(events)))
		return
	}
	defer sStmt.Close()

	now := nowMillis()
	sessionDeltas := make(map[string]*trackEvent) // first event per session
	for _, ev := range events {
		if _, err := stmt.Exec(generateUUID(), ev.VisitorID, ev.SessionID, ev.EventType,
			ev.PagePath, ev.PageTitle, ev.Referrer,
			ev.UTMSource, ev.UTMMedium, ev.UTMCampaign, ev.X, ev.Y, ev.ScrollPct,
			ev.ViewportW, ev.ViewportH, ev.ElementSelector, ev.ElementText,
			ev.IPHash, ev.UserAgent, ev.Device, ev.Country, now); err != nil {
			log.Printf("[analytics] flush: insert: %v", err)
			e.trackCounters.addDropped(uint64(len(events)))
			return
		}
		// Every event type advances the session (clicks/scrolls extend
		// it and carry the exit path) — not just pageviews.
		if _, ok := sessionDeltas[ev.SessionID]; !ok {
			cp := ev
			sessionDeltas[ev.SessionID] = &cp
		}
	}

	// Session rollup: insert-or-advance in the same transaction. The
	// ON CONFLICT upsert keeps concurrent writers idempotent per session.
	for sid, first := range sessionDeltas {
		if _, err := sStmt.Exec(sid, first.VisitorID, now, now, 1, first.PagePath, first.PagePath); err != nil {
			log.Printf("[analytics] flush: session upsert: %v", err)
			// Session failure alone doesn't drop the events already
			// inserted in this tx — but SQLite tx semantics mean we must
			// roll back; count everything dropped and abort.
			e.trackCounters.addDropped(uint64(len(events)))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[analytics] flush: commit: %v", err)
		e.trackCounters.addDropped(uint64(len(events)))
		return
	}
	e.trackCounters.addIngested(uint64(len(events)))
}

// ensureAnalyticsSchema creates/extends the analytics tables. Tables are
// pre-created by EnsureTables with only _spine_id + created_at; these
// ALTERs add the real columns idempotently — same mechanism db.insert uses.
func (e *Engine) ensureAnalyticsSchema() error {
	ensure := func(table string, extra []string) error {
		return e.Bus.ensureTable(table, append([]string{
			`"id" TEXT`, `"visitor_id" TEXT`, `"session_id" TEXT`, `"event_type" TEXT`,
			`"page_path" TEXT`, `"page_title" TEXT`, `"referrer" TEXT`,
			`"utm_source" TEXT`, `"utm_medium" TEXT`, `"utm_campaign" TEXT`,
			`"x" REAL`, `"y" REAL`, `"scroll_pct" REAL`,
			`"viewport_w" INTEGER`, `"viewport_h" INTEGER`,
			`"element_selector" TEXT`, `"element_text" TEXT`,
			`"ip_hash" TEXT`, `"user_agent" TEXT`, `"device" TEXT`,
			`"country" TEXT`, `"created_at" INTEGER`,
		}, extra...))
	}
	if err := ensure("analytics_events", nil); err != nil {
		return err
	}
	// session_id carries the UNIQUE constraint the ON CONFLICT upsert
	// requires — a plain ALTER path can't retrofit constraints onto an
	// existing table, so it's declared in the column definition (fresh
	// CREATE) and enforced by the UNIQUE index below (existing tables).
	// Idempotent schema evolution for the conversion join: conversion_key
	// records WHICH key produced the attribution ('visitor' = exact
	// visitor_id match, 'ip' = ip_hash + 24h fallback, '' = not
	// converted), so the dashboard can show the approximate fraction.
	if err := e.Bus.ensureTable("analytics_sessions", []string{
		`"session_id" TEXT`,
		`"visitor_id" TEXT`,
		`"started_at" INTEGER`, `"ended_at" INTEGER`,
		`"pageviews" INTEGER`, `"entry_path" TEXT`, `"exit_path" TEXT`,
		`"converted" INTEGER`, `"device" TEXT`,
		`"conversion_key" TEXT`,
	}); err != nil {
		return err
	}
	// UNIQUE index is the upsert's conflict target. SQLite cannot
	// ALTER-add a UNIQUE column, so the constraint lives here (idempotent,
	// applies to both fresh and pre-existing tables) instead of the column
	// definition — and doubles as the session_id lookup index.
	if _, err := e.Bus.DB().Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_analytics_sessions_sid ON analytics_sessions(session_id)`); err != nil {
		return err
	}
	return nil
}

// conversionJoin marks the analytics session that produced an order as
// converted. Attribution authority, explicitly:
//
//  1. PRIMARY: visitor_id. The storefront snippet's first-party localStorage
//     UUID flows through every analytics event. A session whose visitor_id
//     matches the ordering visitor is a TRUE match — no approximation.
//  2. FALLBACK: ip_hash + 24h window, used ONLY when no session carries the
//     visitor's UUID (first-touch before localStorage was set, or storage
//     cleared mid-session). Shared IPs (CGNAT, hotel wifi, office networks)
//     make this ambiguous, so the join records conversion_key='ip' — the
//     dashboard surfaces what fraction of conversions are approximate.
//
// At most ONE session is marked per order (no double-counting). Fires on
// ORDER_CREATED via the Bus event hook; never blocks or fails the emit.
func (e *Engine) conversionJoin(payload map[string]interface{}) {
	if e.Bus == nil {
		return
	}
	visitorID, _ := payload["visitor_id"].(string)
	ipHash, _ := payload["ip_hash"].(string)
	if visitorID == "" && ipHash == "" {
		return // nothing to join on
	}

	db := e.Bus.DB()
	if err := e.ensureAnalyticsSchema(); err != nil {
		log.Printf("[analytics] conversion: schema: %v", err)
		return
	}

	// 1) Exact visitor match (most recent session for this visitor).
	if visitorID != "" {
		res, err := db.Exec(`UPDATE analytics_sessions SET converted = 1, conversion_key = 'visitor'
			WHERE visitor_id = ? AND converted = 0
			AND session_id = (SELECT session_id FROM analytics_sessions WHERE visitor_id = ? ORDER BY started_at DESC LIMIT 1)`,
			visitorID, visitorID)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				return // exact match won — no fallback
			}
		} else {
			log.Printf("[analytics] conversion: visitor join: %v", err)
		}
	}

	// 2) Fallback: ip_hash within a 24h lookback. Marks the most recent
	// unconverted session from that hash — approximate by definition.
	if ipHash != "" {
		cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
		res, err := db.Exec(`UPDATE analytics_sessions SET converted = 1, conversion_key = 'ip'
			WHERE session_id = (SELECT session_id FROM analytics_sessions
				WHERE visitor_id IN (SELECT DISTINCT visitor_id FROM analytics_events WHERE ip_hash = ? AND created_at >= ?)
				 AND converted = 0
				ORDER BY started_at DESC LIMIT 1)`,
			ipHash, cutoff)
		if err != nil {
			log.Printf("[analytics] conversion: ip join: %v", err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[analytics] conversion: session marked via ip_hash fallback (approximate)")
		}
	}
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
