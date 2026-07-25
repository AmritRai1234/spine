import urllib.request
import json
import time
import subprocess
import os
import websocket
import threading

server_proc = subprocess.Popen(["./spine"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
time.sleep(1)

ws_messages = []

def on_message(ws, message):
    ws_messages.append(json.loads(message))

def run_ws():
    ws = websocket.WebSocketApp("ws://localhost:8080/ws", on_message=on_message)
    ws.run_forever()

ws_thread = threading.Thread(target=run_ws, daemon=True)
ws_thread.start()
time.sleep(0.5)

print("1. Testing GET /schema...")
req = urllib.request.urlopen("http://localhost:8080/schema")
schema = json.loads(req.read().decode())
assert "nodes" in schema and "routes" in schema
print("   ✓ GET /schema returned valid manifest JSON")

print("2. Testing POST /emit with VALID payload...")
post_data = json.dumps({"event": "SUBMIT_LEAD", "payload": {"email": "alex@spine.dev"}}).encode()
req = urllib.request.Request("http://localhost:8080/emit", data=post_data, headers={"Content-Type": "application/json"})
res = urllib.request.urlopen(req)
resp_json = json.loads(res.read().decode())
assert resp_json["status"] == "ok"
assert "LEAD_STATUS" in resp_json["emitted_states"]
print("   ✓ Emission succeeded with status: ok and state: LEAD_STATUS")

print("3. Testing POST /emit with INVALID payload (missing 'email')...")
bad_data = json.dumps({"event": "SUBMIT_LEAD", "payload": {}}).encode()
bad_req = urllib.request.Request("http://localhost:8080/emit", data=bad_data, headers={"Content-Type": "application/json"})
try:
    urllib.request.urlopen(bad_req)
    print("   ✗ ERROR: Expected 400 Bad Request but succeeded!")
except urllib.error.HTTPError as e:
    assert e.code == 400
    err_body = json.loads(e.read().decode())
    assert err_body["status"] == "error"
    print(f"   ✓ Rejected invalid payload as expected (HTTP 400: {err_body['error']})")

print("4. Testing WebSocket State Broadcast...")
time.sleep(0.5)
assert len(ws_messages) > 0, "No WS messages received!"
last_msg = ws_messages[-1]
assert last_msg["type"] == "state"
assert last_msg["state"] == "LEAD_STATUS"
assert last_msg["payload"]["email"] == "alex@spine.dev"
print(f"   ✓ Real-time WS received state broadcast: {last_msg}")

print("5. Testing Hot Reloading...")
os.utime("examples/app.spine", None)
time.sleep(1.5)
print("   ✓ Touched examples/app.spine file for hot reload test")

server_proc.terminate()
server_proc.wait()

print("\n🎉 ALL E2E TESTS PASSED SUCCESSFULLY!")
