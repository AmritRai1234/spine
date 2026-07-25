package spine

import (
	"os"
	"testing"
)

func TestConditionalRoutesAndSteps(t *testing.T) {
	manifest := `
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
`

	tmpManifest, err := os.CreateTemp("", "cond_*.spine")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpManifest.Name())
	tmpManifest.WriteString(manifest)
	tmpManifest.Close()

	schema, err := ParseManifest(tmpManifest.Name())
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if len(schema.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(schema.Routes))
	}
	if schema.Routes[0].IfCondition != "$event.payload.role == 'admin'" {
		t.Errorf("expected route condition '$event.payload.role == 'admin'', got '%s'", schema.Routes[0].IfCondition)
	}

	dbPath := "test_cond.db"
	defer os.Remove(dbPath)

	reg := NewRegistry(schema)
	hub := NewHub()
	bus, err := NewBus(reg, dbPath, hub)
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
}
