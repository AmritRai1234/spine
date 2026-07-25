<p align="center">
  <img src="https://raw.githubusercontent.com/AmritRai1234/spine/main/assets/logo.png?v=2" width="200" alt="Spine Logo"><br>
  <strong>SPINE</strong><br>
  <em>Declarative Event-Driven Backend & Multi-Node Orchestration Engine</em>
</p>

<p align="center">
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/throughput-56K%20req%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/latency-59%CE%BCs-blue?style=flat-square" alt="Latency"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/memory-21MB%20RSS-purple?style=flat-square" alt="Memory"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-v2.1.0-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-LGPLv3-blue?style=flat-square" alt="License"></a>
  <a href="#tests"><img src="https://img.shields.io/badge/tests-24%2F24%20pass-brightgreen?style=flat-square" alt="Tests"></a>
</p>

---

**Spine** is a high-performance, declarative event-driven runtime and orchestration engine written in Go. It replaces complex API controllers and scattered database handlers with a single, type-safe `.spine` manifest file. 

With Spine, you declare database tables, event nodes, and multi-step action routes in code. The runtime automatically handles type contract validation, high-throughput database persistence (SQLite WAL / Turso libSQL), outbox webhook retries, multi-node clustering (Redis Streams / NATS), and real-time WebSocket state broadcasting.

```
Client Event ➔ Load Balancer ➔ Spine Node (Auth & Rate Limit) ➔ Event Bus ➔ PubSub Backplane ➔ Batched DB / WebSocket
```

---

## Table of Contents

- [Why Spine?](#why-spine)
- [System & Cluster Architecture](#system--cluster-architecture)
- [Project Layout](#project-layout)
- [Quick Start](#quick-start)
- [The `.spine` Manifest Specification](#the-spine-manifest-specification)
- [Database Schema & Migrations](#database-schema--migrations)
- [HTTP & WebSocket API Reference](#http--websocket-api-reference)
- [Minimalist Developer Web Dashboard](#minimalist-developer-web-dashboard)
- [Optimization & High-Throughput Engine](#optimization--high-throughput-engine)
- [Security & Governance](#security--governance)
- [Performance & Benchmarks](#performance--benchmarks)
- [Tests](#tests)
- [Go SDK / Embedding Guide](#go-sdk--embedding-guide)
- [License](#license)

---

## Why Spine?

| Traditional Backends | Spine Engine |
| :--- | :--- |
| **Scattered Handlers**: Dozens of REST controllers parsing JSON & writing SQL queries | **Declarative Manifest**: Single `.spine` file governs routes, persistence & events |
| **Manual Validation**: Duplicate type checks & validation code across endpoints | **Contract Enforcement**: Enforces typed payload contracts at runtime |
| **Polling Overhead**: Clients polling endpoints for database updates | **Real-Time Push**: Built-in WebSocket broadcasting across all cluster nodes |
| **Complex Dependencies**: Requires external DBs, Redis, background worker queues | **Single Binary or Scaled Cluster**: Embedded SQLite WAL for single-node; Redis/Turso for cluster scale |
| **Brittle Webhooks**: Dropped requests on network glitches | **Durable Outbox Queue**: DB-backed outbox table (`_spine_outbox`) survives process & node failures |

---

## System & Cluster Architecture

Spine adapts the **Blackboard Architectural Pattern** for high-performance event dispatch—in-memory event emissions act as state updates posted to the central blackboard, while route steps function as reactive handlers.

### 1. Single-Node Execution Pipeline

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              HTTP Client                                │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │ POST /emit
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│            pkg/middleware (Auth & Trusted-Proxy Rate Limiting)          │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│              pkg/manifest (Type Contracts & Route AST)                  │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │ Contract Validation
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│           pkg/engine (Event Bus & Lock-Free Dispatcher)                 │
└───┬────────────────────────┬────────────────────────┬───────────────────┘
    │                        │                        │
    ▼                        ▼                        ▼
┌──────────────────────┐ ┌──────────────────────┐ ┌──────────────────────┐
│  Batched Write Queue │ │  Outbox Retry Queue  │ │  WebSocket Hub Push  │
│  (SQLite / Turso)    │ │  (DB-Backed Outbox)  │ │  (ws://.../ws)       │
└──────────────────────┘ └──────────────────────┘ └──────────────────────┘
```

### 2. Multi-Node Distributed Cluster Topology

For production workloads exceeding single-node writer capacity, Spine instances scale horizontally behind a Load Balancer using a **PubSub Backplane (Redis Streams / NATS)** and a primary database (Turso / libSQL):

```
                        ┌──────────────────────────────┐
                        │      CDN / Load Balancer     │
                        └──────────────┬───────────────┘
                                       │
                ┌──────────────────────┴──────────────────────┐
                ▼                                             ▼
 ┌─────────────────────────────┐               ┌─────────────────────────────┐
 │       Spine Node #1         │               │       Spine Node #2         │
 │ (HTTP, WS Hub, Local Bus)   │               │ (HTTP, WS Hub, Local Bus)   │
 └──────────────┬──────────────┘               └──────────────┬──────────────┘
                │                                             │
                ├──────────────────────┬──────────────────────┤
                │                      │                      │
                ▼                      ▼                      ▼
 ┌─────────────────────────────┐ ┌───────────────────┐ ┌─────────────────────────────┐
 │    PubSub Backplane         │ │ DB Outbox Queue   │ │   Turso / libSQL Primary    │
 │ (Redis Streams / NATS)      │ │ (_spine_outbox)   │ │   (Multi-Region Replicas)   │
 └─────────────────────────────┘ └───────────────────┘ └─────────────────────────────┘
```

When any node processes an event that emits state, it publishes the update to the PubSub backplane (`pkg/engine/pubsub.go`), instantly synchronizing WebSockets across all connected cluster instances. Outbound webhooks are persisted to the shared primary DB outbox table (`_spine_outbox`), allowing pending retries to survive individual node crashes.

---

## Project Layout

```
Spine/
├── pkg/
│   ├── engine/          # Core runtime execution engine
│   │   ├── bus.go       # Event dispatch, batch writer, table indexing
│   │   ├── engine.go    # HTTP server mux, graceful shutdown & health probes
│   │   ├── hub.go       # WebSocket real-time state broadcasting hub
│   │   ├── outbox.go    # DB-backed outbox retry queue (survives process/node crashes)
│   │   ├── pubsub.go    # Local & distributed PubSub backplane interface (Redis/NATS)
│   │   ├── optimizer.go # Self-improving adaptive latency tuner
│   │   ├── cond.go      # Dynamic condition evaluator (if: guards)
│   │   ├── vars.go      # Templated variable token resolver ($now, $uuid, $event)
│   │   ├── query.go     # Database table inspector & event audit query API
│   │   └── migrations.go# Versioned schema migration tracker
│   │
│   ├── manifest/        # Declarative .spine language parser & AST
│   │   ├── parser.go    # Hardened multi-file manifest parser with validation
│   │   ├── registry.go  # Lock-free atomic route & node registry
│   │   └── schema.go    # AST struct definitions
│   │
│   └── middleware/      # Security & access control middleware
│       ├── auth.go      # Timing-safe API key & Bearer token verification
│       └── ratelimit.go # Token-bucket rate limiter with auto-eviction
│
├── web/                 # Minimalist developer web dashboard (Vite + React + TS)
├── tests/               # End-to-end test suites (24 tests across 11 files)
│   ├── parser_test.go   # 14 parser validation & edge case tests
│   ├── benchmark_test.go# Performance benchmarks (parse, emit, registry)
│   └── ...              # Bus, conditions, optimizer, plugins, queries, etc.
├── examples/            # Example .spine manifest files
├── spine.go             # Public top-level Go library API facade
└── README.md            # Master project documentation
```

---

## Quick Start

```bash
# 1. Clone & Build
git clone https://github.com/AmritRai1234/spine.git
cd spine
go build -o spine ./cmd/spine/

# 2. Validate your manifest
./spine parse examples/app.spine

# 3. Start the server
./spine serve examples/app.spine --port 8080

# 4. Emit an event (from another terminal)
./spine emit SUBMIT_LEAD --payload '{"email":"test@dev.com","name":"Jane"}'
```

### Docker

```bash
# Build
docker build -t spine .

# Run
docker run -p 8080:8080 -v $(pwd)/examples:/app spine serve /app/app.spine
```

### CLI Reference

```bash
spine serve <manifest.spine> [--port 8080] [--db spine.db] [--api-key KEY] [--rate-limit 1000]
spine emit  <EVENT_NAME>     [--payload '{}'] [--server http://localhost:8080] [--api-key KEY]
spine parse <manifest.spine> [--json]
spine version
```

Environment variables: `SPINE_PORT`, `SPINE_DB`, `SPINE_API_KEY`

---

## Security & Governance

1. **Timing-Safe API Key Verification**:
   - Gatekeep endpoints using `SetAPIKey(key)` or `SPINE_API_KEY`.
   - Accepts headers: `Authorization: Bearer <KEY>` or `X-API-Key: <KEY>`.
   - Uses `crypto/subtle.ConstantTimeCompare` to prevent timing side-channel attacks.

2. **WebSocket Authentication**:
   - WebSocket connections (`/ws`) require API key validation **before** upgrading the connection.
   - Supports authentication via `?token=<KEY>` query parameter or standard auth headers.

3. **Request Body Size Limits**:
   - All request bodies are capped at **1 MB** via `io.LimitReader` to prevent memory exhaustion attacks.

4. **SQL Injection Protection**:
   - All table/column identifiers are sanitized via `sanitizeIdent()` (alphanumeric + underscore only).
   - `db.delete` uses **parameterized queries** (`WHERE id = ?`) instead of string interpolation.

5. **Trusted Proxy Rate Limiting**:
   - Token-bucket rate limiting with automatic **stale entry eviction** (>5min idle entries cleaned every 60s).
   - Parses `X-Forwarded-For` and `X-Real-IP` headers **only** from configured trusted proxy IPs.

6. **Graceful Shutdown**:
   - Handles `SIGINT` / `SIGTERM` with a 10-second drain timeout via `http.Server.Shutdown()`.
   - Ensures in-flight requests complete and the batch writer flushes before exit.

7. **Durable Outbox Queue Resilience**:
   - Outbound webhooks and action retries are stored in the primary database (`_spine_outbox` table). Pending retries are automatically processed by a background goroutine every 5 seconds.

---

## Performance & Benchmarks

| Benchmark | Result |
|---|---|
| **EmitSingle** (full pipeline) | 7.9μs / 44 allocs / 1,587 B |
| **EmitWithValidation** (typed payload) | 9.6μs / 57 allocs / 2,223 B |
| **EmitParallel** (multi-goroutine) | 9.2μs / 57 allocs |
| **RegistryLookup** (lock-free) | 70ns / 1 alloc / 7 B |
| **ParseSmallManifest** (3 nodes) | 14μs / 86 allocs |
| **ParseLargeManifest** (20 nodes) | 52μs / 586 allocs |

### Key Optimizations
- **ensureTable fast-path**: Skips all SQL for known table+column combos (sync.Map cache)
- **Pre-sized slices**: All hot-path slices pre-allocated to payload length (zero reallocs)
- **strings.Builder SQL**: Single-allocation SQL construction instead of fmt.Sprintf + strings.Join
- **Pooled emittedStates**: sync.Pool for emitted state slices (reduced GC pressure)
- **Dynamic batch writer**: Ticker auto-resets when adaptive optimizer changes mode
- **Lock-free registry**: atomic.LoadPointer for zero-contention route lookups
- **Object pooling**: sync.Pool for emitRequest structs and bytes.Buffer on HTTP hot path
- **SQLite WAL tuning**: Aggressive pragma configuration (WAL, NORMAL sync, 256MB mmap)

---

## Tests

**24/24 tests pass** across 11 test files:

| Suite | Coverage |
|---|---|
| `bus_v12_test.go` | Core emit → insert → state → WebSocket flow |
| `parser_test.go` | 14 cases: duplicates, circular includes, tabs, validation |
| `benchmark_test.go` | Parse, emit, and registry performance benchmarks |
| `cond_test.go` / `cond_route_test.go` | Condition evaluator + route/step guards |
| `optimizer_test.go` | Adaptive optimizer mode switching |
| `parallel_retry_test.go` | Parallel step execution + retry/backoff |
| `plugin_test.go` | Multi-file imports + custom action plugins |
| `prod_scale_test.go` | API key auth, health probes, PubSub |
| `query_test.go` | GET /tables, GET /events query APIs |
| `turso_test.go` | Turso/libSQL driver detection |
| `vars_test.go` | Template variable resolution |

```bash
go test ./... -v
```

---

## Manifest Parser

The `.spine` manifest parser includes production-grade hardening:

- **Line-numbered errors**: `app.spine:17: duplicate node name 'Dashboard'`
- **Duplicate node detection**: Prevents silent collisions with line-number references
- **Circular include guard**: Detects `a.spine → b.spine → a.spine` cycles
- **Tab tolerance**: Tabs normalized to 2-space equivalents
- **Mixed whitespace detection**: Rejects ambiguous tab+space indentation
- **Post-parse semantic validation**: Catches missing `spine_version`, empty routes, unknown events
- **Unknown top-level key detection**: Catches typos like `routs:` with suggestions

---

## License

Distributed under the **GNU Lesser General Public License v3.0 (LGPLv3)**. See [LICENSE](LICENSE) for full details.
