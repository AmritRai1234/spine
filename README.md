<p align="center">
  <img src="https://raw.githubusercontent.com/AmritRai1234/spine/main/assets/logo.png?v=2" width="200" alt="Spine Logo"><br>
  <strong>SPINE</strong><br>
  <em>Declarative Event-Driven Backend Engine</em>
</p>

<p align="center">
  <a href="#performance"><img src="https://img.shields.io/badge/throughput-56K%20req%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance"><img src="https://img.shields.io/badge/latency-59%CE%BCs-blue?style=flat-square" alt="Latency"></a>
  <a href="#performance"><img src="https://img.shields.io/badge/memory-21MB%20RSS-purple?style=flat-square" alt="Memory"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-library-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-gray?style=flat-square" alt="License"></a>
</p>

---

**Spine** is a high-performance event-driven runtime that replaces traditional API endpoint handlers with a single declarative manifest. Define events, payload schemas, database tables, and routing logic in one `.spine` file. The runtime handles validation, persistence (SQLite & Turso/libSQL), action execution, event chaining, and real-time state broadcasting automatically.

```
Frontend fires event -> Spine validates -> Executes route steps -> Persists to SQLite -> Broadcasts state over WebSocket
```

## Why Spine

| Problem | Spine Answer |
|---------|--------------|
| Scattered endpoint handlers parsing JSON & writing DB queries | One `.spine` manifest governs all routes and database persistence |
| Payload validation split across controllers | Schema-defined type contracts enforced at the gate |
| Polling databases for state changes | Real-time WebSocket push with zero configuration |
| Separate configs for DB schema, routes, permissions | Single source of truth in `app.spine` |
| Deploying multiple services, Redis, and external DBs | Single 13MB executable with embedded batched SQLite WAL engine |

## Quick Start

```bash
# Clone and build
git clone https://github.com/AmritRai1234/spine.git
cd spine
go build -o spine ./cmd/spine/

# Run the server
./spine serve examples/app.spine
```

```
[spine] loaded: 6 nodes, 6 routes, 7 tables
Server listening at http://0.0.0.0:8080
WebSocket server listening at ws://0.0.0.0:8080/ws
```

## The Manifest

A `.spine` file declares your entire backend contract:

```yaml
spine_version: 1

database:
  tables:
    - users
    - landing_analytics
    - audit_logs

nodes:
  LandingPage:
    owns_files:
      - frontend/src/pages/LandingPage.tsx
    emits:
      - event: SUBMIT_LEAD
        payload:
          email: string
    listens:
      - state: LEAD_STATUS
        payload:
          status: string

routes:
  - "on": SUBMIT_LEAD
    steps:
      - action: db.insert
        table: landing_analytics
        input: "$event.payload"
      - action: log.write
        message: "[LEAD] Received lead for $event.payload.email at $now"
    emit: LEAD_STATUS

  - "on": LEAD_STATUS
    steps:
      - action: log.write
        message: "[AUDIT] Lead status processed successfully"
```

## Route Actions & Capabilities

Spine supports multi-step execution pipelines inside routes:

- `db.insert`: Inserts event payload into database table with auto-schema creation.
- `db.update`: Updates table record by payload ID or matching key.
- `db.delete`: Deletes records by ID or custom SQL expression.
- `http.post`: Outbound webhook dispatch with retries (`max_attempts`) and payload substitution.
- `log.write`: Formatted stdout/stderr logging.

### Advanced Orchestration Directives

- `parallel: true`: Runs route steps concurrently using worker goroutines.
- `if: "..."`: Conditional execution guards (`==`, `!=`, `>`, `<`, `contains`, `exists`).
- `max_attempts: 3` & `backoff_ms: 100`: Automatic retry loop for transient failures.

### Template Variable Resolution

Route steps support dynamic template variables:
- `$now`: Current UTC ISO-8601 timestamp (`2026-07-25T04:12:00Z`).
- `$uuid`: Randomly generated v4 UUID.
- `$env.VAR`: Host environment variable lookup.
- `$event.name`: Triggering event name.
- `$event.payload.path.to.field`: Nested JSON payload extraction.

## API Specification

### POST /emit — Fire an Event

```bash
curl -X POST http://localhost:8080/emit \
  -H "Content-Type: application/json" \
  -d '{"event": "SUBMIT_LEAD", "payload": {"email": "user@example.com"}}'
```

Response:
```json
{
  "status": "ok",
  "event": "SUBMIT_LEAD",
  "routes_matched": 1,
  "emitted_states": ["LEAD_STATUS"]
}
```

### GET /metrics — Self-Improving Latency Engine Metrics

Returns real-time optimizer mode, batch size targets, and flush windows:

```json
{
  "status": "ok",
  "optimizer_mode": "Extreme-Batching",
  "target_batch_size": 10000,
  "flush_interval": "250µs"
}
```

### GET /health — Engine Status

```json
{"status": "ok", "engine": "spine-go", "version": 1}
```

### GET /schema — Live Manifest Introspection

Returns full parsed manifest as JSON — nodes, routes, tables, and payload schemas.

### ws://localhost:8080/ws — Real-Time State Push

WebSocket connection streams state updates automatically when routes emit states:

```json
{
  "type": "state",
  "state": "LEAD_STATUS",
  "event": "SUBMIT_LEAD",
  "payload": {"email": "user@example.com"},
  "timestamp": 1784969604115
}
```

## CLI Usage

```bash
spine <command> [options]

Commands:
  serve        Start the Spine runtime server
  emit         Fire an event to a running server
  bench        Run high-concurrency load benchmark
  schema       Display parsed manifest
  validate     Lint manifest for errors or missing references
  status       Check server health
  version      Print runtime version
  help         Show CLI help menu
```

### Built-in Load Benchmark

```bash
./spine bench -c 50 -d 5s --host http://localhost:8080
```

## Performance

Benchmarked using `spine bench` on 50 concurrent client connections:

| Metric | Measurement |
|--------|-------------|
| Throughput | **56,800 req/sec** |
| Minimum Latency | **59 microseconds** |
| Average Latency | **797 microseconds** |
| Success Rate | **100% (0 errors)** |
| Memory Footprint | **21MB RSS** |

### Optimization Stack

1. **Channel-Based Transaction Batcher**: Groups writes into single `BEGIN IMMEDIATE ... COMMIT` SQLite transactions.
2. **In-Memory RAM State Cache**: Sub-microsecond `GetState()` lookups without disk overhead.
3. **Manifest-Driven Auto-Indexing Engine**: Self-tuning database indexes on query lookup columns.
4. **Lock-Free Registry**: Atomic pointer swaps (`atomic.Pointer`) for zero mutex contention on event dispatch.
5. **SQLite WAL Pragmas**: `synchronous=NORMAL`, 64MB cache, mmap, `temp_store=MEMORY`.

## Go Library Integration

```go
package main

import (
    "log"
    spine "github.com/AmritRai1234/spine"
)

func main() {
    engine, err := spine.NewFromFile("app.spine", "data.db")
    if err != nil {
        log.Fatal(err)
    }
    defer engine.Close()

    // Programmatic event emission
    result, err := engine.Bus.Emit("SUBMIT_LEAD", map[string]interface{}{
        "email": "user@example.com",
    })

    // Start server
    log.Fatal(engine.ListenAndServe(":8080"))
}
```

## License

MIT
