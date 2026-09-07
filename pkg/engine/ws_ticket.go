package engine

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// ws_ticket.go — one-time WebSocket connection tickets (security M5).
//
// Browsers cannot set custom headers on a WebSocket upgrade, so the only
// pre-ticket option was ?token=<API key> — which leaks the key into
// reverse-proxy access logs, browser history, and any intermediate
// logging. The ticket flow closes that hole:
//
//  1. browser: GET /ws-ticket with the key in X-API-Key (headers only)
//  2. engine:  constant-time key check → opaque 256-bit ticket, single
//     use, 30s TTL, bound to the caller's resolved AccessContext
//  3. browser: new WebSocket("wss://…/ws?ticket=<ticket>")
//  4. engine:  LoadAndDelete consumes the ticket on the upgrade — a
//     replayed or intercepted ticket is dead after the first use, and
//     the 30s life caps any brute-force window regardless
//
// The legacy ?token= path remains for backward compatibility but logs a
// deprecation warning on every use (see wsAuthCheck).

// wsTicketTTL is how long a /ws-ticket remains valid for its single use.
const wsTicketTTL = 30 * time.Second

// handleWSTicket issues a one-time WebSocket connection ticket to an
// already-authenticated caller. Registered through the full middleware
// chain (rate limit + security headers + logging + recovery).
func (e *Engine) handleWSTicket(w http.ResponseWriter, r *http.Request) {
	// Resolve the caller's identity exactly as /ws would — headers only.
	// Accepting ?token= here would defeat the ticket's whole purpose.
	var authenticated bool
	var access *AccessContext
	clientKey := extractAPIKey(r.Header.Get("X-API-Key"), r.Header.Get("Authorization"))
	if resolver := e.accessPtr.Load(); resolver != nil && resolver.HasRules() {
		access = resolver.Resolve(clientKey)
		authenticated = access != nil
	} else if e.APIKey == "" {
		authenticated = !e.authFailClosed.Load()
	} else {
		authenticated = subtle.ConstantTimeCompare([]byte(clientKey), []byte(e.APIKey)) == 1
	}
	if !authenticated {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  "unauthorized: invalid or missing API key",
		})
		return
	}

	// Sweep expired tickets opportunistically — bounded work per issuance,
	// no background goroutine needed for a map that only ever holds
	// (issue rate × 30s) entries.
	now := time.Now()
	e.wsTickets.Range(func(k, v any) bool {
		if v.(*wsTicket).expires.Before(now) {
			e.wsTickets.Delete(k)
		}
		return true
	})

	buf := make([]byte, 32) // 256 bits
	if _, err := rand.Read(buf); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "ticket generation failed"})
		return
	}
	ticket := hex.EncodeToString(buf)
	e.wsTickets.Store(ticket, &wsTicket{expires: now.Add(wsTicketTTL), access: access})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"ticket": ticket,
		"ttl":    int(wsTicketTTL.Seconds()),
	})
}
