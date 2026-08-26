package features

import (
	"os"
	"path/filepath"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

func TestEnvironmentOverlays(t *testing.T) {
	dir := t.TempDir()
	baseManifest := filepath.Join(dir, "app.spine")
	prodManifest := filepath.Join(dir, "app.prod.spine")
	dbPath := filepath.Join(dir, "spine.db")

	baseContent := `spine_version: 1
database:
  tables:
    - base_table

routes:
  - on: BASE_EVENT
    steps:
      - action: db.insert
        table: base_table
`

	prodContent := `spine_version: 1
database:
  tables:
    - prod_table

routes:
  - on: PROD_EVENT
    steps:
      - action: db.insert
        table: prod_table
`

	if err := os.WriteFile(baseManifest, []byte(baseContent), 0644); err != nil {
		t.Fatalf("Failed to write base manifest: %v", err)
	}
	if err := os.WriteFile(prodManifest, []byte(prodContent), 0644); err != nil {
		t.Fatalf("Failed to write prod manifest: %v", err)
	}

	t.Setenv("SPINE_ENV", "prod")

	eng, err := spine.NewFromFile(baseManifest, dbPath)
	if err != nil {
		t.Fatalf("Failed to init engine with overlay: %v", err)
	}
	defer eng.Close()

	schema := eng.Bus.GetRegistry().GetSchema()

	foundProdTable := false
	for _, tbl := range schema.DbTables {
		if tbl == "prod_table" {
			foundProdTable = true
			break
		}
	}

	if !foundProdTable {
		t.Errorf("Expected prod_table from app.prod.spine overlay in schema.DbTables, got %v", schema.DbTables)
	}

	foundProdRoute := false
	for _, route := range schema.Routes {
		if route.OnEvent == "PROD_EVENT" {
			foundProdRoute = true
			break
		}
	}

	if !foundProdRoute {
		t.Error("Expected PROD_EVENT route from app.prod.spine overlay in schema.Routes")
	}
}
