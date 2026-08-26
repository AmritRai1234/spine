package features

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestConditionalRoutesAndSteps(t *testing.T) {
	manifestContent := `
spine_version: 1

database:
  tables:
    - audit

routes:
  - on: USER_REGISTER
    if: "$event.payload.role == 'admin'"
    steps:
      - action: log.write
        message: "Admin registered: $event.payload.email"
        if: "$event.payload.email contains 'admin.com'"
    emit: ADMIN_REGISTERED

  - on: SCORE_EVENT
    if: "$event.payload.score == 15.0"
    steps:
      - action: log.write
        message: "Score matched: $event.payload.score"
    emit: SCORE_MATCHED
`

	tmpManifest, err := os.CreateTemp("", "cond_*.spine")
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

	if len(schema.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(schema.Routes))
	}
	if schema.Routes[0].IfCondition != "$event.payload.role == 'admin'" {
		t.Errorf("expected route condition '$event.payload.role == 'admin'', got '%s'", schema.Routes[0].IfCondition)
	}
	if schema.Routes[1].IfCondition != "$event.payload.score == 15.0" {
		t.Errorf("expected route condition '$event.payload.score == 15.0', got '%s'", schema.Routes[1].IfCondition)
	}

	dbPath := filepath.Join(t.TempDir(), "cond.db")

	reg := manifest.NewRegistry(schema)
	hub := engine.NewHub()
	bus, err := engine.NewBus(reg, dbPath, hub)
	if err != nil {
		t.Fatalf("NewBus failed: %v", err)
	}
	defer bus.Close()

	// Case 1: Non-admin role (should skip route)
	res1, err := bus.Emit("USER_REGISTER", map[string]interface{}{
		"role":  "user",
		"email": "user@example.com",
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	emitted1, _ := res1["emitted_states"].([]string)
	if len(emitted1) != 0 {
		t.Errorf("expected 0 emitted states for non-admin, got %v", emitted1)
	}

	// Case 2: Admin role (should execute route & emit state)
	res2, err := bus.Emit("USER_REGISTER", map[string]interface{}{
		"role":  "admin",
		"email": "alex@admin.com",
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	emitted2, _ := res2["emitted_states"].([]string)
	if len(emitted2) != 1 || emitted2[0] != "ADMIN_REGISTERED" {
		t.Errorf("expected ['ADMIN_REGISTERED'], got %v", emitted2)
	}

	// Case 3: SCORE_EVENT with score=15 (decimal 15.0 == integer 15 → numeric equal)
	res3, err := bus.Emit("SCORE_EVENT", map[string]interface{}{
		"score": 15,
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	emitted3, _ := res3["emitted_states"].([]string)
	if len(emitted3) != 1 || emitted3[0] != "SCORE_MATCHED" {
		t.Errorf("expected ['SCORE_MATCHED'] for score=15, got %v", emitted3)
	}

	// Case 4: SCORE_EVENT with score=7 (different number → should skip)
	res4, err := bus.Emit("SCORE_EVENT", map[string]interface{}{
		"score": 7,
	})
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	emitted4, _ := res4["emitted_states"].([]string)
	if len(emitted4) != 0 {
		t.Errorf("expected 0 emitted states for score=7, got %v", emitted4)
	}
}
