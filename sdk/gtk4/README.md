# Spine GTK4 Desktop Integration SDK

The **Spine GTK4 SDK** enables desktop applications built with the **GTK4 framework** (C, Python PyGObject, Go) to bind directly to Spine's event-driven runtime and real-time WebSocket state broadcasting hub.

---

## Key Design Principles

1. **Thread-Safe Main Loop Dispatch**: GTK4 UI widgets cannot be safely mutated from background networking threads. The Spine GTK4 SDK automatically marshals state broadcasts onto the GTK4 main thread using `GLib.idle_add()` (Python) or `g_idle_add()` (C).
2. **Asynchronous Event Emission**: Emitting events (`SUBMIT_LEAD`, `UPDATE_PREFERENCES`) from GTK buttons occurs asynchronously without blocking GTK UI rendering or frame rates.

---

## Directory Structure

```
sdk/gtk4/
├── spine_gtk4.h          # C GTK4 Header
├── spine_gtk4.c          # C GTK4 SDK Implementation
├── spine_gtk4.py         # PyGObject / GTK4 Python SDK Class
├── example_gtk4_app.py   # Complete Runnable GTK4 Desktop Application
└── README.md
```

---

## Quick Start (Python PyGObject)

### 1. Install GTK4 Dependencies

On Ubuntu / Debian:
```bash
sudo apt update
sudo apt install -y python3-gi python3-gi-cairo libgtk-4-dev
pip install requests websocket-client
```

On Fedora:
```bash
sudo dnf install -y python3-gobject gtk4-devel
```

### 2. Run Example GTK4 Desktop App

Start Spine backend server:
```bash
spine serve examples/app.spine --port 8080
```

In another terminal, launch the GTK4 application:
```bash
python3 sdk/gtk4/example_gtk4_app.py
```

---

## Code Example

```python
from gi.repository import Gtk
from spine_gtk4 import SpineGTK4Client

# Initialize Spine GTK4 client
client = SpineGTK4Client("http://localhost:8080")
client.connect_websocket()

# Listen to Spine state broadcasts and update GTK4 UI widgets safely
def on_status_update(payload):
    label.set_text(f"New Status: {payload.get('status')}")

client.listen_state("LEAD_STATUS", on_status_update)

# Emit event when GTK4 Button is clicked
def on_button_clicked(button):
    client.emit("SUBMIT_LEAD", {"email": "gtk4@example.com"})

button.connect("clicked", on_button_clicked)
```
