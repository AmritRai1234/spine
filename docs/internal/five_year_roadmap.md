# Spine — 5-Year Strategic Roadmap

> Based on a deep read of the current codebase (~10K lines, v3.0.1 — last
> revised with the v3.0.1 hardening pass).
> The theme: **depth over breadth, trust over hype.**

---

## ✅ Already shipped (as of v3.0.1) — remove from future planning

Items this roadmap previously listed as *future work* that the codebase now ships:

- **Row-level access control** — `access:` rules in manifests (roles, keys, read_only, event whitelists, table filters), enforced on emit, table queries, `/events`, and WS broadcasts.
- **Request validation hardening** — global rate limiting (per-IP token bucket), per-route caps on `/ws`, body-size and JSON-depth limits, per-IP WS connection caps.
- **Outbox processor tests + fixes** — full retry/backoff/lease coverage; the retry loop was found and fixed (no more self-perpetuating re-enqueue).
- **WebSocket reconnection protocol** — auth-gated replay (`GetEventsSince`) with a working cursor: broadcasts carry the audit id, reconnect replays only missed events.
- **`spine dev` command** — hot-reload dev server with manifest watching.
- **Error messages that teach** — "did you mean?" suggestions on unknown keys/events.
- **`spine init`** — scaffolds with generated secrets.
- **`spine codegen`** — TypeScript type generation from manifests.
- **Full-text search** — FTS5 auto-provisioned index on SQLite/Turso (was non-functional; now real).
- **Event replay** — `spine replay` CLI (dry-run supported).
- **Benchmark honesty** — the comparative runner now measures (no hardcoded winners); README discloses enqueue-vs-durable semantics.
- **Durability hardening** — batch-writer statement-failure accounting (`spine_stmt_failures`), spill delete-after-commit, idempotency marked complete only after a durability fence, saga compensation ordering.

---

## Year 1 — Harden the Core (2026–2027)

*Goal: Make Spine something people trust in production for single-node workloads.*

### Security & Reliability
- **Row-level access control** — `access:` rules in manifests so different API keys see different data. Right now auth is all-or-nothing.
- **Request validation hardening** — Rate limit per route (not just global), request size limits per table, and payload depth limits to prevent nested JSON bombs.
- **Outbox processor tests** — The outbox retry system (`outbox.go`) has zero test coverage. Add tests for: partial failure recovery, exponential backoff correctness, concurrent retry races.
- **WebSocket reconnection protocol** — Define a proper reconnect handshake with state replay so clients don't miss events during network blips. Right now a disconnect = lost state.

### Developer Experience
- **`spine dev` command** — Hot-reload dev server with colored log output, request inspector, and a built-in terminal UI showing live event flow (like `railway logs` but for Spine events).
- **Error messages that teach** — When a manifest has a typo, show "did you mean?" suggestions. When a route references a missing event, show which nodes emit what. The parser already has the data for this.
- **`spine init`** — Scaffold a new project with a starter manifest, example routes, and a docker-compose.yml.

### Performance (Real Numbers)
- **Benchmark honesty** — Create a proper benchmark suite that measures end-to-end latency including SQLite WAL fsync, not just channel throughput. Publish p50/p95/p99 numbers.
- **WAL checkpoint tuning** — The current `wal_autocheckpoint=10000` is aggressive. Add adaptive checkpointing that runs during low-traffic windows to avoid write stalls.

---

## Year 2 — The TypeScript Moment (2027–2028)

*Goal: Make Spine the fastest way to build real-time apps. Kill the 5 thin SDKs, ship one great one.*

### First-Class TypeScript SDK
- **Drop** the Python, Swift, Kotlin, and GTK4 SDKs (or archive them). They're thin `fetch()` wrappers that nobody will maintain.
- **Build a proper `@spine/client` package** with:
  - Type-safe event emission generated from `.spine` manifests
  - Reactive state subscriptions (like Zustand/Jotai but backed by Spine WS)
  - Automatic reconnection with event replay
  - Optimistic updates with rollback on failure routes
- **`spine codegen`** — Generate TypeScript types from manifest schemas. If a node emits `USER_LOGIN` with `{email: string, role: string}`, generate the corresponding TS interface.

### Query Layer
- **GraphQL or REST query builder** — Right now `GET /tables/:name` is a raw SQL dump. Add filtered queries, joins across tables, and cursor-based pagination for real app use cases.
- **Computed views** — Manifest-defined read models that auto-update when underlying tables change. Like materialized views but driven by Spine events.
- **Full-text search** — SQLite FTS5 integration triggered by manifest config. `search: true` on a table column auto-creates the FTS index.

### Observability
- **OpenTelemetry integration** — Trace spans for every Emit → route → step → db write. Export to Jaeger/Grafana. The adaptive optimizer already tracks RPS — expose it as Prometheus metrics.
- **Event replay & debugging** — `spine replay --event USER_LOGIN --from 2027-01-01` to replay historical events through current routes for debugging.

---

## Year 3 — Beyond Single Node (2028–2029)

*Goal: Make Spine work for teams building real products, not just solo prototypes.*

### Multi-Node Architecture
- **Turso-first persistence** — The Turso driver already exists but is untested at scale. Make it the recommended production backend with edge replication. SQLite stays for dev/local.
- **Event streaming bridge** — `emit_to:` config that forwards events to NATS, Kafka, or Redis Streams. Spine stays the orchestrator, but events can fan out to external consumers.
- **Idempotency keys** — Every emit gets a dedup key. Re-emitting the same event with the same key is a no-op. Critical for at-least-once delivery in distributed setups.

### Multi-Tenancy
- **Tenant isolation** — `tenant:` field in manifests. Each tenant gets isolated tables (or schema prefixes). API keys are scoped to tenants.
- **Usage metering** — Track events/sec, storage bytes, and WS connections per tenant. Expose via `/admin/usage` endpoint.

### Manifest Evolution
- **Manifest versioning & migrations** — When you change a route, Spine should diff the old and new manifests and apply changes incrementally (not just "restart and hope"). Schema migrations already exist — extend this to route topology.
- **Manifest testing** — `spine test app.spine` that runs manifest-defined test scenarios: "emit X, assert table Y has row Z, assert state S was broadcast."
- **Environment overlays** — `app.spine` + `app.prod.spine` that overrides specific routes/config for production (like Kubernetes overlays).

---

## Year 4 — Platform (2029–2030)

*Goal: Spine becomes a platform other tools integrate with, not just a standalone binary.*

### Plugin Ecosystem
- **WASM action plugins** — `action: wasm.run` that executes a WASM module as a route step. Users write custom logic in Rust/Go/TS, compile to WASM, and Spine runs it sandboxed. Way more powerful than the current Go-only `RegisterAction`.
- **Plugin registry** — A curated list of community actions: `stripe.charge`, `sendgrid.email`, `twilio.sms`, `s3.upload`. Install with `spine plugin add stripe`.
- **Webhook ingestion** — `spine listen --webhook stripe` that registers a webhook endpoint and auto-maps Stripe events to Spine events.

### Dashboard & Admin
- **Ship the web dashboard** — The `web/` directory has a Vite scaffold. Build it into a real admin panel: live event stream, table browser, route visualizer, and manifest editor with syntax highlighting.
- **Visual route builder** — Drag-and-drop route editor that generates `.spine` manifests. Lower the barrier for non-developers.

### Deployment
- **`spine deploy`** — One-command deployment to Fly.io, Railway, or Render. Spine is a single binary with SQLite — it's the perfect fit for edge deployment.
- **Spine Cloud (optional)** — Managed hosting with automatic Turso replication, built-in auth, and usage dashboards. Monetization path if you want one.

---

## Year 5 — Ecosystem Maturity (2030–2031)

*Goal: Spine is the default answer for "I need a real-time event-driven backend and I don't want to build infrastructure."*

### Community & Adoption
- **Starter templates** — `spine init --template chat`, `spine init --template dashboard`, `spine init --template iot`. Complete working apps, not just hello-world.
- **Documentation site** — Move from README to a proper docs site with tutorials, API reference, and architecture guides.
- **Conference talks & case studies** — Real production stories from teams using Spine.

### Advanced Features
- **Saga/workflow engine** — Multi-step transactions with compensation logic. If step 3 fails, automatically run undo steps for 1 and 2. The `on_failure` mechanism is a primitive version of this — evolve it.
- **Scheduled events** — `cron: "0 * * * *"` on routes for time-triggered event emission. Useful for batch jobs, report generation, cleanup tasks.
- **Event sourcing mode** — Optional mode where tables are append-only event logs and current state is computed from projections. The audit log (`_spine_events`) is already halfway there.

---

## Priority Matrix

| Impact | Effort | Do First |
|---|---|---|
| 🟢 TypeScript SDK + codegen | Medium | **Year 1-2 — this is the growth lever** |
| 🟢 Row-level access control | Medium | **Year 1 — blocks production use** |
| 🟢 Outbox + WebSocket tests | Low | **Year 1 — low effort, high trust** |
| 🟡 OpenTelemetry tracing | Medium | Year 2 |
| 🟡 WASM plugins | High | Year 3-4 |
| 🟡 Multi-tenancy | High | Year 3 |
| 🔴 Spine Cloud | Very High | Year 4-5 (only if adoption warrants) |

---

## The One Thing That Matters Most

If I had to pick **one thing** to build next, it would be the **TypeScript SDK with codegen**.

Why: Spine's value proposition is "define your backend in a manifest." But right now, the manifest types don't flow through to the client. You declare `email: string` in your `.spine` file, and then you write `payload.email` in your JavaScript with zero type safety. That's the gap. Close it, and Spine becomes genuinely compelling — a type-safe, real-time backend from a single config file.

Everything else is optimization. The TypeScript bridge is the product.
