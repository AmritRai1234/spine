package tests

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestMultiFileImportsAndCustomPlugins(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Child included manifest: auth.spine
	authManifest := `spine_version: 1
database:
  tables:
    - auth_users

nodes:
  AuthNode:
    emits:
      - event: AUTH_SUCCESS
        payload:
          user_id: string

routes:
  - on: AUTH_SUCCESS
    steps:
      - action: db.insert
        table: auth_users
        input: "$event.payload"
`
	authPath := filepath.Join(tempDir, "auth.spine")
	if err := os.WriteFile(authPath, []byte(authManifest), 0644); err != nil {
		t.Fatalf("failed to write auth.spine: %v", err)
	}

	// 2. Main manifest: app.spine importing auth.spine
	mainManifest := `spine_version: 1

includes:
  - auth.spine

database:
  tables:
    - main_logs

nodes:
  MainNode:
    emits:
      - event: TRIGGER_CUSTOM
        payload:
          msg: string

routes:
  - on: TRIGGER_CUSTOM
    steps:
      - action: custom.audit
        message: "Custom plugin executed"
      - action: queue.publish
        table: "events_queue"
`
	mainPath := filepath.Join(tempDir, "app.spine")
	if err := os.WriteFile(mainPath, []byte(mainManifest), 0644); err != nil {
		t.Fatalf("failed to write app.spine: %v", err)
	}

	dbPath := filepath.Join(tempDir, "plugin_test.db")
	engine, err := spine.NewFromFile(mainPath, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine with includes: %v", err)
	}
	defer engine.Close()

	// Verify imported schema tables
	tables, err := engine.Bus.GetTables()
	if err != nil {
		t.Fatalf("failed to get tables: %v", err)
	}
	tableNames := make(map[string]bool)
	for _, tbl := range tables {
		tableNames[tbl.Name] = true
	}

	// Check if auth_users from auth.spine was imported
	if !tableNames["auth_users"] {
		t.Errorf("expected included table 'auth_users' to exist in database schema")
	}

	// Test 3: Register Custom Plugin Action Handler
	var pluginExecCount uint64
	engine.Bus.RegisterAction("custom.audit", func(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
		atomic.AddUint64(&pluginExecCount, 1)
		return nil
	})

	// Emit TRIGGER_CUSTOM event
	_, err = engine.Bus.Emit("TRIGGER_CUSTOM", map[string]interface{}{"msg": "Hello Plugin"})
	if err != nil {
		t.Fatalf("failed to emit TRIGGER_CUSTOM event: %v", err)
	}

	// Poll: the route runs synchronously through execStep, so the counter
	// must reach 1 well within the deadline (a fixed sleep was flaky under load).
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadUint64(&pluginExecCount) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadUint64(&pluginExecCount); got != 1 {
		t.Errorf("expected custom plugin action 'custom.audit' to execute 1 time, got %d", got)
	}
}
