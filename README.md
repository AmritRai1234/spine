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
├── cmd/
│   └── spine/           # Standalone CLI application executable (cmd/spine/main.go)
│
├── pkg/
│   ├── engine/          # Core runtime execution engine
│   │   ├── bus.go       # Event dispatch, batch writer, table indexing
│   │   ├── engine.go    # HTTP server mux & health/probe endpoints
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
│   │   ├── parser.go    # Multi-file manifest parser state machine
│   │   ├── registry.go  # Lock-free atomic route & node registry
│   │   └── schema.go    # AST struct definitions
│   │
│   └── middleware/      # Security & access control middleware
│       ├── auth.go      # API key & Bearer token header verification
│       └── ratelimit.go # Token-bucket rate limiter (with Trusted Proxy IP extraction)
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

## Security & Governance

1. **API Key & Bearer Token Authentication**:
   - Gatekeep endpoints using `--api-key <KEY>` or `SPINE_API_KEY`.
   - Accepts headers: `Authorization: Bearer <KEY>` or `X-API-Key: <KEY>`.

2. **Trusted Proxy Rate Limiting**:
   - Token-bucket rate limiting (`pkg/middleware/ratelimit.go`) extracts client IPs securely.
   - Parses `X-Forwarded-For` and `X-Real-IP` headers **only** when requests originate from configured trusted proxy IPs (`SetTrustedProxies([]string{"127.0.0.1", "10.0.0.1"})`), preventing malicious IP spoofing evasions.

3. **Durable Outbox Queue Resilience**:
   - Outbound webhooks and action retries are stored in the primary database (`_spine_outbox` table). If a node dies mid-execution, pending retries are resumed automatically by surviving cluster nodes.

---

## License

Distributed under the **GNU Lesser General Public License v3.0 (LGPLv3)**. See [LICENSE](LICENSE) for full details.
