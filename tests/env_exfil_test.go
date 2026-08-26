package tests

// Template-injection regression tests: client-supplied payload values must be
// treated as literal data, never resolved as $env/$now/$uuid templates.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
)

// TestPayloadTemplatesAreLiteral verifies that a payload value starting with
// '$' is stored verbatim. Regression: normalizeParam used to resolve any
// payload string beginning with '$' via ResolveVariables, so a client could
// emit {"note": "$env.STRIPE_SECRET_KEY"} and read the server's secret back
// from the table (env-var exfiltration).
func TestPayloadTemplatesAreLiteral(t *testing.T) {
	t.Setenv("SPINE_TEST_LEAK_SECRET", "super-secret-value-42")

	dir := t.TempDir()
	manifestContent := `spine_version: 1

database:
  tables:
    - notes

nodes:
  - name: NotesNode
    emits:
      - event: ADD_NOTE
        payload:
          note: string

routes:
  - on: ADD_NOTE
    steps:
      - action: db.insert
        table: notes
        sync: "true"
`
	manifestPath := filepath.Join(dir, "exfil.spine")
	dbPath := filepath.Join(dir, "exfil.db")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("Failed to write test manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize engine: %v", err)
	}
	defer eng.Close()

	literal := "$env.SPINE_TEST_LEAK_SECRET"
	if _, err := eng.Bus.Emit("ADD_NOTE", map[string]interface{}{"note": literal}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	var stored string
	if err := eng.Bus.DB().QueryRow(`SELECT "note" FROM notes LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("failed to read stored note: %v", err)
	}
	if stored != literal {
		t.Errorf("payload value must be stored literally, got %q (want %q)", stored, literal)
	}
	if strings.Contains(stored, "super-secret-value-42") {
		t.Errorf("server secret leaked into stored payload value: %q", stored)
	}
}
