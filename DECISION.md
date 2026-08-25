# Spine — Decision Ledger

Standing rulings, CEO Loop Log, and architect verdicts. Append-only; newest on
top within each section. Decisions here bind unless a later dated entry
overrides them.

## Standing rulings (CEO)

- 2026-08-25 — Direction autonomy delegated to the CEO persona (spine-ceo).
  The CEO decides what gets built, in what order, and what is REJECTED;
  epic-level rulings do not require user approval.
- 2026-08-25 — Core values in priority order: Quality, Safety, Ease of use,
  Simplicity, Performance-as-floor. Values 1-4 are constraints, never traded
  for speed or cleverness; value 5 is a floor.
- 2026-08-25 — Positioning: self-hosted Shopify alternative. Win on freedom,
  simplicity, and merchant ownership of data — never on matching Shopify's
  feature count. Spine is NOT an ERP, a headless CMS, or a website builder.

## CEO Loop Log

### 2026-08-25 | Cycle 1 — "CI Truthfulness" (toolchain pin) — SHIPPED

- **Epic:** CI sources the Go toolchain from go.mod via
  `actions/setup-go@v5` `go-version-file` in both jobs; removes the hardcoded
  `go-version: "1.24"` that only passed via GOTOOLCHAIN auto-download.
  Closes assessment LOW #13.
- **What shipped:** `.github/workflows/ci.yml` (2-line change),
  `NEXT-EPIC.md` (brief), this ledger. Commit: `ci(workflow): pin toolchain
  via go-version-file from go.mod`.
- **Gate results (real output):** `go build ./...` OK · `go vet ./...` OK ·
  `go test ./... -count=1` OK (tests suite 11.046s) · `doc-lint` OK ·
  actionlint exit 0. No race run needed (no engine change).
- **Architect verdict:** ADOPT (Benefit 3/5, Risk 1/5, Effort S) —
  assessments/2026-08-25-ci-toolchain-pin.md.
- **Rejected this cycle:** HIGH engine findings (batch-commit silent drop
  bus.go:374; startup error-swallows query.go:376 / outbox.go:23) — write-path
  surgery deferred until the tree is clean of the user's 27 uncommitted WIP
  files. WS rate-limit/conn-cap and README benchmark relabeling deferred
  (README is WIP-modified).
- **Open risks:** user WIP (16 modified + 11 untracked) still uncommitted on
  main; CI will only exercise the new pin once the workflow actually runs on
  GitHub (can't be proven from this machine — setup-go go-version-file
  behavior is documented but unobserved in this repo's runs).

### Next cycle (queue, in priority order)

1. HIGH: batch-commit failure handling (bus.go:374) — log + retain batch,
   bounded retry, spill path. Requires clean tree.
2. HIGH: fail-fast for `_spine_events` / `_spine_outbox` init (query.go:376,
   outbox.go:23) + reloadManifest EnsureTables error.
3. MEDIUM: WS rate limiter + connection cap + idle read deadline (engine.go:746).
4. MEDIUM: README benchmark labeling ("in-process", not network).
5. LOW: `git rm -r --cached` build junk (sdk/archive, egg-info, __pycache__) + .gitignore.
