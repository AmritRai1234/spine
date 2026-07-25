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
- `queue.publish`: Broadcasts event payload to pub/sub topic queues and WebSocket listeners.

### Advanced Orchestration Directives

- `includes:`: Imports modular sub-manifests recursively (`includes: [auth.spine, billing.spine]`).
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

### GET /tables — List Database Tables & Row Counts

```json
{
  "status": "ok",
  "tables": [
    {"name": "_spine_events", "rows": 1250},
    {"name": "landing_analytics", "rows": 480}
  ]
}
```

### GET /tables/:name — Query Table Rows

Returns paginated rows from a specific table (`/tables/landing_analytics?limit=10&offset=0`):

```json
{
  "status": "ok",
  "table": "landing_analytics",
  "count": 1,
  "rows": [
    {"id": 1, "email": "user@example.com"}
  ]
}
```

### GET /events — Event Audit Log

Returns full history of emitted events with payload and state metadata (`/events?event=SUBMIT_LEAD&limit=20`):

```json
{
  "status": "ok",
  "count": 1,
  "events": [
    {
      "id": 1,
      "event": "SUBMIT_LEAD",
      "payload": {"email": "user@example.com"},
      "emitted_states": ["LEAD_STATUS"],
      "created_at": "2026-07-25T11:02:02Z"
    }
  ]
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

## Optimization Architecture

Spine implements **Adaptive Load-Based Transaction Batching** (`optimizer.go`):

- **Dynamic Workload Sampling**: Samples throughput (RPS) every 100ms using atomic counters.
- **Adaptive Control Modes**: Automatically shifts between modes to balance latency and disk I/O:
  - `Micro-Latency` (RPS < 200): 250 item batch limit, 5ms flush window.
  - `Balanced` (RPS 200 - 2,000): 1,000 item batch limit, 2ms flush window.
  - `High-Throughput` (RPS 2,000 - 10,000): 2,500 item batch limit, 1ms flush window.
  - `Extreme-Batching` (RPS > 20,000): 10,000 item batch limit, 250µs flush window.
- **Lock-Free Registry**: Atomic pointer swaps (`atomic.Pointer`) for zero mutex contention on in-memory event dispatch.
- **Database Engine Tuning**: SQLite WAL pragmas (`synchronous=NORMAL`, `mmap_size=268435456`, `_busy_timeout=30000`).

## Security & Governance

1. **Authentication Middleware**:
   - Gatekeep endpoints using `--api-key <KEY>` or `SPINE_API_KEY` environment variable.
   - Accepts headers: `Authorization: Bearer <KEY>` or `X-API-Key: <KEY>`.

2. **SQL Parameterization**:
   - All database actions (`db.insert`, `db.update`, `db.delete`) use parameterized SQL statements (`?` bindings) and strict identifier sanitization (`sanitizeIdent`).

3. **Durable Outbox Retry Queue**:
   - Outbound actions and retries are backed by `_spine_outbox` table persistence, ensuring webhooks and retries survive process restarts.

4. **Production Health & Readiness Probes**:
   - `/healthz`: Liveness probe (`{"status":"healthy"}`).
   - `/readyz`: Readiness probe (`{"status":"ready"}`).

5. **Cluster Mode & Multi-Node PubSub**:
   - Event and WebSocket broadcasts use the `PubSub` interface, supporting local in-memory fanout or distributed backplane pub/sub across multi-instance load balancer clusters.

## Performance & Benchmark Methodology

Benchmarked using `spine bench` on local loopback:

- **Environment**: Linux x86_64, Go 1.22, 12-core CPU, NVMe SSD.
- **Test Configuration**: 50 concurrent HTTP client streams emitting standard JSON payloads over 5 seconds.
- **Results**:
  - **Throughput**: ~50,000 - 53,000 req/sec
  - **Min Latency**: 72 microseconds
  - **Average Latency**: 939 microseconds
  - **Success Rate**: 100% (0 dropped packets)

> **Note**: Benchmark results vary depending on disk I/O, network latency, and client connection counts. Run `./spine bench -c 50 -d 5s` to reproduce on your target hardware.

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

    // Register custom Go action plugin
    engine.Bus.RegisterAction("custom.payment", func(step *spine.RouteStep, eventName string, payload map[string]interface{}) error {
        log.Printf("Executing custom Go plugin action for event %s", eventName)
        return nil
    })

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
