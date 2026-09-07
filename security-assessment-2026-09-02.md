# Spine Framework — Security Assessment
**Date:** 2026-09-02 · **Repo:** ~/spine/spine (main, v3.0.5+) · **Scope:** web layer (HTTP/WS/middleware/auth/DB surface) — assessment only, no code changes.

**UPDATE 2026-09-07 (v3.0.6+):** M1, M2, M3 fixed earlier (commits cf40c93, 682c80e, 5196c74). Now also fixed:
- **M4** — static serving: `web/dist` only (source `web/` tree never served), through the public-browser middleware chain, SPA fallback, hashed-asset immutable caching, missing assets 404 (never HTML), traversal containment (`pkg/engine/static.go`).
- **M5** — WS one-time tickets: `GET /ws-ticket` exchanges a header-carried key for a single-use 256-bit ticket (30s TTL, bound to resolved AccessContext); `/ws?ticket=` consumes it via LoadAndDelete. Legacy `?token=` still works but logs a deprecation warning on every use (`pkg/engine/ws_ticket.go`).
- **L5** — `/readyz` now behind the shared per-IP rate limiter (DB-pinging endpoint was the only health path worth flood-protecting; `/health`, `/healthz` stay unlimited for LB probes).
- **L6** — `DynamicCORSMiddleware` re-reads `SPINE_CORS_ORIGINS` per request (rebuilds only on change) — CORS hot-reload now matches WS-origins semantics.
- **L7** — audit-log secret masking is suffix-based (`*secret` / `*key` / `*token`, case-insensitive) instead of a two-entry allowlist; short values mask entirely, longer keep a 4-char tail.

Remaining open: L1, L2, L3, L4, L8 (documented trade-offs / minor; see below).

---

## Overall posture

Strong for a self-hosted framework. The last two hardening rounds (2026-08 audit + v2) already fixed the big classes: constant-time key compares, HMAC webhook verification with replay guard, WS origin deny-by-default, fail-closed auth, trusted-proxy-validated XFF, body/depth limits, SQL always parameterized with sanitized identifiers, encrypted-at-rest social token vault, TLS 1.2 floor with ACME, graceful-shutdown correctness, bcrypt with timing-equalized login.

What remains is a set of medium/low findings plus a few structural gaps.

---

## Findings

### HIGH
*(none — no critical issues found in the current web layer)*

### MEDIUM

**M1. `/metrics` is unauthenticated and unrated.**
`engine.go:661` serves Prometheus text with no `wrapMiddleware`, so no API key, no rate limit, no body limit, CORS chain, or security headers. Anyone on the network can scrape RPS, batch sizes, mode, WS client counts — useful recon before an attack. Behind the prod reverse proxy this may also be publicly reachable. Fix: put it behind auth (or bind to localhost / make opt-in `SPINE_METRICS_PUBLIC=1`), and route it through the standard middleware chain.

**M2. `/oauth/*` callback bypasses every middleware layer.**
`engine.go:612` registers the handler raw: no rate limiting, no security headers, no logging/recovery. The OAuth state token is single-use + 15-min TTL (good CSRF guard) and the open-redirect guard checks `https://`/`http://` prefix — but:
- No rate limit ⇒ an attacker can brute-force state tokens at wire speed within the 15-min window. State entropy is the only defense; add IP rate limiting at minimum.
- Open-redirect guard is prefix-based: `https://evil.com#...` passes. It's set by the admin at connect time, so exploitability is low, but a parsed-URL host check would be stricter.
- The `returnTo` check should also reject credentials in URL (`user@host`), which the prefix check misses.

**M3. No `Content-Security-Policy` header.**
`security_headers.go` sets nosniff, X-Frame-Options, HSTS, Referrer-Policy — but no CSP (even a simple `default-src 'self'`) and no `Permissions-Policy`. The engine serves the admin SPA from the same origin (`web/dist`, `engine.go:1238-1245`), so a stored-XSS hole anywhere in the dashboard gets full API-key access from localStorage/session. A baseline CSP is cheap and high-value for an admin-bearing origin.

**M4. Static file serving has no cache-control/path hardening and serves directory roots.**
`http.FileServer(http.Dir("web"))` fallback (engine.go:1243) will happily serve the un-minified `web/` tree including any dev files committed there (`*.map`, `.env`-like artifacts if ever present, source). It also doesn't set `Cache-Control`/`X-Content-Type-Options` beyond the global middleware (which `mux.Handle("/")` bypasses entirely — see M6). Prefer serving only `web/dist` and wrapping with the security/logging chain.

**M5. WS auth accepts the API key as a query parameter (`?token=`).**
`wsAuthCheck` (engine.go:502-510) supports `?token=` because browsers can't set WS headers. Query strings leak into reverse-proxy access logs, browser history, and any intermediate logging. It's a known trade-off, but for production deployments log-scrubbing or a short-lived one-time WS ticket (`GET /ws-ticket` with the key in a header → 30s ticket → connect) would close it.

### LOW

**L1. AuthMiddleware compares before checking empty client key.** If `apiKey` is configured but the client sends no key at all, the compare is constant-time against empty — fine — but the response is identical to wrong-key, which is correct. No action; noting the only theoretical leak is key-length via `len()` check in `AccessResolver.Resolve` (length comparison before compare leaks length only — negligible).

**L2. `AccessResolver.Resolve` keeps scanning all rules after a match** — deliberate (timing equalization), but it also means a request matching *multiple* keys silently takes the LAST rule in manifest order. Document that duplicate keys are undefined behavior or reject them at manifest parse time.

**L3. `maxRequestBodySize` is captured at package init** (`var maxRequestBodySize = maxBodyBytesFromEnv(...)`). This is the exact anti-pattern flagged in the skill pitfalls: env read in package-level var, untestable, and hot-reload can't change it. Same class of bug fixed for `SPINE_WS_ORIGINS` and `SPINE_ALLOW_UNSIGNED_WEBHOOKS`.

**L4. Webhook path trusts `payload["id"]` as idempotency key** (engine.go:591). A malicious provider payload with a forged-but-valid signature can replay with the *same* id but different body and get deduped silently, or rotate ids to bypass dedup. Signature verification is the real control; the id-stamp is convenience. Document that dedup strength = provider id integrity.

**L5. `/health`, `/healthz`, `/readyz` are unauthenticated by design** — fine, but they currently sit outside the rate limiter too. Cheap to keep, but a per-IP limit would stop health-check floods from shared hosts.

**L6. CORS default allowlist `*` with no credentials is safe**, but `DefaultCORSOptions()` reads env at call time (good) and the wildcard branch at `engine.go:426` is built once at mux-build time — hot-reload of `SPINE_CORS_ORIGINS` will NOT take effect until restart, unlike WS origins which are re-read per request. Inconsistent semantics; align by re-reading per request.

**L7. Audit-log secret masking is a hardcoded allowlist** (`logEventAudit`, query.go:455-464): only `stripe_secret` and `webhook_secret` are masked. New credential-bearing actions (social client secrets — those never persist, good — but also any future API keys written via `set:` into admin events) will land in `_spine_events` plaintext. Consider a suffix-convention mask (`*_secret`, `*_key`, `*_token`) instead of an allowlist.

**L8. WS per-IP connection cap default 10,000** is generous for a single-box store; combined with unauthenticated `/ws` upgrade (auth happens post-upgrade in-frame), an attacker can hold 10k upgrade sockets doing nothing. Consider authenticating during the HTTP upgrade handshake itself (the key is available via query/header there) so unauthenticated sockets never enter the hub.

---

## What's already right (don't regress)

- Constant-time API-key compare everywhere (auth middleware, access resolver, WS auth).
- HMAC-SHA256 webhook verification, constant-time, 300s replay window, fail-closed 503 with explicit opt-out env.
- WS origin deny-by-default, per-request env re-read, port-aware same-origin, X-Forwarded-Host never trusted.
- XFF honored only from trusted proxies; default = ignore spoofable headers.
- SQL: 100% parameterized values; identifiers through `sanitizeIdent`; FTS MATCH parameterized.
- bcrypt with dummy-hash timing equalization; 72-byte rejection rather than silent truncation.
- Body limit (1MB), depth limit (32), JSON-bomb resistance, WS max message size matched to HTTP cap.
- AES-256-GCM token vault with no default key (secure-by-default: unset = memory-only).
- TLS 1.2 floor, ACME HostPolicy (no wildcard confusion), HSTS with includeSubDomains.
- Graceful shutdown draining all listeners (no leaked-port half-open states).
- Fail-closed when no auth configured at all.

---

## Priority recommendation (when we do code work)

1. M1 metrics auth (one-line wrap)
2. M2 /oauth through the rate-limit + logging chain
3. M3 baseline CSP header
4. M6/L6 middleware consistency for static + CORS hot-reload
5. L7 suffix-based secret masking
6. L8 WS upgrade-time auth
7. L3 env-at-init fix (testability)
