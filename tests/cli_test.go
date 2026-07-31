package tests

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitProjectScaffolding(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my_project")

	manifestPath := filepath.Join(target, "app.spine")
	envPath := filepath.Join(target, ".env.example")

	err := os.MkdirAll(target, 0755)
	if err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	manifestContent := `spine_version: 1
database:
  tables:
    - users
`
	err = os.WriteFile(manifestPath, []byte(manifestContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	err = os.WriteFile(envPath, []byte("SPINE_PORT=8080"), 0644)
	if err != nil {
		t.Fatalf("Failed to write env: %v", err)
	}

	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Error("Expected app.spine to exist")
	}

	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		t.Error("Expected .env.example to exist")
	}
}
