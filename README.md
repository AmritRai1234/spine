<p align="center">
  <img src="https://raw.githubusercontent.com/AmritRai1234/spine/main/assets/logo.png?v=2" width="200" alt="Spine Logo"><br>
  <strong>SPINE</strong><br>
  <em>Declarative Event-Driven Backend & Orchestration Engine</em>
</p>

<p align="center">
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/throughput-56K%20req%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/latency-59%CE%BCs-blue?style=flat-square" alt="Latency"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/memory-21MB%20RSS-purple?style=flat-square" alt="Memory"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-v2.0.0-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-LGPLv3-blue?style=flat-square" alt="License"></a>
</p>

---

**Spine** is an ultra-fast, declarative event-driven runtime and orchestration engine written in Go. It replaces complex API controllers and scattered database handlers with a single, type-safe `.spine` manifest file. 

With Spine, you define database tables, event nodes, and multi-step action routes in code. The runtime automatically handles type validation, high-throughput database persistence (SQLite WAL / Turso libSQL), webhook retries, outbox queues, and real-time WebSocket state broadcasting.

```
Client Event (HTTP / CLI) ➔ Auth & Rate Limit ➔ Schema Gate ➔ Event Bus ➔ Batched SQLite / Turso ➔ WebSocket Broadcast
```

---

## Table of Contents

- [Why Spine?](#why-spine)
- [System Architecture](#system-architecture)
- [Project Layout](#project-layout)
- [Quick Start](#quick-start)
- [The `.spine` Manifest Specification](#the-spine-manifest-specification)
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
| **Polling Overhead**: Clients polling endpoints for database updates | **Real-Time Push**: Built-in WebSocket broadcasting with zero setup |
| **Complex Dependencies**: Requires external DBs, Redis, background worker queues | **Single Binary**: 13MB executable with embedded SQLite WAL batch engine |
| **Brittle Webhooks**: Dropped requests on network glitches | **Durable Outbox Queue**: Outbox-backed retries survive application restarts |

---

## System Architecture

Spine implements a modernized **Blackboard Architectural Pattern**. In-memory events act as state updates posted to the blackboard, while registered route steps act as knowledge sources reacting to state mutations.

```
                    ┌─────────────────────────────────────────────────────────┐
                    │                      HTTP Client                        │
                    └────────────────────────────┬────────────────────────────┘
                                                 │ POST /emit
                                                 ▼
                    ┌─────────────────────────────────────────────────────────┐
                    │            pkg/middleware (Auth & Rate Limit)           │
                    └────────────────────────────┬────────────────────────────┘
                                                 │
                                                 ▼
                    ┌─────────────────────────────────────────────────────────┐
                    │              pkg/manifest (Schema Registry)             │
                    └────────────────────────────┬────────────────────────────┘
                                                 │ Contract Validation
                                                 ▼
                    ┌─────────────────────────────────────────────────────────┐
                    │           pkg/engine (Event Bus & Dispatcher)           │
                    └───┬────────────────────────┬────────────────────────┬───┘
                        │                        │                        │
                        ▼                        ▼                        ▼
        ┌──────────────────────┐   ┌──────────────────────┐   ┌──────────────────────┐
        │  Batched Write Queue │   │  Outbox Retry Queue  │   │  WebSocket Hub Push  │
        │  (SQLite / Turso)    │   │  (http.post / Webhooks) │   │  (ws://.../ws)       │
        └──────────────────────┘   └──────────────────────┘   └──────────────────────┘
```

---

## Project Layout

The codebase follows an idiomatic Go layout:

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
│   │   ├── pubsub.go    # Local & distributed PubSub backplane interface
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
│       └── ratelimit.go # Token-bucket IP rate limiter
│
├── web/                 # Minimalist developer web dashboard (Vite + React + TS)
│   ├── src/             # Dashboard source code
│   └── dist/            # Compiled static web bundle (served at /)
│
├── tests/               # End-to-end unit test suites (10 total test suites)
├── spine.go             # Public top-level Go library API facade
└── README.md            # Project master documentation
```

---

## Quick Start

### 1. Installation & Building

```bash
# Clone the repository
git clone https://github.com/AmritRai1234/spine.git
cd spine

# Build the executable
go build -o spine ./cmd/spine/
```

### 2. Run the Engine

```bash
# Start Spine server with example manifest
./spine serve examples/app.spine --port 8080
```

Console Output:
```
   ███████╗██████╗ ██╗███╗   ██╗███████╗
   ██╔════╝██╔══██╗██║████╗  ██║██╔════╝
   ███████╗██████╔╝██║██╔██╗ ██║█████╗  
   ╚════██║██╔═══╝ ██║██║╚██╗██║██╔══╝  
   ███████║██║     ██║██║ ╚████║███████╗
   ╚══════╝╚═╝     ╚═╝╚═╝  ╚═══╝╚══════╝

   Event-Driven Backend Engine  v2.0.0

  ⏳ Loading manifest examples/app.spine
  ✓ Parsed successfully

  📋 Manifest
  ──────────────────────────────────────────────────
  Version:           1
  Nodes:             3
  Routes:            4
  Tables:            8
  Database:          spine.db

  🚀 Server
  ──────────────────────────────────────────────────
  HTTP:              http://0.0.0.0:8080
  WebSocket:         ws://0.0.0.0:8080/ws
  Dashboard:         http://0.0.0.0:8080/
```

### 3. Open the Developer Dashboard

Navigate to **`http://localhost:8080`** in your browser to view the minimalist live event dashboard.

---

## The `.spine` Manifest Specification

A `.spine` file declares your backend nodes, database schema, event triggers, and routing steps:

```yaml
spine_version: 1

includes:
  - auth.spine

database:
  tables:
    - landing_analytics
    - users
    - audit_logs

nodes:
  LandingPage:
    owns_files:
      - src/pages/LandingPage.tsx
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

  - on: LEAD_STATUS
    steps:
      - action: log.write
        message: "[AUDIT] Lead status broadcasted successfully"
```

### Supported Route Actions

- `db.insert`: Inserts event payload into specified table (auto-migrates schema).
- `db.update`: Updates table record matching payload primary key or filters.
- `db.delete`: Deletes records matching `where` condition or payload ID.
- `http.post`: Dispatches outbound webhook with retry policy (`max_attempts`, `backoff_ms`).
- `log.write`: Writes formatted log messages to standard stdout.
- `queue.publish`: Broadcasts event payload to pub/sub channels & connected WebSocket listeners.
- **Custom Go Plugins**: Register custom Go action handlers via `engine.Bus.RegisterAction("action.name", handler)`.

### Templated Variable Substitution Tokens

Route step parameters support dynamic string token expansion:
- `$now`: Current UTC ISO-8601 timestamp (`2026-07-25T11:45:00Z`).
- `$uuid`: Generated v4 UUID string.
- `$env.VARIABLE`: Host OS environment variable lookup.
- `$event.name`: Triggering event name.
- `$event.payload.field`: Extracted value from nested event payload JSON.

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
| `/ws` | `WS` | WebSocket state broadcast channel |

---

## Minimalist Developer Web Dashboard

Spine includes a dark minimalist web dashboard embedded directly in the binary and served at `http://localhost:8080/`.

- **Obsidian Dark Aesthetic**: Built using `Geist Mono` and `Inter` with zero bloated dependencies.
- **Code-First Event Console**: Interactively emit events with auto-filled JSON templates (`⌘+Enter` shortcut).
- **Live Event Stream**: Real-time event log feed with filter tabs (`ALL`, `EMIT`, `STATE`, `ERROR`).
- **Schema Inspector**: Visual breakdown of declared nodes, triggers, route step chains (`→`), and tables.

---

## CLI Reference

```bash
spine <command> [flags]
```

- `serve [manifest]`: Start the Spine runtime engine (default: `examples/app.spine`).
- `emit <event> [json]`: Dispatch an event directly from CLI.
- `bench`: Run concurrent benchmark test against server (`./spine bench -c 50 -d 5s`).
- `validate [manifest]`: Lint manifest for syntax or undefined route references.
- `schema [manifest]`: Output parsed JSON representation of manifest.
- `version`: Print current Spine release version.

---

## Optimization & High-Throughput Engine

Spine achieves **>50,000 requests/second** on a single node through its **Adaptive Load-Based Transaction Optimizer** (`pkg/engine/optimizer.go`):

1. **Adaptive Workload Modes**:
   - `Micro-Latency` (RPS < 200): 250 item batch limit, 5ms flush window.
   - `Balanced` (RPS 200–2,000): 1,000 item batch limit, 2ms flush window.
   - `High-Throughput` (RPS 2,000–10,000): 2,500 item batch limit, 1ms flush window.
   - `Extreme-Batching` (RPS > 20,000): 10,000 item batch limit, 250µs flush window.
2. **Lock-Free Registry**: In-memory atomic pointer swaps (`atomic.Pointer`) for zero mutex contention during hot-reloading.
3. **Optimized DB Pragmas**: Pre-tuned SQLite WAL mode (`synchronous=NORMAL`, `mmap_size=268435456`, `busy_timeout=30000`).

---

## Security & Governance

1. **API Key & Bearer Token Authentication**:
   - Secure server endpoints with `--api-key <KEY>` or `SPINE_API_KEY`.
   - Supports `Authorization: Bearer <KEY>` or `X-API-Key: <KEY>` headers.
2. **Token Bucket Rate Limiting**:
   - Prevents denial-of-service via IP token-bucket rate limiting (`engine.SetRateLimit(rps, burst)`).
3. **SQL Injection Guardrails**:
   - All DB operations utilize parameterized SQL queries (`?` bindings) and strict identifier sanitization (`sanitizeIdent`).

---

## Go SDK / Embedding Guide

Embed Spine directly inside your custom Go services:

```go
package main

import (
	"log"
	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func main() {
	// Initialize Spine engine from manifest
	engine, err := spine.NewFromFile("app.spine", "spine.db")
	if err != nil {
		log.Fatalf("Failed to start Spine: %v", err)
	}
	defer engine.Close()

	// Register custom Go plugin action
	engine.Bus.RegisterAction("custom.payment", func(step *manifest.RouteStep, event string, payload map[string]interface{}) error {
		log.Printf("[PLUGIN] Processing payment for event: %s", event)
		return nil
	})

	// Programmatically emit an event
	res, err := engine.Bus.Emit("SUBMIT_LEAD", map[string]interface{}{
		"email": "developer@spine.dev",
	})
	if err != nil {
		log.Printf("Emit error: %v", err)
	} else {
		log.Printf("Emit result: %v", res)
	}

	// Start HTTP & WebSocket server
	log.Fatal(engine.ListenAndServe(":8080"))
}
```

---

## Performance & Benchmarks

Benchmarked using `./spine bench` on local loopback (Linux x86_64, 12-core CPU, NVMe SSD):

- **Throughput**: **50,000 – 53,000 requests/sec**
- **Min Latency**: **72 µs**
- **Average Latency**: **939 µs**
- **Packet Loss**: **0%** (100% success rate under 50 concurrent client streams)

---

## License

Distributed under the **GNU Lesser General Public License v3.0 (LGPLv3)**. See [LICENSE](LICENSE) for full details.
