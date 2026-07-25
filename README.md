<p align="center">
  <strong>⚡ SPINE</strong><br>
  <em>Declarative Event-Driven Backend Engine</em>
</p>

<p align="center">
  <a href="#performance"><img src="https://img.shields.io/badge/throughput-10K%20req%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance"><img src="https://img.shields.io/badge/P99%20latency-6ms-blue?style=flat-square" alt="P99"></a>
  <a href="#performance"><img src="https://img.shields.io/badge/memory-21MB%20RSS-purple?style=flat-square" alt="Memory"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-library-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-gray?style=flat-square" alt="License"></a>
</p>

---

**Spine** replaces your API route handlers with a single declarative manifest. Define events, payload schemas, database tables, and routing logic in one `.spine` file — the runtime handles validation, persistence, dispatching, and real-time state broadcasting automatically.

```
Frontend fires event → Spine validates → Executes route steps → Persists to SQLite → Broadcasts state over WebSocket
```

## Why Spine

| Problem | Spine's Answer |
|---------|---------------|
| 50 endpoint handlers that just parse JSON, write to DB, push a notification | One `.spine` manifest governs all of it |
| Payload validation scattered across controllers | Schema-defined types enforced at the gate |
| Polling for state changes | Real-time WebSocket push, zero config |
| Separate config for DB schema, routes, permissions | Single source of truth in `app.spine` |
| Deploying Node/Python/Ruby + Postgres + Redis | Single 13MB binary + SQLite |

## Quick Start

```bash
# Install
go install github.com/AmritRai1234/spine/cmd/spine@latest

# Or build from source
git clone https://github.com/AmritRai1234/spine.git
cd spine
go build -o spine ./cmd/spine/

# Run
./spine examples/app.spine
```

```
[spine] loaded: 6 nodes, 5 routes, 7 tables

┌──────────────────────────────────────────┐
│  SPINE v1 Runtime Server (Go)            │
│  HTTP:  http://0.0.0.0:8080               │
│  WS:    ws://0.0.0.0:8080/ws              │
└──────────────────────────────────────────┘
```

## The Manifest

A `.spine` file declares your entire backend contract:

```yaml
spine_version: 1

database:
  tables:
    - users
    - landing_analytics
    - projects

nodes:
  LandingPage:
    owns_files:
      - frontend/src/pages/LandingPage.tsx
    emits:
      - event: SUBMIT_LEAD
        payload:
          email: string        # ← validated at runtime
    listens:
      - state: LEAD_STATUS     # ← pushed via WebSocket
        payload:
          status: string

routes:
  - "on": SUBMIT_LEAD
    steps:
      - action: db.insert
        table: landing_analytics
        input: "$event.payload"
    emit: LEAD_STATUS          # ← broadcasts to all WS clients
```

**Nodes** declare who can emit what events and who listens to what state.  
**Routes** declare what happens when an event fires.  
**The runtime** enforces all of it.

## API

### `POST /emit` — Fire an Event

```bash
curl -X POST http://localhost:8080/emit \
  -H "Content-Type: application/json" \
  -d '{"event": "SUBMIT_LEAD", "payload": {"email": "user@example.com"}}'
```

```json
{
  "status": "ok",
  "event": "SUBMIT_LEAD",
  "routes_matched": 1,
  "emitted_states": ["LEAD_STATUS"]
}
```

**Invalid payloads are rejected:**

```bash
curl -X POST http://localhost:8080/emit \
  -d '{"event": "SUBMIT_LEAD", "payload": {}}'
```

```json
{
  "status": "error",
  "error": "validation error: missing required field 'email' (expected type string)"
}
```

### `GET /health` — Health Check

```json
{"status": "ok", "engine": "spine-go", "version": 1}
```

### `GET /schema` — Live Manifest Introspection

Returns the full parsed manifest as JSON — nodes, routes, tables, payload schemas.

### `ws://localhost:8080/ws` — Real-Time State Push

Connect a WebSocket client and receive state broadcasts whenever a route emits:

```json
{
  "type": "state",
  "state": "LEAD_STATUS",
  "event": "SUBMIT_LEAD",
  "payload": {"email": "user@example.com"},
  "timestamp": 1784969604115
}
```

Clients can also **emit events over the WebSocket**:

```json
{"event": "SUBMIT_LEAD", "payload": {"email": "user@example.com"}}
```

## Use as a Go Library

```go
import spine "github.com/AmritRai1234/spine"

func main() {
    engine, err := spine.NewFromFile("app.spine", "data.db")
    if err != nil {
        log.Fatal(err)
    }
    defer engine.Close()

    // Or emit events programmatically:
    result, err := engine.Bus.Emit("SUBMIT_LEAD", map[string]interface{}{
        "email": "user@example.com",
    })

    // Start the server
    log.Fatal(engine.ListenAndServe(":8080"))
}
```

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│  HTTP/WS    │────▶│   Registry   │────▶│    Bus       │
│  Server     │     │  (lock-free) │     │  (dispatch)  │
└─────────────┘     └──────────────┘     └──────┬──────┘
                                                │
                    ┌──────────────┐     ┌──────▼──────┐
                    │  WebSocket   │◀────│   SQLite     │
                    │  Hub         │     │  (WAL mode)  │
                    │  (broadcast) │     └─────────────┘
                    └──────────────┘
```

| Component | File | Role |
|-----------|------|------|
| Schema | `schema.go` | Type definitions for nodes, routes, fields |
| Parser | `parser.go` | Line-by-line state machine manifest parser |
| Registry | `registry.go` | Lock-free atomic lookups + payload validation |
| Bus | `bus.go` | Event dispatch, SQLite persistence, route execution |
| Hub | `hub.go` | WebSocket client management + state broadcasting |
| Engine | `engine.go` | Top-level API, HTTP server, hot-reload watcher |

## Features

- **Manifest-Driven** — One `.spine` file defines your entire backend
- **Payload Validation** — Type-checked (`string`, `number`, `boolean`) at the gate
- **SQLite Persistence** — WAL mode, auto-schema, zero-config
- **WebSocket Broadcasting** — Real-time state push to connected clients
- **Hot Reload** — Edit `app.spine` → instant registry update, no restart
- **Lock-Free Reads** — Atomic pointer swap for zero-contention lookups
- **Schema Introspection** — `GET /schema` exposes the live manifest as JSON
- **Single Binary** — 13MB, no dependencies, no Docker required

## Performance

Benchmarked with Apache Bench on a standard laptop (AMD Ryzen, 16GB RAM):

```
POST /emit → validate payload → SQLite insert → JSON response
```

| Concurrency | Requests/sec | Mean Latency | P99 Latency | Failed |
|-------------|-------------|-------------|------------|--------|
| 10 | **10,138** | 0.99 ms | 6 ms | 0 |
| 50 | **9,904** | 5.0 ms | 22 ms | 0 |
| 100 | **9,844** | 10.2 ms | 45 ms | 0 |

**Resources:** 13MB binary, 21MB RSS post-load.

### Optimization Stack

1. **Table existence cache** — `CREATE TABLE IF NOT EXISTS` runs once, not per-request
2. **SQLite tuning** — `synchronous=NORMAL`, 64MB cache, mmap, `temp_store=MEMORY`
3. **Lock-free registry** — `atomic.Pointer` swap, zero mutex contention on reads
4. **sync.Pool** — Pooled request structs and response buffers
5. **Single writer** — `MaxOpenConns=1` eliminates SQLite lock contention

## CLI

```bash
spine [options] [manifest.spine]

Options:
  --port PORT    Listen port (default: 8080)
  --db PATH      SQLite path (default: spine.db)
  --version      Print version
  --help         Show help
```

## Roadmap

- [ ] `http.post` action — webhook dispatch from routes
- [ ] `queue.publish` action — Redis/NATS integration
- [ ] Conditional routes — `if: payload.role == "admin"`
- [ ] Event chaining — route A emits state → triggers route B
- [ ] Auth middleware — JWT validation per-route
- [ ] `spine validate` — CLI manifest linter
- [ ] Prometheus `/metrics` endpoint
- [ ] Event replay from audit log
- [ ] Multi-file manifests — `include: auth.spine`

## License

MIT
