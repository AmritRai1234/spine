# Spine Product Roadmap

Declarative Event-Driven Backend Engine

---

## v1.0 — Foundation ✅ *SHIPPED*

The core runtime. Single binary, manifest-driven, production-ready.

- [x] `.spine` manifest parser (state machine, line-by-line)
- [x] Event dispatch bus with route matching
- [x] Schema-based payload validation (`string`, `number`, `boolean`)
- [x] SQLite persistence with WAL mode
- [x] WebSocket real-time state broadcasting
- [x] Hot manifest reloading (file watcher, zero-downtime)
- [x] `GET /schema` — live introspection API
- [x] `GET /health` — health check endpoint
- [x] Go library (`go get github.com/AmritRai1234/spine`)

---

## v1.1 — CLI & Load Benchmark ✅ *SHIPPED*

- [x] Premium CLI subcommands (`serve`, `emit`, `schema`, `validate`, `status`)
- [x] Built-in `spine bench` high-concurrency load testing command
- [x] Colored JSON output and terminal formatting

---

## v1.2 — High-Performance Actions & Integrations ✅ *SHIPPED*

- [x] `http.post` — outbound webhook dispatch with timeout and template substitution
- [x] `log.write` — structured stdout/stderr logging
- [x] `db.delete` — condition-based row deletion
- [x] Event Chaining — route state emission automatically triggers dependent routes (with depth limit guard)
- [x] Template Variables — `$now`, `$uuid`, `$env.VAR`, `$event.name`, `$event.payload.path`
- [x] High-Throughput Batch Engine — buffered channel drain with grouped SQLite transactions (56,800+ req/sec)
- [x] Prepared Statement Caching — zero query compilation overhead on hot path
- [x] RAM State Engine — sub-microsecond `GetState()` lookups in RAM
- [x] Manifest-Driven Auto-Indexing Engine — auto-generates SQL indexes for query lookup columns

---

## v1.3 — Conditional Logic, Parallel Execution & Adaptive Engine ✅ *SHIPPED*

Make routes smart with branching, parallel dispatch, fault tolerance, and dynamic auto-tuning.

- [x] `if:` conditions on routes and individual steps
- [x] Comparison operators: `==`, `!=`, `>`, `>=`, `<`, `<=`, `contains`, `exists`
- [x] `parallel: true` — concurrent step execution via worker goroutines
- [x] `max_attempts` & `backoff_ms` — automated retry engine for step execution
- [x] Turso / libSQL pure-Go driver engine (`turso.tech/database/tursogo`)
- [x] Adaptive Self-Improving Latency Engine (`optimizer.go`)
- [x] `GET /metrics` live runtime telemetry endpoint
- [x] Scaled batch capacity up to 10,000 items with 0% data loss

---

## v1.4 — Query & Read API 📊 *(NEXT UP)*

Reading data back directly from manifest specifications.

- [ ] `GET /tables` — list all tables with row counts
- [ ] `GET /tables/:name` — query rows with filters
- [ ] `GET /events` — query the event audit log
- [ ] Auto-log every emitted event to `_spine_events` table

---

## v2.0 — Multi-Runtime & Plugins 🔌

- [ ] Plugin system via Go interfaces & `.so` shared libraries
- [ ] Queue publisher/consumer (`queue.publish` for Redis/NATS)
- [ ] PostgreSQL & MySQL drivers
- [ ] Multi-file manifest imports (`include: auth.spine`)

---

## Version Timeline

| Version | Focus | Status |
|---------|-------|--------|
| **v1.0** | Core Runtime | ✅ Shipped |
| **v1.1** | CLI & Load Benchmarking | ✅ Shipped |
| **v1.2** | Actions, Event Chaining & Performance (56K req/s) | ✅ Shipped |
| **v1.3** | Conditional Logic, Parallel Execution & Adaptive Engine | ✅ Shipped |
| **v1.4** | Query API & Event Audit Log | 🧠 Next Up |
| **v2.0** | Plugins & Multi-DB Engine | Q1 2027 |
