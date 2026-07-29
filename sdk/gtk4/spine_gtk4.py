import json
import threading
import requests
import websocket
from typing import Callable, Dict, Any, Optional

try:
    from gi.repository import GLib, Gtk
except ImportError:
    GLib = None
    Gtk = None

class SpineGTK4Client:
    """GTK4 PyGObject Integration Client for Spine.
    
    Safe thread dispatch guarantees that all state updates received over WebSocket
    are scheduled directly onto the GTK4 main thread using GLib.idle_add().
    """

    def __init__(self, base_url: str = "http://localhost:8080", api_key: Optional[str] = None):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.listeners: Dict[str, list] = {}
        self.headers = {"Content-Type": "application/json"}
        if self.api_key:
            self.headers["X-API-Key"] = self.api_key

    def emit(self, event: str, payload: Optional[Dict[str, Any]] = None):
        """Emit an event asynchronously to Spine."""
        def _emit_worker():
            try:
                url = f"{self.base_url}/emit"
                requests.post(url, headers=self.headers, json={"event": event, "payload": payload or {}})
            except Exception as e:
                print(f"[SpineGTK4] Emit error: {e}")

        threading.Thread(target=_emit_worker, daemon=True).start()

    def listen_state(self, state_name: str, callback: Callable[[Dict[str, Any]], None]):
        """Subscribe a GTK4 UI callback to a Spine state broadcast."""
        if state_name not in self.listeners:
            self.listeners[state_name] = []
        self.listeners[state_name].append(callback)

    def _dispatch_to_gtk_main_thread(self, state_name: str, payload: Dict[str, Any]):
        """Schedules callback execution on GTK main thread using GLib.idle_add."""
        if state_name in self.listeners:
            for cb in self.listeners[state_name]:
                if GLib:
                    GLib.idle_add(cb, payload)
                else:
                    cb(payload)

    def connect_websocket(self):
        """Connect to Spine real-time WebSocket state broadcasting hub."""
        ws_scheme = "wss" if self.base_url.startswith("https") else "ws"
        host = self.base_url.split("://")[1]
        ws_url = f"{ws_scheme}://{host}/ws"
        if self.api_key:
            ws_url += f"?token={self.api_key}"

        def _on_message(ws, message):
            try:
                msg = json.loads(message)
                if msg.get("type") == "state":
                    state_name = msg.get("state")
                    payload = msg.get("payload", {})
                    self._dispatch_to_gtk_main_thread(state_name, payload)
            except Exception as e:
                print(f"[SpineGTK4] WS message error: {e}")

        ws = websocket.WebSocketApp(ws_url, on_message=_on_message)
        threading.Thread(target=ws.run_forever, daemon=True).start()
        return ws
