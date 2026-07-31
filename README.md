<p align="center">
  <img src="https://raw.githubusercontent.com/AmritRai1234/spine/main/assets/logo.png?v=2" width="200" alt="Spine Logo"><br>
  <strong>SPINE</strong><br>
  <em>Declarative Event-Driven Backend Engine (v5.0 Complete)</em>
</p>

<p align="center">
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/throughput-420K%20emit%2Fs-brightgreen?style=flat-square" alt="Throughput"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/latency-2.7μs-blue?style=flat-square" alt="Latency"></a>
  <a href="#performance--benchmarks"><img src="https://img.shields.io/badge/allocs-25%20per%20emit-purple?style=flat-square" alt="Allocs"></a>
  <a href="https://pkg.go.dev/github.com/AmritRai1234/spine"><img src="https://img.shields.io/badge/Go-v1.24-00ADD8?style=flat-square&logo=go" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-LGPLv3-blue?style=flat-square" alt="License"></a>
  <a href="#tests"><img src="https://img.shields.io/badge/tests-100%25%20pass-brightgreen?style=flat-square" alt="Tests"></a>
</p>

---

**Spine** is a high-performance, declarative event-driven runtime and orchestration engine written in Go. It replaces complex API controllers and scattered database handlers with a single, type-safe `.spine` manifest file.

With Spine, you declare database tables, event nodes, and multi-step action routes in code. The runtime automatically handles type contract validation, high-throughput database persistence (SQLite WAL / Turso libSQL), outbox webhook retries, full-text search (`fts.search`), multi-tenancy isolation (`tenant:`), cloud deployment generation (`spine deploy`), starter templates (`spine init --template`), and real-time WebSocket state broadcasting.

```
Client Event → Load Balancer → Spine Node (RLAC Auth & Rate Limit) → Event Bus → PubSub Backplane → Batched DB / WebSocket
```

---

## Table of Contents

- [Why Spine?](#why-spine)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [CLI Reference](#cli-reference)
- [Building a Website with Spine](#building-a-website-with-spine)
- [The `.spine` Manifest Specification](#the-spine-manifest-specification)
- [HTTP & WebSocket API Reference](#http--websocket-api-reference)
- [Go SDK / Embedding Guide](#go-sdk--embedding-guide)
- [System & Cluster Architecture](#system--cluster-architecture)
- [Project Layout](#project-layout)
- [Configuration Reference](#configuration-reference)
- [Optimization & High-Throughput Engine](#optimization--high-throughput-engine)
- [Security & Governance](#security--governance)
- [Performance & Benchmarks](#performance--benchmarks)
- [Tests](#tests)
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

## Installation

### 1. One-Line Universal Shell Installer (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/AmritRai1234/spine/main/install.sh | bash
```

### 2. Via `go install`

```bash
go install github.com/AmritRai1234/spine/cmd/spine@latest
```

### 3. Via Makefile (Source Build)

```bash
git clone https://github.com/AmritRai1234/spine.git
cd spine
make install
```

### 4. Python Client SDK (`pip`)

```bash
# Install Python Client SDK from repository
pip install sdk/python/
```

### 5. Docker

```bash
docker build -t spine .
docker run -p 8080:8080 -v $(pwd)/examples:/app spine serve /app/app.spine
```

---

## Quick Start

### 1. Create a Manifest

Create `app.spine`:

```yaml
spine_version: 1

database:
  tables:
    - users
    - leads

nodes:
  LandingPage:
    emits:
      - event: SUBMIT_LEAD
        payload:
          email: string
          name: string
    listens:
      - state: LEAD_STATUS
        payload:
          status: string

routes:
  - on: SUBMIT_LEAD
    steps:
      - action: db.insert
        table: leads
      - action: log.write
        message: "New lead from $event.payload.email"
    emit: LEAD_STATUS
```

### 2. Start the Server

```bash
# From source
./spine serve app.spine --port 8080

# Or with Docker
docker run -p 8080:8080 -v $(pwd):/app spine serve /app/app.spine
```

### 3. Emit an Event

```bash
# Via CLI
./spine emit SUBMIT_LEAD --payload '{"email":"jane@example.com","name":"Jane"}'

# Via HTTP
curl -X POST http://localhost:8080/emit \
  -H "Content-Type: application/json" \
  -d '{"event":"SUBMIT_LEAD","payload":{"email":"jane@example.com","name":"Jane"}}'
```

### 4. Query Data

```bash
# List all tables
curl http://localhost:8080/tables

# Query table rows
curl http://localhost:8080/tables/leads?limit=10

# Filter by column value
curl http://localhost:8080/tables/leads?where=status:active&limit=20

# Query event audit log
curl http://localhost:8080/events?event=SUBMIT_LEAD&limit=20
```

### 5. Real-Time WebSocket

```javascript
const ws = new WebSocket("ws://localhost:8080/ws");
ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log("State update:", data.state, data.payload);
};

// Emit events via WebSocket
ws.send(JSON.stringify({
  event: "SUBMIT_LEAD",
  payload: { email: "ws@example.com", name: "WS User" }
}));
```

---

## CLI Reference

Spine provides a rich command-line toolkit for local dev, scaffolding, codegen, debugging, and deployment.

```bash
spine serve app.spine [options]    # Start production HTTP/WS engine
spine dev app.spine [options]      # Start hot-reloading dev server with colored logging
spine init [dir] [--template X]   # Scaffold new project (templates: chat, dashboard, iot)
spine test [manifest.spine]        # Run manifest-defined assertion tests
spine deploy [fly|railway|render]  # Generate cloud deployment config (fly.toml, Dockerfile)
spine plugin add <plugin-name>     # Download & register WASM/Go action plugin modules
spine docs [--port 9090]           # Launch local doc server & visualizer
spine emit <event> --payload '{...}' # Emit event to running Spine server
spine codegen [manifest.spine]     # Generate type-safe TypeScript definitions
spine replay [manifest.spine]      # Replay historical audit log events
```

---

## Building a Website with Spine

A complete example showing how a frontend app talks to Spine. No Go code required — just a `.spine` manifest and JavaScript.

### Full Manifest Example

```yaml
spine_version: 1

database:
  tables:
    - users
    - posts

nodes:
  Frontend:
    emits:
      - event: REGISTER_USER
        payload:
          email: string
          name: string
          age: number        # → SQLite REAL (enables numeric sorting)
          active: boolean    # → SQLite INTEGER

      - event: CREATE_POST
        payload:
          title: string
          author_email: string

    listens:
      - state: USER_REGISTERED
      - state: POST_CREATED

routes:
  # Register a user — auto-generate ID and timestamp
  - on: REGISTER_USER
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.upsert
        table: users
        key: email
    emit: USER_REGISTERED

  # Create a post
  - on: CREATE_POST
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.insert
        table: posts
      - action: log.write
        message: "New post '$event.payload.title' by $event.payload.author_email"
    emit: POST_CREATED
```

### JavaScript — Emit Events

```javascript
const SPINE = 'http://localhost:8080';
const API_KEY = 'your-api-key';

// Register a user
async function registerUser(email, name, age) {
  const res = await fetch(`${SPINE}/emit`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-API-Key': API_KEY
    },
    body: JSON.stringify({
      event: 'REGISTER_USER',
      payload: { email, name, age, active: true }
    })
  });
  return res.json();
}
```

### JavaScript — Query Data

```javascript
// Get all users
const users = await fetch(`${SPINE}/tables/users`, {
  headers: { 'X-API-Key': API_KEY }
}).then(r => r.json());

// Filter: get only active users
const active = await fetch(`${SPINE}/tables/users?where=active:1&limit=50`, {
  headers: { 'X-API-Key': API_KEY }
}).then(r => r.json());

// Filter: get posts by a specific author
const posts = await fetch(`${SPINE}/tables/posts?where=author_email:jane@example.com`, {
  headers: { 'X-API-Key': API_KEY }
}).then(r => r.json());
```

### JavaScript — Real-Time Updates & Browser WebSocket Auth

Connect via `?token=` query parameter or send an in-band `{ "type": "auth", "token": "..." }` handshake message (ideal for browser clients):

```javascript
// Option A: Connection URL token
const ws = new WebSocket(`ws://localhost:8080/ws?token=${API_KEY}`);

// Option B: In-band message handshake (for browsers without custom headers)
const wsBrowser = new WebSocket('ws://localhost:8080/ws');
wsBrowser.onopen = () => {
  wsBrowser.send(JSON.stringify({ type: 'auth', token: API_KEY }));
};

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === 'auth_ack' && msg.status === 'ok') {
    console.log('WebSocket authenticated successfully!');
  }
  if (msg.type === 'state') {
    switch (msg.state) {
      case 'USER_REGISTERED':
        showNotification(`Welcome ${msg.payload.name}!`);
        break;
      case 'POST_CREATED':
        addPostToFeed(msg.payload);
        break;
    }
  }
};
```

### What Spine Handles For You

| You write | Spine does |
|---|---|
| `event: REGISTER_USER` with `email: string` | Validates payload types at runtime |
| `action: set` with `id: $uuid` | Auto-generates UUID and timestamps |
| `action: db.upsert` with `key: email` | Creates table, adds columns, upserts rows |
| `age: number` in payload | Creates `REAL` column (not TEXT), enables numeric sorting |
| `emit: USER_REGISTERED` | Broadcasts to all WebSocket clients instantly |
| `?where=active:1` | Returns filtered rows with parameterized SQL (injection-safe) |

---

## The `.spine` Manifest Specification

### Structure

```yaml
spine_version: 1          # Required. Manifest format version.

include:                   # Optional. Import other .spine files.
  - auth.spine
  - billing.spine

database:
  tables:                  # Declare tables (auto-created with schema evolution)
    - users
    - orders
  outbox:                  # Optional. Durable webhook retry worker pool tuning
    max_workers: 10        # Concurrent worker goroutines (default: 10)
    max_retries: 5         # Maximum outbox retry attempts (default: 5)
    backoff_ms: 1000       # Initial retry backoff interval in ms (default: 1000)

nodes:                     # Declare UI/service nodes and their event contracts
  NodeName:
    owns_files:            # Optional. Maps node to source files
      - src/pages/Node.tsx
    emits:                 # Events this node can emit
      - event: EVENT_NAME
        payload:
          field_name: type # Supported: string, number, integer, boolean
                           # Maps to SQLite: TEXT, REAL, INTEGER, INTEGER
    listens:               # States this node subscribes to (via WebSocket)
      - state: STATE_NAME
        payload:
          field_name: type

routes:                    # Event → action pipelines
  - on: EVENT_NAME         # Trigger event
    if: "condition"        # Optional. Route-level guard condition
    parallel: true         # Optional. Execute steps concurrently
    on_failure: FAILURE_STATE # Optional. Failure state to emit if route steps fail
    steps:
      - action: ACTION     # Step action (see Actions Reference)
        if: "condition"    # Optional. Step-level guard condition
        max_attempts: 3    # Optional. Retry count on failure
        backoff_ms: 1000   # Optional. Backoff in ms between retries
        on_failure: STEP_FAILED # Optional. Step-specific failure state emission
    emit: STATE_NAME       # Optional. Emit state after route completes
```

### Error & Failure Routes (`on_failure` / `on_error`)

When an action step fails (or exceeds its `max_attempts`), Spine automatically triggers failure route handling if `on_failure` (or `on_error`) is defined at the step or route level:

1. **Error Context**: Spine enriches the failure payload with `error`, `failed_action`, `failed_event`, and preserves an immutable `_error_context` object (`original_payload`, `step_index`, `failed_event`, `failed_action`, `timestamp`).
2. **RAM State Cache**: Updates state cache for instant `GetState("PROCESSING_FAILED")` access.
3. **WebSocket Broadcast**: Pushes state change to all connected UI / Dashboard WebSocket clients.
4. **Event Chaining**: Automatically triggers any downstream routes listening on the failure state.

```yaml
routes:
  - on: PROCESS_VIDEO
    on_failure: PROCESSING_FAILED
    steps:
      - action: pipeline.run
        timeout_sec: 30

  - on: PROCESSING_FAILED
    steps:
      - action: db.insert
        table: error_logs
      - action: log.write
        message: "[ALERT] Pipeline failed for $event.payload.project_id: $event.payload.error"
```

### Built-in Actions

| Action | Description | Parameters |
|---|---|---|
| `db.insert` | Insert a row into a table | `table` (required) |
| `db.update` | Update a row (matches on `id` or specified key) | `table` (required) |
| `db.upsert` | Insert or update on conflict | `table` (required), `key` (conflict column, default: `id`). Fails explicitly if `key` is missing from payload. |
| `db.delete` | Delete a row by `id` or `where` condition | `table`, `where` (optional) |
| `set` | Inject computed fields into the payload | Any `key: value` pairs (supports `$uuid`, `$now`, etc.) |
| `log.write` | Write a log message with template variables | `message` (required) |
| `http.post` | Send an HTTP POST webhook | `url` (required) |
| `emit` | Emit a chained event | `event`, `payload` (optional) |

### Template Variables

| Variable | Description | Example |
|---|---|---|
| `$event.name` | Current event name | `SUBMIT_LEAD` |
| `$event.payload.field` | Payload field value | `$event.payload.email` |
| `$now` | Current UTC timestamp (RFC 3339) | `2026-07-26T16:00:00Z` |
| `$uuid` | Generated UUID v4 | `a1b2c3d4-...` |
| `$env.VAR_NAME` | Environment variable | `$env.API_KEY` |

### Conditions

Route and step guards support comparison operators:

```yaml
# String comparison
if: "$event.payload.role == 'admin'"

# Numeric comparison
if: "$event.payload.amount > 100"

# Not equal
if: "$event.payload.status != 'deleted'"
```

---

## HTTP & WebSocket API Reference

### Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | No | Health check (`{"status":"ok"}`) |
| `GET` | `/healthz` | No | Kubernetes liveness probe |
| `GET` | `/readyz` | No | Kubernetes readiness probe |
| `POST` | `/emit` | Yes | Emit an event with payload |
| `GET` | `/schema` | Yes | Return parsed manifest schema as JSON |
| `GET` | `/tables` | Yes | List all database tables |
| `GET` | `/tables/{name}` | Yes | Query rows (`?limit=50&offset=0&where=col:val`) |
| `GET` | `/events` | Yes | Query event audit log (`?event=NAME&limit=50`) |
| `GET` | `/metrics` | No | Optimizer mode and batch metrics |
| `WS` | `/ws` | Yes | Real-time WebSocket (state broadcasts + emit) |

### Authentication

Protected endpoints require an API key via one of:

```bash
# Header: X-API-Key
curl -H "X-API-Key: YOUR_KEY" http://localhost:8080/emit ...

# Header: Authorization Bearer
curl -H "Authorization: Bearer YOUR_KEY" http://localhost:8080/emit ...

# WebSocket: query parameter
ws://localhost:8080/ws?token=YOUR_KEY
```

### Emit Request/Response

```bash
# Request
POST /emit
Content-Type: application/json
{
  "event": "SUBMIT_LEAD",
  "payload": {
    "email": "jane@example.com",
    "name": "Jane"
  }
}

# Response (200 OK)
{
  "status": "ok",
  "event": "SUBMIT_LEAD",
  "emitted_states": ["LEAD_STATUS"],
  "steps_executed": 2
}
```

### WebSocket Protocol

```javascript
// Client → Server: Emit event
{ "event": "EVENT_NAME", "payload": { ... } }

// Server → Client: Event acknowledgment
{ "type": "event_ack", "status": "ok", "event": "EVENT_NAME", "result": { ... } }

// Server → Client: State broadcast (push)
{ "type": "state", "state": "STATE_NAME", "event": "EVENT_NAME", "payload": { ... }, "timestamp": 1722009600000 }
```

---

## Go SDK / Embedding Guide

Spine can be embedded directly in your Go application:

### Basic Usage

```go
package main

import (
    "log"
    spine "github.com/AmritRai1234/spine"
)

func main() {
    // Create engine from manifest file
    eng, err := spine.NewFromFile("app.spine", "data.db")
    if err != nil {
        log.Fatal(err)
    }
    defer eng.Close()

    // Configure security
    eng.SetAPIKey("my-secret-key")
    eng.SetRateLimit(1000, 2000) // 1000 req/s, burst 2000

    // Start HTTP + WebSocket server
    log.Println("Spine running on :8080")
    log.Fatal(eng.ListenAndServe(":8080"))
}
```

### Programmatic Emit

```go
// Emit events directly from Go code
result, err := eng.Bus.Emit("SUBMIT_LEAD", map[string]interface{}{
    "email": "api@example.com",
    "name":  "API User",
})
if err != nil {
    log.Printf("emit error: %v", err)
}
log.Printf("emitted states: %v", result["emitted_states"])
```

### Custom HTTP Handler

```go
// Embed Spine's handler in your own mux
mux := http.NewServeMux()
mux.Handle("/api/", http.StripPrefix("/api", eng.HTTPHandler()))
mux.HandleFunc("/custom", myCustomHandler)
http.ListenAndServe(":8080", mux)
```

### Custom Action Plugins

```go
// Register custom actions callable from .spine manifests
eng.Bus.RegisterAction("notify.slack", func(step *spine.RouteStep, event string, payload map[string]interface{}) error {
    channel := step.Config["channel"]
    message := spine.ResolveVariables(step.Config["message"], event, payload)
    return sendSlackMessage(channel, message)
})
```

Then use in your manifest:

```yaml
routes:
  - on: CRITICAL_ERROR
    steps:
      - action: notify.slack
        channel: "#alerts"
        message: "Error from $event.payload.service: $event.payload.error"
```

### Reading State Cache

```go
// Read cached state (sub-microsecond, lock-free)
state, ok := eng.Bus.GetState("USER_PROFILE")
if ok {
    log.Printf("cached user: %v", state["name"])
}
```

### Filtered Queries

```go
// Query rows with column filter (SQL-injection safe)
rows, err := eng.Bus.QueryWhere("leads", "status", "active", 50, 0)
for _, row := range rows {
    log.Printf("Lead: %v", row["email"])
}
```

### Direct Database Access

```go
// Access Spine's internal DB for custom queries
// Shares the same connection pool and WAL visibility
db := eng.Bus.DB()
var count int
db.QueryRow(`SELECT COUNT(*) FROM "leads" WHERE status = ?`, "active").Scan(&count)
```

### Schema Inspection

```go
// Parse manifest without creating an engine
schema, err := spine.ParseManifest("app.spine")
if err != nil {
    log.Fatal(err)
}

for name, node := range schema.Nodes {
    log.Printf("Node %s emits %d events", name, len(node.Emits))
}
for _, route := range schema.Routes {
    log.Printf("Route on %s → %d steps", route.On, len(route.Steps))
}
```

---

## System & Cluster Architecture

Spine adapts the **Blackboard Architectural Pattern** for high-performance event dispatch—in-memory event emissions act as state updates posted to the central blackboard, while route steps function as reactive handlers.

### Single-Node Execution Pipeline

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
│  Sharded Write Queue │ │  Outbox Retry Queue  │ │  Async WS Hub Push   │
│  (8 channels→SQLite) │ │  (DB-Backed Outbox)  │ │  (ws://.../ws)       │
└──────────────────────┘ └──────────────────────┘ └──────────────────────┘
```

### Multi-Node Distributed Cluster

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

---

## Project Layout

```
spine/
├── cmd/spine/           # CLI binary (serve, emit, parse, version)
│   └── main.go
├── pkg/
│   ├── engine/          # Core runtime execution engine
│   │   ├── bus.go       # Event dispatch, sharded batch writer, SQL caching
│   │   ├── engine.go    # HTTP server mux, graceful shutdown & health probes
│   │   ├── hub.go       # Async WebSocket broadcasting hub
│   │   ├── outbox.go    # Notification-driven outbox retry queue
│   │   ├── pubsub.go    # Local & distributed PubSub backplane (Redis/NATS)
│   │   ├── optimizer.go # Self-tuning adaptive batch size & interval optimizer
│   │   ├── cond.go      # Dynamic condition evaluator (if: guards)
│   │   ├── vars.go      # Template variable resolver ($now, $uuid, $event)
│   │   ├── query.go     # Table inspector & event audit query API
│   │   └── migrations.go# Versioned schema migration tracker
│   ├── manifest/        # Declarative .spine language parser & AST
│   │   ├── parser.go    # Multi-file manifest parser with validation
│   │   ├── registry.go  # Lock-free atomic route & node registry
│   │   └── schema.go    # AST struct definitions
│   └── middleware/      # Security & access control middleware
│       ├── auth.go      # Timing-safe API key & Bearer token verification
│       └── ratelimit.go # 64-shard token-bucket rate limiter
├── examples/
│   └── app.spine        # Full example manifest
├── tests/               # 24 tests across 11 files
├── web/                 # Developer web dashboard
├── spine.go             # Public Go library API facade
├── Dockerfile           # Multi-stage Docker build
├── go.mod
└── README.md
```

---

## Configuration Reference

### CLI

```bash
spine serve <manifest.spine> [flags]
spine emit  <EVENT_NAME>     [flags]
spine parse <manifest.spine> [--json]
spine version
```

| Flag | Default | Description |
|---|---|---|
| `--port` | `8080` | HTTP server port |
| `--db` | `spine.db` | SQLite database path or Turso URL |
| `--api-key` | — | API key for authenticated endpoints |
| `--rate-limit` | — | Requests per second (enables rate limiting) |

### Environment Variables

| Variable | Description |
|---|---|
| `SPINE_PORT` | Server port (overrides `--port`) |
| `SPINE_DB` | Database path (overrides `--db`) |
| `SPINE_API_KEY` | API key (overrides `--api-key`) |

### Database Drivers

| Connection String | Driver |
|---|---|
| `spine.db` | SQLite (local, WAL mode) |
| `libsql://your-db.turso.io` | Turso / libSQL (cloud) |
| `turso://your-db.turso.io` | Turso / libSQL (cloud) |

### SQLite Performance Pragmas

Spine automatically applies these pragmas for optimal write throughput:

| Pragma | Value | Purpose |
|---|---|---|
| `journal_mode` | `WAL` | Write-ahead logging for concurrent reads |
| `synchronous` | `NORMAL` | Balanced durability vs speed |
| `cache_size` | `-64000` | 64MB page cache |
| `temp_store` | `MEMORY` | In-memory temp tables |
| `mmap_size` | `0` | Regular I/O (avoids TLB shootdown overhead) |
| `page_size` | `8192` | 8KB pages for SSD-aligned I/O |
| `wal_autocheckpoint` | `10000` | Delay WAL checkpoints for throughput |

---

## Optimization & High-Throughput Engine

### Write Pipeline

```
Emit() → Contract Validation → Route Steps → Sharded Writer → Batch Flush → SQLite WAL
                                                    │
                                            ┌───────┴───────┐
                                        Shard 0         Shard 7
                                        (FNV hash)      (FNV hash)
                                            └───────┬───────┘
                                                    │
                                          Single Batch Writer
                                          (tx + stmt cache)
```

### Key Optimizations

| Optimization | Impact | Description |
|---|---|---|
| **Sharded Write Channels** | Critical | 8 input channels distribute producer contention via FNV hash routing |
| **SQL Template Caching** | Critical | SQL strings cached per table+columns fingerprint (sync.Map) |
| **Deterministic Key Ordering** | High | Sorted payload keys enable SQL caching with consistent fingerprints |
| **Prepared Statement Cache** | High | Reuses `sqlite3_prepare_v2` within batch transactions |
| **Async WS Broadcast** | High | Buffered channel decouples Emit path from WebSocket fan-out |
| **Regular I/O (no mmap)** | High | Avoids TLB shootdowns and page fault overhead on write-heavy workloads |
| **Shared HTTP Client** | Medium | Connection pooling for webhook steps (100 idle conns) |
| **64-Shard Rate Limiter** | Medium | Eliminates global mutex contention under high concurrency |
| **Notification-Driven Outbox** | Medium | Immediate wakeup on enqueue instead of 5-second polling |
| **Lock-Free Registry** | High | `atomic.LoadPointer` for zero-contention route lookups (~70ns) |
| **Object Pooling** | Medium | `sync.Pool` for emit requests, byte buffers, and sorted key slices |
| **Adaptive Optimizer** | Medium | Self-tuning batch size and flush interval based on load |

---

## Security & Governance

1. **Timing-Safe API Key Verification**: Uses `crypto/subtle.ConstantTimeCompare` to prevent timing side-channel attacks.

2. **WebSocket Authentication**: WS connections require API key validation **before** upgrading. Supports `?token=KEY` query parameter or standard auth headers.

3. **Request Body Size Limits**: All request bodies capped at **1 MB** via `io.LimitReader` to prevent memory exhaustion.

4. **SQL Injection Protection**: All table/column identifiers sanitized via `sanitizeIdent()` (alphanumeric + underscore only). All queries use parameterized values.

5. **Trusted Proxy Rate Limiting**: Token-bucket rate limiting with 64-shard lock distribution. Parses `X-Forwarded-For` and `X-Real-IP` headers **only** from configured trusted proxy IPs. Stale entries auto-evicted every 60 seconds.

6. **Graceful Shutdown**: Handles `SIGINT` / `SIGTERM` with 10-second drain timeout. Ensures in-flight requests complete and batch writer flushes before exit.

7. **Durable Outbox Queue**: Outbound webhooks stored in `_spine_outbox` table. Pending retries survive process crashes and are automatically retried on restart.

---

## Performance & Benchmarks

Measured on AMD Ryzen 7 5825U (16 threads):

| Benchmark | ops/sec | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **EmitSingle** (full pipeline) | 420,000 | 2,765 | 1,207 | 25 |
| **EmitParallel** (16 goroutines) | 333,000 | 3,474 | 1,339 | 25 |
| **EmitWithValidation** (typed) | 370,000 | 3,069 | 1,456 | 29 |
| **RegistryLookup** (lock-free) | 17,000,000 | 70 | 7 | 1 |
| **ParseSmallManifest** (3 nodes) | 78,000 | 15,310 | 8,265 | 86 |
| **ParseLargeManifest** (20 nodes) | 20,000 | 58,609 | 44,740 | 586 |

Run benchmarks yourself:

```bash
go test ./tests/ -bench=. -benchmem -count=3
```

---

## Tests

**100% of test suites passing cleanly** across 25+ test files:

| Suite | Coverage |
|---|---|
| `bus_v12_test.go` | Core emit → insert → state → WebSocket flow |
| `access_test.go` | Multi-key Row-Level Access Control (RLAC) |
| `outbox_test.go` | Outbox retries & exponential backoff |
| `ws_reconnect_test.go` | WS reconnection protocol & state replay |
| `idempotency_test.go` | `_idempotency_key` deduplication |
| `cron_test.go` | Scheduled cron event worker |
| `overlay_test.go` | `SPINE_ENV` environment overlay layering |
| `tenant_test.go` | Multi-tenancy context isolation (`tenant:`) |
| `webhook_test.go` | Webhook ingestion (`POST /webhook/:provider`) |
| `fts_test.go` | Full-text search (`fts.search`) |
| `metrics_test.go` | Prometheus `/metrics` and `/admin/usage` |
| `roadmap_completion_test.go` | Full end-to-end 5-Year Roadmap feature parity verification |

```bash
# Run all tests
go test ./tests/ -v

# Run with race detector
go test ./tests/ -race -count=1
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
