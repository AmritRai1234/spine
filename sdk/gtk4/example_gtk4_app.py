#!/usr/bin/env python3
"""
GTK4 Desktop Application powered by Spine Backend Engine.

Prerequisites:
  sudo apt install python3-gi python3-gi-cairo libgtk-4-dev
  pip install requests websocket-client
"""

import sys
from spine_gtk4 import SpineGTK4Client

try:
    import gi
    gi.require_version('Gtk', '4.0')
    from gi.repository import Gtk, GLib
except ValueError:
    print("GTK4 is not installed on this system. Install via: sudo apt install python3-gi libgtk-4-dev")
    sys.exit(1)

class SpineGTK4Window(Gtk.ApplicationWindow):
    def __init__(self, app, client: SpineGTK4Client):
        super().__init__(application=app, title="Spine GTK4 Desktop Dashboard")
        self.client = client
        self.set_default_size(500, 350)

        # Header Bar
        header = Gtk.HeaderBar()
        self.set_titlebar(header)

        # Main Vertical Box
        vbox = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=16)
        vbox.set_margin_top(24)
        vbox.set_margin_bottom(24)
        vbox.set_margin_start(24)
        vbox.set_margin_end(24)
        self.set_child(vbox)

        # Title Label
        title_label = Gtk.Label(label="⚡ Spine GTK4 Real-Time Control Center")
        title_label.add_css_class("title-1")
        vbox.append(title_label)

        # Status Label (Updates live via Spine WebSocket state listener)
        self.status_label = Gtk.Label(label="Status: Waiting for event...")
        self.status_label.add_css_class("dim-label")
        vbox.append(self.status_label)

        # Input Entry
        self.entry = Gtk.Entry()
        self.entry.set_placeholder_text("Enter Lead Email / User Data...")
        vbox.append(self.entry)

        # Submit Button
        btn = Gtk.Button(label="Submit Lead to Spine")
        btn.add_css_class("suggested-action")
        btn.connect("clicked", self.on_submit_clicked)
        vbox.append(btn)

        # Register GTK4 State Listener on Spine State "LEAD_STATUS"
        self.client.listen_state("LEAD_STATUS", self.on_lead_status_received)

    def on_submit_clicked(self, button):
        text = self.entry.get_text().strip()
        if text:
            self.status_label.set_text(f"Emitting SUBMIT_LEAD for '{text}'...")
            self.client.emit("SUBMIT_LEAD", {"email": text, "name": "GTK4 User"})
            self.entry.set_text("")

    def on_lead_status_received(self, payload):
        """Executed on GTK main thread via GLib.idle_add."""
        status = payload.get("status", "Received")
        self.status_label.set_text(f"✅ Real-Time Update from Spine: {status}")

def on_activate(app):
    client = SpineGTK4Client("http://localhost:8080")
    client.connect_websocket()

    win = SpineGTK4Window(app, client)
    win.present()

def main():
    app = Gtk.Application(application_id="dev.spine.GTK4Demo")
    app.connect("activate", on_activate)
    return app.run(sys.argv)

if __name__ == "__main__":
    main()
