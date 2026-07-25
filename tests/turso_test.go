package tests

import (
	"os"
	"testing"

	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestTursoDriverIntegration(t *testing.T) {
	manifestContent := `
spine_version: 1

database:
  tables:
    - turso_leads

routes:
  - on: SUBMIT_TURSO_LEAD
    steps:
      - action: db.insert
        table: turso_leads
`

	tmpManifest, err := os.CreateTemp("", "turso_*.spine")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpManifest.Name())
	tmpManifest.WriteString(manifestContent)
	tmpManifest.Close()

	schema, err := manifest.ParseManifest(tmpManifest.Name())
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	dbPath := "test_turso.turso"
	defer os.Remove(dbPath)

	reg := manifest.NewRegistry(schema)
	hub := engine.NewHub()
	bus, err := engine.NewBus(reg, dbPath, hub)
	if err != nil {
		t.Fatalf("NewBus with Turso failed: %v", err)
	}
	defer bus.Close()

	res, err := bus.Emit("SUBMIT_TURSO_LEAD", map[string]interface{}{
		"email":  "turso@dev.com",
		"status": "active",
	})
	if err != nil {
		t.Fatalf("Emit to Turso driver failed: %v", err)
	}

	if status, ok := res["status"].(string); !ok || status != "ok" {
		t.Errorf("expected status 'ok', got %v", res)
	}
}
