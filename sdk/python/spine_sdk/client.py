"""
SpineClient — Full-featured Python client for Spine event-driven backends.

Provides synchronous HTTP methods (emit, query, health) and an async WebSocket
connection for real-time state subscriptions with auto-reconnect and event replay.

Example:
    from spine_sdk import SpineClient

    client = SpineClient(base_url="http://localhost:8080", api_key="sk-dev")

    # Emit an event
    result = client.emit("USER_SIGNUP", {"email": "jane@example.com", "plan": "pro"})

    # Query table rows
    rows = client.query_table("users", limit=20, where={"plan": "pro"})

    # Real-time subscriptions (runs in background thread)
    def on_update(state: str, payload: dict):
        print(f"State changed: {state}", payload)

    client.subscribe("USER_STATUS", on_update)
    client.connect()  # starts WebSocket in background thread
"""

from __future__ import annotations

import asyncio
import json
import logging
import threading
from typing import Any, Callable, Dict, List, Optional, Set
from urllib.parse import urlencode, urljoin

import httpx
import websockets
import websockets.exceptions

from spine_sdk.types import (
    EmitResponse,
    EventLog,
    QueryOptions,
    TableInfo,
    TableQueryResponse,
)

logger = logging.getLogger("spine_sdk")


class SpineClient:
    """Synchronous + async Python client for a Spine backend.

    HTTP operations (emit, query_table, get_tables, etc.) are synchronous and
    use ``httpx``.  WebSocket subscriptions run in a background thread with an
    internal ``asyncio`` event loop.
    """

    def __init__(
        self,
        base_url: str,
        api_key: Optional[str] = None,
        ws_url: Optional[str] = None,
        auto_reconnect: bool = True,
        reconnect_interval_s: float = 3.0,
        timeout_s: float = 10.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._auto_reconnect = auto_reconnect
        self._auto_reconnect_option = auto_reconnect  # configured value; connect() restores it
        self._reconnect_interval_s = reconnect_interval_s

        # Derive WebSocket URL from base_url if not provided
        if ws_url:
            self._ws_url = ws_url
        else:
            scheme = "wss" if self._base_url.startswith("https") else "ws"
            host = self._base_url.replace("https://", "").replace("http://", "")
            self._ws_url = f"{scheme}://{host}/ws"

        # HTTP client (connection-pooled)
        headers: Dict[str, str] = {}
        if self._api_key:
            headers["X-API-Key"] = self._api_key
        self._http = httpx.Client(
            base_url=self._base_url,
            headers=headers,
            timeout=timeout_s,
        )

        # Subscription state
        self._subscriptions: Dict[str, Set[Callable]] = {}
        self._last_seen_id: int = 0
        self._connected = False

        # Background WebSocket thread
        self._ws_thread: Optional[threading.Thread] = None
        self._ws_loop: Optional[asyncio.AbstractEventLoop] = None
        self._ws_stop = threading.Event()

    # ------------------------------------------------------------------
    # HTTP: Emit
    # ------------------------------------------------------------------

    def emit(
        self,
        event: str,
        payload: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
    ) -> EmitResponse:
        """Emit an event to the Spine backend via ``POST /emit``.

        Args:
            event: The event name (e.g. ``"SUBMIT_LEAD"``).
            payload: Key-value payload data.
            idempotency_key: Optional dedup key.

        Returns:
            An :class:`EmitResponse` with status and emitted states.

        Raises:
            httpx.HTTPStatusError: If the server returns a 4xx/5xx.
        """
        body: Dict[str, Any] = {"event": event, "payload": dict(payload or {})}
        if idempotency_key:
            body["payload"]["_idempotency_key"] = idempotency_key

        resp = self._http.post("/emit", json=body)
        resp.raise_for_status()
        return EmitResponse.from_dict(resp.json())

    # ------------------------------------------------------------------
    # HTTP: Tables
    # ------------------------------------------------------------------

    def get_tables(self) -> List[TableInfo]:
        """List all tables with row counts via ``GET /tables``.

        Returns:
            List of :class:`TableInfo` objects.
        """
        resp = self._http.get("/tables")
        resp.raise_for_status()
        data = resp.json()
        tables = data.get("tables", [])
        return [TableInfo(name=t["name"], rows=t.get("rows", 0)) for t in tables]

    def query_table(
        self,
        table: str,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
        cursor: Optional[int] = None,
        where: Optional[Dict[str, str]] = None,
    ) -> TableQueryResponse:
        """Query rows from a table via ``GET /tables/{name}``.

        Supports limit/offset pagination, keyset cursor pagination, and
        column-equality filters.

        Args:
            table: Table name.
            limit: Max rows to return (1–500).
            offset: Offset for limit/offset pagination.
            cursor: ``_spine_id`` cursor for keyset pagination.
            where: Column-equality filters (``{"status": "active"}``).

        Returns:
            A :class:`TableQueryResponse` with rows and optional next_cursor.
        """
        params: List[tuple] = []
        if limit is not None:
            params.append(("limit", str(limit)))
        if offset is not None:
            params.append(("offset", str(offset)))
        if cursor is not None:
            params.append(("cursor", str(cursor)))
        if where:
            for col, val in where.items():
                params.append(("where", f"{col}:{val}"))

        url = f"/tables/{table}"
        if params:
            url += "?" + urlencode(params)

        resp = self._http.get(url)
        resp.raise_for_status()
        return TableQueryResponse.from_dict(resp.json())

    # ------------------------------------------------------------------
    # HTTP: Events
    # ------------------------------------------------------------------

    def get_events(
        self,
        event: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> List[EventLog]:
        """Query the event audit log via ``GET /events``.

        Args:
            event: Filter by event name (optional).
            limit: Max events to return.
            offset: Offset for pagination.

        Returns:
            List of :class:`EventLog` entries.
        """
        params: Dict[str, str] = {}
        if event:
            params["event"] = event
        if limit is not None:
            params["limit"] = str(limit)
        if offset is not None:
            params["offset"] = str(offset)

        resp = self._http.get("/events", params=params)
        resp.raise_for_status()
        data = resp.json()
        events = data.get("events", [])
        return [
            EventLog(
                id=e.get("id", 0),
                event=e.get("event", ""),
                payload=e.get("payload", {}),
                emitted_states=e.get("emitted_states", []),
                created_at=e.get("created_at", ""),
            )
            for e in events
        ]

    # ------------------------------------------------------------------
    # HTTP: Schema & Health
    # ------------------------------------------------------------------

    def get_schema(self) -> Dict[str, Any]:
        """Fetch the live manifest schema via ``GET /schema``.

        Returns:
            The parsed schema as a dictionary.
        """
        resp = self._http.get("/schema")
        resp.raise_for_status()
        return resp.json()

    def health(self) -> Dict[str, Any]:
        """Check backend health via ``GET /health``.

        Returns:
            Health status dictionary (e.g. ``{"status": "ok"}``).
        """
        resp = self._http.get("/health")
        resp.raise_for_status()
        return resp.json()

    # ------------------------------------------------------------------
    # WebSocket: Subscribe
    # ------------------------------------------------------------------

    def subscribe(
        self,
        state_name: str,
        callback: Callable[[str, Dict[str, Any]], None],
    ) -> Callable[[], None]:
        """Subscribe to real-time state broadcasts.

        The callback is invoked with ``(state_name, payload)`` each time
        the backend emits the given state over WebSocket.

        Args:
            state_name: The state to subscribe to (e.g. ``"LEAD_STATUS"``).
            callback: Function called on each broadcast.

        Returns:
            An unsubscribe function — call it to remove this callback.
        """
        if state_name not in self._subscriptions:
            self._subscriptions[state_name] = set()
        self._subscriptions[state_name].add(callback)

        def unsubscribe() -> None:
            cbs = self._subscriptions.get(state_name)
            if cbs:
                cbs.discard(callback)
                if not cbs:
                    del self._subscriptions[state_name]

        return unsubscribe

    # ------------------------------------------------------------------
    # WebSocket: Connect / Disconnect
    # ------------------------------------------------------------------

    def connect(self) -> None:
        """Start the WebSocket connection in a background thread.

        Handles authentication, auto-reconnect, and missed-event replay.
        This method returns immediately; messages are dispatched to
        registered subscription callbacks.

        A prior ``disconnect()`` does not permanently disable reconnection:
        every connect() restores the configured ``auto_reconnect`` value.
        """
        if self._ws_thread and self._ws_thread.is_alive():
            return

        self._auto_reconnect = self._auto_reconnect_option
        self._ws_stop.clear()
        self._ws_thread = threading.Thread(
            target=self._ws_run_loop,
            daemon=True,
            name="spine-ws",
        )
        self._ws_thread.start()

    def disconnect(self) -> None:
        """Close the WebSocket connection and clear all subscriptions."""
        self._auto_reconnect = False
        self._ws_stop.set()

        if self._ws_loop and self._ws_loop.is_running():
            self._ws_loop.call_soon_threadsafe(self._ws_loop.stop)

        if self._ws_thread and self._ws_thread.is_alive():
            self._ws_thread.join(timeout=5.0)

        self._ws_thread = None
        self._ws_loop = None
        self._subscriptions.clear()
        self._connected = False

    def close(self) -> None:
        """Shut down the client: close WebSocket and HTTP connections."""
        self.disconnect()
        self._http.close()

    @property
    def is_connected(self) -> bool:
        """Whether the WebSocket is currently connected."""
        return self._connected

    # ------------------------------------------------------------------
    # WebSocket: Internal Event Loop
    # ------------------------------------------------------------------

    def _ws_run_loop(self) -> None:
        """Entry point for the background WebSocket thread."""
        self._ws_loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._ws_loop)
        try:
            self._ws_loop.run_until_complete(self._ws_connect())
        except Exception:
            logger.debug("WebSocket loop exited", exc_info=True)
        finally:
            self._ws_loop.close()

    async def _ws_connect(self) -> None:
        """Async WebSocket connection with auth, reconnect, and replay."""
        while not self._ws_stop.is_set():
            try:
                url = self._ws_url
                if self._api_key:
                    url += f"?token={self._api_key}"

                async with websockets.connect(url) as ws:
                    is_reconnection = not self._connected and self._last_seen_id > 0
                    self._connected = True
                    logger.info("WebSocket connected to %s", self._ws_url)

                    # Auth handshake
                    if self._api_key:
                        await ws.send(json.dumps({
                            "type": "auth",
                            "token": self._api_key,
                        }))

                    # Replay missed events on reconnection
                    if is_reconnection:
                        await ws.send(json.dumps({
                            "type": "reconnect",
                            "last_seen_id": self._last_seen_id,
                        }))

                    # Message receive loop
                    async for raw in ws:
                        if self._ws_stop.is_set():
                            break
                        if self._handle_ws_message(raw):
                            # The server caps a replay at 500 rows; page until
                            # caught up.
                            await ws.send(json.dumps({
                                "type": "reconnect",
                                "last_seen_id": self._last_seen_id,
                            }))

            except websockets.exceptions.ConnectionClosed:
                logger.info("WebSocket connection closed")
            except Exception as exc:
                logger.warning("WebSocket error: %s", exc)
            finally:
                self._connected = False

            if not self._auto_reconnect or self._ws_stop.is_set():
                break

            logger.info(
                "Reconnecting in %.1fs...", self._reconnect_interval_s
            )
            await asyncio.sleep(self._reconnect_interval_s)

    def _handle_ws_message(self, raw: str) -> bool:
        """Parse and dispatch a single WebSocket message.

        Returns True when the caller should request the next replay page
        (reconnect_ack with ``has_more``).
        """
        try:
            data = json.loads(raw)
        except json.JSONDecodeError:
            return False

        # Track event IDs for reconnect replay
        msg_id = data.get("id")
        if isinstance(msg_id, (int, float)):
            self._last_seen_id = max(self._last_seen_id, int(msg_id))

        msg_type = data.get("type", "")

        # Replay missed events from reconnect_ack. Audit-log rows carry
        # {id, event, payload, emitted_states} — dispatch the payload to
        # every state the event emitted (the old code looked for evt["state"],
        # which audit rows never have — replay silently dropped everything).
        if msg_type == "reconnect_ack":
            for evt in data.get("missed_events", []):
                # Advance the cursor from the replayed rows too — otherwise
                # every reconnect re-sends the same last_seen_id and
                # re-dispatches the same events.
                evt_id = evt.get("id")
                if isinstance(evt_id, (int, float)):
                    self._last_seen_id = max(self._last_seen_id, int(evt_id))
                payload = evt.get("payload")
                if not payload:
                    continue
                for state in evt.get("emitted_states", []) or []:
                    self._dispatch(state, payload)
            return bool(data.get("has_more"))

        # Standard state broadcast
        state = data.get("state")
        payload = data.get("payload")
        if state and payload:
            self._dispatch(state, payload)

        return False

    def _dispatch(self, state: str, payload: Dict[str, Any]) -> None:
        """Invoke all registered callbacks for a state."""
        callbacks = self._subscriptions.get(state)
        if not callbacks:
            return
        for cb in list(callbacks):
            try:
                cb(state, payload)
            except Exception:
                logger.exception("Subscription callback error for state '%s'", state)

    # ------------------------------------------------------------------
    # Context Manager
    # ------------------------------------------------------------------

    def __enter__(self) -> "SpineClient":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"SpineClient(base_url={self._base_url!r}, connected={self._connected})"
