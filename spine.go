// Package spine provides an event-driven runtime engine that reads
// a declarative .spine manifest and provides HTTP/WebSocket APIs for
// event emission, payload validation, SQLite persistence, and
// real-time state broadcasting.
//
// Usage as a library:
//
//	schema, _ := spine.ParseManifest("app.spine")
//	engine := spine.New(schema, "data.db")
//	engine.ListenAndServe(":8080")
//
// Usage as a CLI:
//
//	go install spine-go/cmd/spine@latest
//	spine --port 8080 app.spine
package spine

// Version is the library version.
const Version = "1.0.0"
