# NEXT-EPIC — CEO Loop Rulings

Current ruling on top. Written by the CEO persona (spine-ceo); the war room and
architect plan the HOW inside each brief. Replaced each cycle by the closed-loop
protocol.

---

## Cycle 1 — 2026-08-25 | Epic: "CI Truthfulness" (toolchain pin)

**What we build:** CI stops lying about its toolchain. Replace the hardcoded
`go-version: "1.24"` in both jobs of `.github/workflows/ci.yml` with
`go-version-file: go.mod`, so GitHub Actions reads the Go version from the
manifest of record — one source of truth, no drift, no silent auto-download
crutch.

**Why it serves the mission:** Spine is a self-hosted Shopify alternative whose
whole pitch is trust — merchants own their data and their runtime. A CI that
only passes because GOTOOLCHAIN auto-downloads a different version than it
declares is a quality lie: it masks real breakage, wastes minutes per run
fetching a second toolchain, and breaks the day runner defaults change. CI must
verify exactly what we ship. (Closes assessment LOW #13:
"CI pins Go 1.24 while go.mod requires 1.25.0".)

**Values:** Quality #1 (dominant — truthful CI is the foundation of "no
known-broken ships"), Simplicity #4 (one source of truth for the Go version,
replacing a duplicated magic constant).

**REJECTED this cycle (complexity budget):** the two HIGH engine findings —
batch-commit silent drop (bus.go:374) and startup error-swallows
(query.go initEventTable, outbox.go initOutboxTable) — because they touch the
async write path while 27 files of the user's uncommitted WIP sit on top of it;
engine surgery on a moving tree is how known-broken ships happen. Queue
position 1 for the next clean-tree cycle. Also rejected: WS rate-limit /
connection cap, README benchmark relabeling (README is WIP-modified), any new
dependency, any go.mod bump, any edit to the 16 modified / 11 untracked WIP
files.

**Success criteria:**
1. Both ci.yml jobs source the Go version from go.mod — no hardcoded version remains.
2. Workflow YAML validates (actionlint).
3. Local gate green: `go build ./... && go vet ./... && go test ./...`.
4. Commit contains ONLY this cycle's files: `.github/workflows/ci.yml`, `NEXT-EPIC.md`, `DECISION.md`.
5. Zero user WIP files touched, staged, or reverted.
