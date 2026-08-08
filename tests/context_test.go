package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// TestBuildNodeContext verifies the per-node context slice contains the node's
// contract, outgoing/incoming/failure routes — and nothing else.
func TestBuildNodeContext(t *testing.T) {
	tempDir := t.TempDir()
	spineFile := filepath.Join(tempDir, "ctx.spine")

	manifestContent := `spine_version: 1
database:
  tables:
    - orders
    - audit_logs

nodes:
  Dashboard:
    owns_files:
      - src/pages/Dashboard.tsx
    emits:
      - event: CREATE_ORDER
        payload:
          item: string
          quantity: number
    listens:
      - state: ORDER_CREATED
        payload:
          order_id: string
      - state: ORDER_FAILED

  AdminPanel:
    emits:
      - event: DELETE_USER
        payload:
          user_id: string

routes:
  - on: CREATE_ORDER
    on_failure: ORDER_FAILED
    steps:
      - action: db.insert
        table: orders
    emit: ORDER_CREATED

  - on: ORDER_FAILED
    steps:
      - action: db.insert
        table: audit_logs

  - on: DELETE_USER
    steps:
      - action: db.delete
        table: orders
        where: "id = 'x'"
`
	if err := os.WriteFile(spineFile, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	schema, err := manifest.ParseManifest(spineFile)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	ctx, err := manifest.BuildNodeContext(schema, "Dashboard")
	if err != nil {
		t.Fatalf("BuildNodeContext failed: %v", err)
	}

	if ctx.Node.Name != "Dashboard" {
		t.Errorf("expected node Dashboard, got %s", ctx.Node.Name)
	}

	// Outgoing: CREATE_ORDER route only (not DELETE_USER)
	if len(ctx.OutgoingRoutes) != 1 || ctx.OutgoingRoutes[0].OnEvent != "CREATE_ORDER" {
		t.Fatalf("expected 1 outgoing route (CREATE_ORDER), got %+v", ctx.OutgoingRoutes)
	}

	// Incoming: the CREATE_ORDER route emits ORDER_CREATED which Dashboard listens to
	if len(ctx.IncomingRoutes) != 1 || ctx.IncomingRoutes[0].EmitState != "ORDER_CREATED" {
		t.Fatalf("expected 1 incoming route (emit ORDER_CREATED), got %+v", ctx.IncomingRoutes)
	}

	// Failure: ORDER_FAILED handler pulled in via on_failure of the outgoing route
	if len(ctx.FailureRoutes) != 1 || ctx.FailureRoutes[0].OnEvent != "ORDER_FAILED" {
		t.Fatalf("expected 1 failure route (ORDER_FAILED), got %+v", ctx.FailureRoutes)
	}

	// Contract fields preserved
	if len(ctx.Node.Emits) != 1 || len(ctx.Node.Emits[0].Fields) != 2 {
		t.Errorf("expected emit contract with 2 fields, got %+v", ctx.Node.Emits)
	}
	if len(ctx.Node.OwnsFiles) != 1 || ctx.Node.OwnsFiles[0] != "src/pages/Dashboard.tsx" {
		t.Errorf("expected owns_files preserved, got %+v", ctx.Node.OwnsFiles)
	}

	// Unknown node errors with the list of available nodes
	_, err = manifest.BuildNodeContext(schema, "NoSuchPage")
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
	if !strings.Contains(err.Error(), "Dashboard") || !strings.Contains(err.Error(), "AdminPanel") {
		t.Errorf("error should list available nodes, got: %v", err)
	}

	// Unrelated node must not leak into another node's context
	adminCtx, err := manifest.BuildNodeContext(schema, "AdminPanel")
	if err != nil {
		t.Fatalf("BuildNodeContext(AdminPanel) failed: %v", err)
	}
	for _, r := range adminCtx.OutgoingRoutes {
		if r.OnEvent == "CREATE_ORDER" {
			t.Error("AdminPanel context leaked Dashboard's CREATE_ORDER route")
		}
	}
	if len(adminCtx.IncomingRoutes) != 0 {
		t.Errorf("AdminPanel listens to nothing, expected 0 incoming routes, got %d", len(adminCtx.IncomingRoutes))
	}
}
