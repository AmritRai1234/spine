import requests
import json
from typing import Dict, Any, Optional, List, Callable
import websocket
import threading

class SpineClient:
    """Python Client SDK for communicating with a running Spine Engine server."""

    def __init__(self, base_url: str = "http://localhost:8080", api_key: Optional[str] = None):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.headers = {"Content-Type": "application/json"}
        if self.api_key:
            self.headers["X-API-Key"] = self.api_key

    def emit(self, event: str, payload: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Emit an event to the Spine runtime engine."""
        url = f"{self.base_url}/emit"
        data = {"event": event, "payload": payload or {}}
        res = requests.post(url, headers=self.headers, json=data)
        res.raise_for_status()
        return res.json()

    def query_table(self, table: str, limit: int = 50, offset: int = 0, where: Optional[str] = None) -> List[Dict[str, Any]]:
        """Query rows from a Spine database table."""
        url = f"{self.base_url}/tables/{table}?limit={limit}&offset={offset}"
        if where:
            url += f"&where={where}"
        res = requests.get(url, headers=self.headers)
        res.raise_for_status()
        return res.json()

    def list_tables(self) -> List[str]:
        """List all database tables in the Spine schema."""
        url = f"{self.base_url}/tables"
        res = requests.get(url, headers=self.headers)
        res.raise_for_status()
        return res.json().get("tables", [])

    def health(self) -> Dict[str, Any]:
        """Check Spine server health status."""
        url = f"{self.base_url}/health"
        res = requests.get(url)
        res.raise_for_status()
        return res.json()

    def connect_websocket(self, on_state: Callable[[Dict[str, Any]], None], on_error: Optional[Callable[[Exception], None]] = None):
        """Connect to Spine real-time WebSocket state broadcasting hub."""
        ws_scheme = "wss" if self.base_url.startswith("https") else "ws"
        host = self.base_url.split("://")[1]
        ws_url = f"{ws_scheme}://{host}/ws"
        if self.api_key:
            ws_url += f"?token={self.api_key}"

        def _on_message(ws, message):
            try:
                data = json.loads(message)
                on_state(data)
            except Exception as e:
                if on_error:
                    on_error(e)

        ws = websocket.WebSocketApp(ws_url, on_message=_on_message)
        wst = threading.Thread(target=ws.run_forever, daemon=True)
        wst.start()
        return ws
