# Spine Python SDK

Official Python client for [Spine](https://github.com/AmritRai1234/spine) event-driven backends.

## Installation

```bash
pip install sdk/python/
```

Or install from the repository:

```bash
pip install git+https://github.com/AmritRai1234/spine.git#subdirectory=sdk/python
```

## Quick Start

```python
from spine_sdk import SpineClient

# Initialize client
client = SpineClient(
    base_url="http://localhost:8080",
    api_key="your-api-key",  # optional
)

# Emit an event
result = client.emit("SUBMIT_LEAD", {
    "email": "jane@example.com",
    "name": "Jane Doe",
})
print(result.status)           # "ok"
print(result.emitted_states)   # ["LEAD_STATUS"]

# Query table rows
response = client.query_table("leads", limit=20)
for row in response.rows:
    print(row["email"], row["name"])

# Filter by column
response = client.query_table("leads", where={"status": "active"})

# Cursor pagination
response = client.query_table("leads", limit=10)
if response.next_cursor:
    next_page = client.query_table("leads", cursor=response.next_cursor, limit=10)
```

## Real-Time WebSocket Subscriptions

```python
from spine_sdk import SpineClient

client = SpineClient(base_url="http://localhost:8080", api_key="sk-dev")

# Subscribe to state changes
def on_lead_update(state: str, payload: dict):
    print(f"Lead status changed: {payload}")

unsubscribe = client.subscribe("LEAD_STATUS", on_lead_update)

# Start WebSocket connection (runs in background thread)
client.connect()

# ... your application logic ...

# Cleanup
unsubscribe()     # remove this specific callback
client.disconnect()  # close WebSocket
```

## API Reference

### `SpineClient(base_url, api_key=None, ws_url=None, auto_reconnect=True, reconnect_interval_s=3.0, timeout_s=10.0)`

Create a new Spine client.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `base_url` | `str` | required | Base URL of the Spine server |
| `api_key` | `str` | `None` | API key for authentication |
| `ws_url` | `str` | `None` | WebSocket URL (auto-derived from base_url) |
| `auto_reconnect` | `bool` | `True` | Auto-reconnect on WebSocket disconnect |
| `reconnect_interval_s` | `float` | `3.0` | Seconds between reconnect attempts |
| `timeout_s` | `float` | `10.0` | HTTP request timeout |

### HTTP Methods

| Method | Description |
|--------|-------------|
| `emit(event, payload, idempotency_key=None)` | Emit an event via `POST /emit` |
| `query_table(table, limit, offset, cursor, where)` | Query table rows via `GET /tables/{name}` |
| `get_tables()` | List all tables via `GET /tables` |
| `get_events(event, limit, offset)` | Query event audit log via `GET /events` |
| `get_schema()` | Fetch live schema via `GET /schema` |
| `health()` | Health check via `GET /health` |

### WebSocket Methods

| Method | Description |
|--------|-------------|
| `connect()` | Start WebSocket in background thread |
| `subscribe(state, callback)` | Subscribe to state broadcasts (returns unsubscribe fn) |
| `disconnect()` | Close WebSocket and clear subscriptions |
| `is_connected` | Property: whether WebSocket is active |

### Context Manager

```python
with SpineClient(base_url="http://localhost:8080") as client:
    result = client.emit("PING", {})
# Automatically closes HTTP + WebSocket connections
```

## Features

- **Synchronous HTTP** — `emit()`, `query_table()`, `get_tables()`, `get_events()`, `get_schema()`, `health()`
- **Real-time WebSocket** — Background thread with async event loop
- **Auto-reconnect** — Configurable reconnect with exponential backoff
- **Event replay** — Missed events replayed on reconnect via `last_seen_id`
- **Idempotency** — Pass `idempotency_key` to prevent duplicate processing
- **Connection pooling** — httpx client reuses TCP connections
- **Type-safe** — Full dataclass types for all responses
- **Context manager** — Clean resource cleanup with `with` statement

## Requirements

- Python ≥ 3.9
- `httpx` ≥ 0.25.0
- `websockets` ≥ 12.0
