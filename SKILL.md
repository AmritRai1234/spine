# Spine Framework — AI Skill Reference

> **For AI/LLM agents**: This document teaches you how to correctly build applications with Spine, a declarative event-driven backend engine written in Go. Read this before generating any Spine code.

---

## What Is Spine?

Spine is a **single-binary backend engine** that replaces traditional REST controllers, database handlers, and WebSocket servers with a **declarative `.spine` manifest file**. You declare tables, events, and routes — Spine handles validation, persistence (SQLite/Turso), real-time WebSocket broadcasting, webhook retries, and more.

**Key facts:**
- Language: Go (v1.24+)
- Database: Embedded SQLite (WAL mode) or Turso/libSQL (cloud)
- Binary size: ~27-33 MB
- Throughput: ~277K events/sec, ~3.6μs latency per emit
- Module path: `github.com/AmritRai1234/spine`

---

## The `.spine` Manifest Format

Every Spine application is defined by a `.spine` file. This is **not YAML** — it is a custom indentation-based format that looks like YAML but is parsed by Spine's own parser.

### Complete Manifest Structure

```yaml
spine_version: 1          # REQUIRED. Always 1.

includes:                  # Optional. Import other .spine files (relative paths)
  - auth.spine
  - billing.spine

database:
  tables:                  # Declare tables. Spine auto-creates them with schema evolution.
    - users
    - orders
    - posts
  outbox:                  # Optional. Tune durable webhook retry queue.
    max_workers: 10        # Concurrent retry goroutines (default: 10)
    max_retries: 5         # Max retry attempts per webhook (default: 5)
    backoff_ms: 1000       # Initial retry backoff in ms (default: 1000)

access:                    # Optional. Role-based access control (RLAC)
  - role: admin
    key: $env.ADMIN_KEY    # API key from environment variable
    events:                # Whitelist of emittable events (nil = all)
      - DELETE_USER
      - ADMIN_ACTION
  - role: viewer
    key: $env.VIEWER_KEY
    read_only: true        # Can only query, cannot emit events
    filter: "tenant_id = 'acme'"  # Row-level filter injected into queries

nodes:                     # Declare UI pages or services and their event contracts
  Dashboard:
    owns_files:            # Optional. Maps node to source files for documentation
      - src/pages/Dashboard.tsx
    emits:
      - event: CREATE_ORDER
        payload:
          item: string         # → SQLite TEXT
          quantity: number     # → SQLite REAL (enables numeric sorting/comparison)
          priority: integer    # → SQLite INTEGER
          active: boolean      # → SQLite INTEGER (0/1)
    listens:
      - state: ORDER_CREATED
        payload:
          order_id: string
          status: string

  AdminPanel:
    emits:
      - event: DELETE_USER
        payload:
          user_id: string

routes:
  # Simple route: event → steps → state emission
  - on: CREATE_ORDER
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.insert
        table: orders
      - action: log.write
        message: "Order created: $event.payload.item"
    emit: ORDER_CREATED

  # Conditional route with guard
  - on: DELETE_USER
    if: "$event.payload.user_id != ''"
    steps:
      - action: db.delete
        table: users
        where: "id = '$event.payload.user_id'"

  # Parallel execution with failure handling
  - on: PROCESS_BATCH
    parallel: true
    on_failure: BATCH_FAILED
    steps:
      - action: db.insert
        table: batch_log
      - action: http.post
        url: https://hooks.example.com/batch
      - action: log.write
        message: "Batch processed"

  # Route with per-step retry and failure handling
  - on: SEND_NOTIFICATION
    steps:
      - action: http.post
        url: https://api.email.com/send
        max_attempts: 3
        backoff_ms: 2000
        on_failure: NOTIFICATION_FAILED

  # Failure handler route (triggered by on_failure)
  - on: NOTIFICATION_FAILED
    steps:
      - action: db.insert
        table: error_logs
      - action: log.write
        message: "Failed: $event.payload.error"

  # Scheduled cron route
  - on: CLEANUP_EVENT
    cron: "interval_sec: 3600"
    steps:
      - action: log.write
        message: "Hourly cleanup triggered"
```

---

## Built-in Actions Reference

### `db.insert` — Insert a row
```yaml
- action: db.insert
  table: users          # REQUIRED. Table name (auto-created if not exists)
  input: "$event.payload"  # Optional. Defaults to full payload.
```
All payload fields are inserted as columns. Spine auto-creates columns on first insert. Typed fields (`number`, `integer`, `boolean`) get proper SQLite types.

### `db.update` — Update a row
```yaml
- action: db.update
  table: users
  where: "email = '$event.payload.email'"  # Optional. Defaults to matching on the payload's `id` field.
```
With `where`, the row(s) matching the condition are updated with all payload fields. The condition is parsed as `column op value` and **parameterized** — template-interpolated values are passed as bound parameters, not string-interpolated into SQL. Without `where`, the row matching the payload's `id` field is updated (all fields except `id` are SET).

### `db.upsert` — Insert or update on conflict
```yaml
- action: db.upsert
  table: users
  key: email            # REQUIRED. The conflict column. Payload MUST include this field.
```
If a row with the same `key` value exists, it updates. Otherwise, it inserts.

### `db.delete` — Delete rows
```yaml
- action: db.delete
  table: users
  where: "id = '$event.payload.user_id'"  # Optional WHERE clause (parameterized, same format as db.update).
```

### `db.sum` — Sum a numeric column
```yaml
- action: db.sum
  table: expenses               # REQUIRED. Table name
  column: amount_cad            # REQUIRED. Column to aggregate
  where: "category = 'office'"  # Optional. Parameterized filter (column op value)
  as: total_expenses            # Optional. Payload field for the result (default: sum_result)
```
Computes `SUM(column)` and injects the result into the payload under `as`, so later steps (and the emitted state) can use `$event.payload.total_expenses`. Empty tables yield `0`, never NULL. Note: reads see committed rows — because writes are batched asynchronously, a `db.sum` in the same route as a `db.insert` may not see that insert yet. Aggregate in a separate route/event (e.g. a `CALC_TOTAL` event) instead.

### `set` — Inject computed fields into payload
```yaml
- action: set
  id: $uuid              # Any key: value pairs
  created_at: $now
  status: active
  source: $event.name
```
Modifies the in-flight payload. Subsequent steps see the injected fields.

### `log.write` — Log a message
```yaml
- action: log.write
  message: "User $event.payload.email registered at $now"
```

### `http.post` — Send webhook
```yaml
- action: http.post
  url: https://hooks.slack.com/webhook  # REQUIRED.
```
Sends the full payload as JSON POST body. On failure, the step can retry (`max_attempts`) or fall back to the outbox for durable delivery.

### `emit` — Chain another event
```yaml
- action: emit
  event: DOWNSTREAM_EVENT
```
Triggers another route within the same engine.

### `fts.search` — Full-text search
```yaml
- action: fts.search
  table: articles
  query: "$event.payload.search_term"
```

### Custom actions (Go plugins)
Any `action: my_namespace.my_action` that isn't built-in is resolved via `RegisterAction()` in Go.

---

## Template Variables

Use these anywhere in step parameters:

| Variable | Resolves To | Example Output |
|---|---|---|
| `$event.name` | Current event name | `CREATE_ORDER` |
| `$event.payload` | Full payload object | (used with `input:`) |
| `$event.payload.field` | Specific payload field | `jane@example.com` |
| `$now` | Current UTC timestamp (RFC 3339) | `2026-07-31T21:00:00Z` |
| `$uuid` | Generated UUID v4 | `a1b2c3d4-e5f6-...` |
| `$env.VAR_NAME` | Environment variable value | (value of `$VAR_NAME`) |

---

## Route Conditions (`if:` guards)

Applied at route level or step level. Supported operators: `==`, `!=`, `>`, `<`, `>=`, `<=`

```yaml
# String comparison
if: "$event.payload.role == 'admin'"

# Numeric comparison
if: "$event.payload.amount > 100"

# Not equal
if: "$event.payload.status != 'deleted'"
```

---

## Payload Type System

Declare field types in node `emits` or `listens`:

| Spine Type | SQLite Column Type | Go Type |
|---|---|---|
| `string` | `TEXT` | `string` |
| `number` | `REAL` | `float64` |
| `integer` | `INTEGER` | `int64` |
| `boolean` | `INTEGER` (0/1) | `bool` |

**If no type is declared**, the field defaults to `TEXT`.

Spine enforces these types at emit time — if a payload field doesn't match its declared type, the emit fails with a validation error.

---

## HTTP API Endpoints

| Method | Path | Auth? | Description |
|---|---|---|---|
| `POST` | `/emit` | Yes | Emit an event: `{"event":"NAME","payload":{...}}` |
| `GET` | `/tables` | Yes | List all tables |
| `GET` | `/tables/{name}` | Yes | Query rows: `?limit=50&offset=0&where=col:val` |
| `GET` | `/events` | Yes | Audit log: `?event=NAME&limit=50` |
| `GET` | `/schema` | Yes | Return parsed manifest as JSON |
| `GET` | `/health` | No | `{"status":"ok"}` |
| `GET` | `/healthz` | No | Kubernetes liveness probe |
| `GET` | `/readyz` | No | Kubernetes readiness probe |
| `GET` | `/metrics` | No | Optimizer metrics |
| `WS` | `/ws` | Yes | WebSocket (real-time state + emit) |

### Authentication methods:
```
Header:  X-API-Key: YOUR_KEY
Header:  Authorization: Bearer YOUR_KEY
WS URL:  ws://host/ws?token=YOUR_KEY
WS msg:  {"type":"auth","token":"YOUR_KEY"}
```

### Emit request/response:
```json
// POST /emit
{"event":"SUBMIT_LEAD","payload":{"email":"jane@example.com","name":"Jane"}}

// Response 200
{"status":"ok","event":"SUBMIT_LEAD","emitted_states":["LEAD_STATUS"],"steps_executed":2}
```

### WebSocket protocol:
```json
// Client → Server: emit event
{"event":"EVENT_NAME","payload":{...}}

// Server → Client: acknowledgment
{"type":"event_ack","status":"ok","event":"EVENT_NAME","result":{...}}

// Server → Client: state broadcast (pushed automatically)
{"type":"state","state":"STATE_NAME","event":"EVENT_NAME","payload":{...},"timestamp":1722009600000}
```

---

## Go SDK — Embedding Spine

### Create engine from manifest file
```go
import spine "github.com/AmritRai1234/spine"

eng, err := spine.NewFromFile("app.spine", "data.db")
if err != nil {
    log.Fatal(err)
}
defer eng.Close()

eng.SetAPIKey("my-secret-key")
eng.SetRateLimit(1000, 2000) // 1000 req/s, burst 2000
log.Fatal(eng.ListenAndServe(":8080"))
```

### Programmatic emit
```go
result, err := eng.Bus.Emit("SUBMIT_LEAD", map[string]interface{}{
    "email": "api@example.com",
    "name":  "API User",
})
```

### Read cached state (lock-free, sub-microsecond)
```go
state, ok := eng.Bus.GetState("USER_PROFILE")
```

### Register custom action plugin
```go
eng.Bus.RegisterAction("notify.slack", func(step *spine.RouteStep, event string, payload map[string]interface{}) error {
    channel := step.Config["channel"]
    message := spine.ResolveVariables(step.Config["message"], event, payload)
    return sendSlackMessage(channel, message)
})
```

### Embed in existing HTTP server
```go
mux := http.NewServeMux()
mux.Handle("/api/", http.StripPrefix("/api", eng.HTTPHandler()))
mux.HandleFunc("/custom", myHandler)
http.ListenAndServe(":8080", mux)
```

### Direct database access
```go
db := eng.Bus.DB()
var count int
db.QueryRow(`SELECT COUNT(*) FROM "leads" WHERE status = ?`, "active").Scan(&count)
```

### Query with filters
```go
rows, err := eng.Bus.QueryWhere("leads", "status", "active", 50, 0)
```

---

## Special Payload Fields

| Field | Purpose |
|---|---|
| `_idempotency_key` | Deduplication key. If the same key is re-emitted within 5 minutes, Spine returns the cached result instead of re-executing. |
| `_tenant` | Multi-tenancy isolation. Routes and queries are scoped to this tenant. |

---

## Common Patterns

### 1. CRUD Application
```yaml
spine_version: 1
database:
  tables:
    - todos

nodes:
  App:
    emits:
      - event: CREATE_TODO
        payload:
          title: string
          done: boolean
      - event: UPDATE_TODO
        payload:
          id: string
          done: boolean
      - event: DELETE_TODO
        payload:
          id: string

routes:
  - on: CREATE_TODO
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.insert
        table: todos
    emit: TODO_CREATED

  - on: UPDATE_TODO
    steps:
      - action: db.update
        table: todos
    emit: TODO_UPDATED

  - on: DELETE_TODO
    steps:
      - action: db.delete
        table: todos
        where: "id = '$event.payload.id'"
    emit: TODO_DELETED
```

### 2. User Registration with Upsert
```yaml
routes:
  - on: REGISTER_USER
    steps:
      - action: set
        id: $uuid
        created_at: $now
        status: active
      - action: db.upsert
        table: users
        key: email
    emit: USER_REGISTERED
```

### 3. Webhook with Retry
```yaml
routes:
  - on: ORDER_PLACED
    steps:
      - action: db.insert
        table: orders
      - action: http.post
        url: https://hooks.stripe.com/charge
        max_attempts: 3
        backoff_ms: 2000
        on_failure: PAYMENT_FAILED
    emit: ORDER_CONFIRMED
```

### 4. Event Chaining
```yaml
routes:
  - on: USER_SIGNED_UP
    steps:
      - action: db.insert
        table: users
      - action: emit
        event: SEND_WELCOME_EMAIL
    emit: SIGNUP_COMPLETE

  - on: SEND_WELCOME_EMAIL
    steps:
      - action: http.post
        url: https://api.sendgrid.com/v3/mail/send
```

### 5. Conditional Routing
```yaml
routes:
  - on: PROCESS_PAYMENT
    if: "$event.payload.amount > 0"
    steps:
      - action: db.insert
        table: payments
      - action: log.write
        message: "Payment of $event.payload.amount processed"
    emit: PAYMENT_PROCESSED

  - on: PROCESS_PAYMENT
    if: "$event.payload.amount == 0"
    steps:
      - action: log.write
        message: "Skipped zero-amount payment"
```

### 6. Multi-Tenancy
```yaml
spine_version: 1
tenant: acme_corp

database:
  tables:
    - projects

routes:
  - on: CREATE_PROJECT
    steps:
      - action: set
        id: $uuid
        tenant_id: acme_corp
      - action: db.insert
        table: projects
```

---

## CLI Commands

```bash
spine serve app.spine --port 8080 --db data.db --api-key SECRET
spine dev app.spine                  # Hot-reload dev server
spine emit EVENT_NAME --payload '{"key":"value"}'
spine init myapp --template chat     # Templates: chat, dashboard, iot
spine deploy fly                     # Generate fly.toml + Dockerfile
spine codegen app.spine              # Generate TypeScript types
spine context app.spine Dashboard    # Print one node's contract slice — paste into an AI session
                                     # so it can edit that page without reading the codebase
spine replay app.spine               # Replay audit log events
spine test app.spine                 # Run manifest-defined tests
```

---

## Database Connection Strings

| Format | Driver |
|---|---|
| `data.db` | SQLite (local file, WAL mode) |
| `libsql://your-db.turso.io` | Turso / libSQL (cloud) |
| `turso://your-db.turso.io` | Turso / libSQL (cloud) |

---

## Common Mistakes to AVOID

1. **Don't forget `spine_version: 1`** — it's required, the parser will reject files without it.

2. **Don't use undefined events in routes** — every `on: EVENT_NAME` must match an event declared in a node's `emits`.

3. **Don't omit `key` in `db.upsert`** — it's required and the payload must include that field.

4. **Don't write raw SQL in `where` clauses** — use the `column op value` format (e.g. `where: "status = '$event.payload.status'"`). It is parsed and parameterized, so template values are injection-safe. Compound conditions (`AND`/`OR`) are not supported in a single `where`.

5. **Don't declare duplicate node names** — the parser will error with line numbers.

6. **Don't create circular includes** — `a.spine → b.spine → a.spine` is detected and rejected.

7. **Don't use `parallel: true` with steps that depend on each other** — parallel steps get independent deep copies of the payload. Step A's mutations won't be visible to Step B.

8. **Don't forget `emit:` if you want WebSocket clients to receive state updates** — the `emit` field is what triggers WebSocket broadcasting.

---

## Architecture Summary

```
Client → HTTP/WS → Auth Middleware → Rate Limiter → Event Bus
                                                       ↓
                                              Contract Validation
                                                       ↓
                                              Route Step Execution
                                              (sequential or parallel)
                                                       ↓
                                    ┌──────────────────┬────────────────┐
                                    ↓                  ↓                ↓
                             Sharded Writer      Outbox Queue      WS Hub
                             (batched SQLite)    (webhook retry)   (broadcast)
```

- **Lock-free registry**: Route lookups via `atomic.LoadPointer` (~66ns)
- **Sharded write channels**: N input channels (N = NumCPU, capped 4-16) distribute producer contention
- **Single batch writer**: One goroutine drains all shards, flushes in batched transactions
- **Async WebSocket hub**: Buffered channel decouples emit path from fan-out
- **State cache**: `sync.Map` for lock-free sub-microsecond reads
