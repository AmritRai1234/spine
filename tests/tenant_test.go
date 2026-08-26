package tests

import (
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestTenantLabel(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	dbPath := filepath.Join(dir, "spine.db")

	manifest := `spine_version: 1
tenant: tenant_acme_corp

access:
  - role: tenant_admin
    key: "sk_tenant_secret"
    tenant: tenant_acme_corp

database:
  tables:
    - tenant_data

routes:
  - on: TENANT_EVENT
    steps:
      - action: db.insert
        table: tenant_data
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}
	defer eng.Close()

	schema := eng.Bus.GetRegistry().GetSchema()
	if schema.Tenant != "tenant_acme_corp" {
		t.Errorf("Expected manifest schema Tenant label 'tenant_acme_corp', got '%s'", schema.Tenant)
	}

	// The per-role `tenant:` key is inert metadata (single-tenant engine) —
	// it must parse without error but no longer implies row isolation.
	ac := eng.Access().Resolve("sk_tenant_secret")
	if ac == nil {
		t.Fatalf("Failed to resolve access context for sk_tenant_secret")
	}
	if ac.Role != "tenant_admin" {
		t.Errorf("Expected role 'tenant_admin', got '%s'", ac.Role)
	}
}
