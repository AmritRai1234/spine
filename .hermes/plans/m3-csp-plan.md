# M3 Plan — Content-Security-Policy Baseline

## What the SPA actually loads (verified in code)

- `web/index.html`: one external stylesheet (Google Fonts), one module script (`/src/main.tsx` → bundled by Vite)
- `web/src/`: **zero** `innerHTML`, `dangerouslySetInnerHTML`, `eval`, or `new Function` — no inline-script/eval dependency
- CSS loads fonts from `https://fonts.googleapis.com` (style-src) and the font files themselves from `https://fonts.gstatic.com` (font-src)
- Built output (`web/dist`) = plain JS/CSS asset files, no inline handlers
- The storefront template apps use the same shape (bundled assets + Google Fonts)

## Policy decision

`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'`

Rationale, directive by directive:
- `script-src 'self'` — the load-bearing one. No `'unsafe-inline'`, no `'unsafe-eval'` (verified: SPA uses neither). Any XSS now cannot execute.
- `style-src 'self' 'unsafe-inline' https://fonts.googleapis.com` — React inline `style=` attributes need `'unsafe-inline'`; Google Fonts needs the host. Style injection is a nuisance, not code execution.
- `font-src https://fonts.gstatic.com` — the font files.
- `img-src 'self' data:` — data-URL product images are a documented feature (SPINE_MAX_BODY_BYTES comment).
- `connect-src 'self'` — the SPA talks to same-origin /emit, /tables, /ws.
- `frame-ancestors 'none'` — belt to X-Frame-Options DENY (CSP level).
- `base-uri 'self'; form-action 'self'` — cheap hardening against injected base/action hijacks.

## Implementation

1. `pkg/middleware/security_headers.go`: add `Content-Security-Policy` +
   `Permissions-Policy: camera=(), microphone=(), geolocation=()` constants
   (exported so tests assert the exact policy text).
2. `SPINE_CSP_EXTRA` env: operator-append directive source (documented escape
   hatch for storefronts with legit third-party needs — Stripe checkout redirect
   is navigation, not embedded script, so it needs nothing; but an operator
   embedding e.g. a chat widget can add to style-src/script-src without
   rebuilding). Read per request (env-at-request-time law).
3. Static file responses get `Cache-Control` (M4 overlaps; CSP is what
   protects the admin origin, cache headers ride along where I'm already
   touching).

## Tests

- Unit: every wrapped response carries the exact CSP header; SPINE_METRICS
  endpoints included; SPINE_CSP_EXTRA appends correctly; junk env is ignored.
- Live: run bin/spine, curl /, /metrics, /oauth — assert header presence.

## Risk check

- If a future SPA feature needs inline `<script>`, CSP blocks it loudly in
  console — visible, correct-by-default behavior. This is the right default
  for an admin-bearing origin.
