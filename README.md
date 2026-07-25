<p align="center">
  <img src="https://raw.githubusercontent.com/AmritRai1234/spine/main/assets/logo.png?v=2" width="200" alt="Spine Logo"><br>
  <strong>SPINE</strong><br>
  <em>Declarative Event-Driven Backend & Multi-Node Orchestration Engine</em>
</p>

<p align="center">
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/throughput-56K%20req%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/latency-59%CE%BCs-blue?style=flat-square" alt="Latency"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/memory-21MB%20RSS-purple?style=flat-square" alt="Memory"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-v2.0.0-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-LGPLv3-blue?style=flat-square" alt="License"></a>
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
- [CLI Reference](#cli-reference)
- [Optimization & High-Throughput Engine](#optimization--high-throughput-engine)
- [Security & Governance](#security--governance)
- [Go SDK / Embedding Guide](#go-sdk--embedding-guide)
- [Performance & Benchmarks](#performance--benchmarks)
- [License](#license)

---

## Why Spine?

| Traditional Backends | Spine Engine |
| :--- | :--- |
| **Scattered Handlers**: Dozens of REST controllers parsing JSON & writing SQL queries | **Declarative Manifest**: Single `.spine` file governs routes, persistence & events |
| **Manual Validation**: Duplicate type checks & validation code across endpoints | **Contract Enforcement**: Enforces typed payload contracts at runtime |
| **Polling Overhead**: Clients polling endpoints for database updates | **Real-Time Push**: Built-in WebSocket broadcasting across all cluster nodes |
| **Complex Dependencies**: Requires external DBs, Redis, background worker queues | **Single Binary or Scaled Cluster**: Embedded SQLite WAL for single-node; Redis/Turso for cluster scale |
| **Brittle Webhooks**: Dropped requests on network glitches | **Durable Outbox Queue**: Outbox-backed retries survive process restarts |

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
│            pkg/middleware (Auth & X-Forwarded-For Rate Limiting)        │
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
│  (SQLite / Turso)    │ │  (http.post Webhook) │ │  (ws://.../ws)       │
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
 ┌─────────────────────────────┐ ┌───────────┐ ┌─────────────────────────────┐
 │    PubSub Backplane         │ │  Outbox   │ │   Turso / libSQL Primary    │
 │ (Redis Streams / NATS)      │ │   Queue   │ │   (Multi-Region Replicas)   │
 └─────────────────────────────┘ └───────────┘ └─────────────────────────────┘
```

When any node processes an event that emits state, it publishes the update to the PubSub backplane (`pkg/engine/pubsub.go`), instantly synchronizing WebSockets across all connected cluster instances.

---

## Project Layout

```
Spine/
├── cmd/
│   └── spine/           # Standalone CLI application executable (cmd/spine/main.go)
│
├── pkg/
│   ├── engine/          # Core runtime execution engine
│   │   ├── bus.go       # Event dispatch, batch writer, table indexing
│   │   ├── engine.go    # HTTP server mux & health/probe endpoints
│   │   ├── hub.go       # WebSocket real-time state broadcasting hub
│   │   ├── outbox.go    # DB-backed outbox retry queue
│   │   ├── pubsub.go    # Local & distributed PubSub backplane interface (Redis/NATS)
│   │   ├── optimizer.go # Self-improving adaptive latency tuner
│   │   ├── cond.go      # Dynamic condition evaluator (if: guards)
│   │   ├── vars.go      # Templated variable token resolver ($now, $uuid, $event)
│   │   ├── query.go     # Database table inspector & event audit query API
│   │   └── migrations.go# Versioned schema migration tracker
│   │
│   ├── manifest/        # Declarative .spine language parser & AST
│   │   ├── parser.go    # Multi-file manifest parser state machine
│   │   ├── registry.go  # Lock-free atomic route & node registry
│   │   └── schema.go    # AST struct definitions
│   │
│   └── middleware/      # Security & access control middleware
│       ├── auth.go      # API key & Bearer token header verification
│       └── ratelimit.go # Token-bucket IP rate limiter (with X-Forwarded-For support)
│
├── web/                 # Minimalist developer web dashboard (Vite + React + TS)
├── tests/               # End-to-end unit test suites (10 test suites)
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

# 2. Run Server
./spine serve examples/app.spine --port 8080

# 3. Open Developer Dashboard
# Navigate to http://localhost:8080 in your browser
```

---

## The `.spine` Manifest Specification

```yaml
spine_version: 1

includes:
  - auth.spine

database:
  tables:
    - landing_analytics
    - users

nodes:
  LandingPage:
    emits:
      - event: SUBMIT_LEAD
        payload:
          email: string

routes:
  - on: SUBMIT_LEAD
    parallel: true
    steps:
      - action: db.insert
        table: landing_analytics
        input: "$event.payload"
      
      - action: http.post
        url: "https://api.crm.example.com/leads"
        max_attempts: 3
        backoff_ms: 100

      - action: log.write
        message: "[LEAD] Received lead for $event.payload.email at $now"
        if: "$event.payload.email != ''"

    emit: LEAD_STATUS
```

---

## Database Schema & Migrations

Spine provides two distinct, complementary schema management strategies:

1. **Development Auto-Migration (`db.insert`)**:
   - In rapid development, `db.insert` dynamically creates missing tables and missing JSON columns on initial write without manual SQL setup.

2. **Production Versioned Migrations (`pkg/engine/migrations.go`)**:
   - For production environments, explicit schema migrations are applied in versioned sequence via `engine.Bus.ApplyMigration(v, name, sql)`.
   - Migration history is transactionally tracked in the `_spine_migrations` meta-table to prevent unauthorized schema drift across deployments.

---

## HTTP & WebSocket API Reference

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `/emit` | `POST` | Dispatch event payload to trigger manifest routes |
| `/schema` | `GET` | Return live parsed manifest AST schema |
| `/tables` | `GET` | List database tables and row counts |
| `/tables/:name` | `GET` | Fetch paginated table rows (`?limit=50&offset=0`) |
| `/events` | `GET` | Audit log history of emitted events |
| `/metrics` | `GET` | Live optimizer metrics, batch size & flush intervals |
| `/healthz` | `GET` | Kubernetes liveness probe (`{"status":"healthy"}`) |
| `/readyz` | `GET` | Kubernetes readiness probe (`{"status":"ready"}`) |
| `/ws` | `WS` | Real-time WebSocket state broadcast channel |

---

## Minimalist Developer Web Dashboard

Spine embeds a dark minimalist developer interface served at `http://localhost:8080/`.

- **Obsidian Dark Aesthetic**: Clean technical UI built with `Geist Mono` and `Inter`.
- **Code-First Event Console**: Interactively emit events with auto-filled JSON templates (`⌘+Enter` shortcut).
- **Live Event Stream**: Real-time event log feed with filter tabs (`ALL`, `EMIT`, `STATE`, `ERROR`).
- **Schema Inspector**: Visual breakdown of declared nodes, triggers, route step chains (`→`), and tables.

---

## Security & Governance

1. **API Key & Bearer Token Authentication**:
   - Gatekeep endpoints using `--api-key <KEY>` or `SPINE_API_KEY`.
   - Accepts headers: `Authorization: Bearer <KEY>` or `X-API-Key: <KEY>`.

2. **Reverse Proxy Aware Rate Limiting**:
   - Token-bucket rate limiting (`pkg/middleware/ratelimit.go`) automatically parses `X-Forwarded-For` and `X-Real-IP` headers to enforce per-client IP limits when deployed behind Load Balancers or CDNs.

3. **SQL Injection Guardrails & Outbox Persistence**:
   - Parameterized SQL (`?` bindings) and strict identifier sanitization (`sanitizeIdent`).
   - Outbound webhooks and retries are durable, backed by the `_spine_outbox` table.

---

## Performance & Benchmarks

Benchmarked using `./spine bench` on local loopback (Linux x86_64, 12-core CPU, NVMe SSD):

- **Throughput**: **50,000 – 53,000 requests/sec**
- **Min Latency**: **72 µs**
- **Average Latency**: **939 µs**
- **Packet Loss**: **0%** (100% success rate under 50 concurrent client streams)

> ⚠️ **Reproducibility Note**: Benchmark results vary depending on disk I/O speed, network topology, kernel scheduler, and client connection counts. To reproduce on your target environment, execute:
> ```bash
> ./spine bench -c 50 -d 5s --host http://localhost:8080
> ```

---

## License

Distributed under the **GNU Lesser General Public License v3.0 (LGPLv3)**. See [LICENSE](LICENSE) for full details.
