# NEXT-EPIC — CEO Loop Rulings

Current ruling on top. Written by the CEO persona (spine-ceo); the war room and
architect plan the HOW inside each brief. Replaced each cycle by the closed-loop
protocol. Full ledger in DECISION.md.

---

## Cycle 2 — 2026-08-25 | Epic: "Write-Path Durability" (batch-commit failure handling)

**What we build:** Close the engine's last durability gap. `bus.go:374` ignores
the batch `tx.Commit()` error and clears the batch on failure — every
`db.insert/update/upsert/delete` in that batch is silently lost, and this is
the only path ALL writes flow through. New behavior on Commit error: log loudly
with the failing SQL, RETAIN the batch (do not reset), back off briefly and
retry once or twice; if still failing, spill through a durable path
(outbox-style) or surface the error to the emit caller. Same treatment for the
`tx.Begin` fallback's ignored per-statement errors (bus.go:340-346).

**Why it serves the mission:** Quality #1, Safety #2. A merchant's write that
vanishes with no log is the worst failure class for a commerce backend — an
order placed that never lands, a stock decrement that silently didn't happen.
This is assessment priority #1 and the single remaining reliability gap between
Spine's performance story and its durability story.

**Values:** Quality (dominant), Safety (data integrity, money movement),
Performance (floor — retry logic lives on the failure path only; hot path
unchanged).

**Precondition:** the tree MUST be clean of the user's uncommitted WIP first —
this touches `pkg/engine/bus.go`, which currently carries 16 modified +
11 untracked WIP files on top of it. The user commits or branches the WIP, then
this epic runs.

**REJECTED this cycle:** the startup error-swallows (HIGH #2, query.go:376 /
outbox.go:23) fold in ONLY if the fix is trivially adjacent; otherwise they get
cycle 3. No WS/rate-limit work, no new deps, no README edits, no go.mod bump.

**Success criteria:**
1. Injected commit failure → loud log, batch retained, retry observed (black-box
   test in tests/ with a failure-injection seam).
2. No code path silently drops a write batch.
3. Full gate green INCLUDING `go test -race ./...` (engine change).
4. Zero user WIP files touched; commit scoped to engine files + tests + ledger.
