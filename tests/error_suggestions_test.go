package tests

import (
	"os"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestErrorMessagesThatTeach(t *testing.T) {
	dir := t.TempDir()

	badTopManifest := `spine_version: 1

nodess:
  AuthNode:
    emits:
      - event: USER_LOGIN
`
	badTopPath := dir + "/typo_top.spine"
	if err := os.WriteFile(badTopPath, []byte(badTopManifest), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := manifest.ParseManifest(badTopPath)
	if err == nil {
		t.Fatal("Expected error for typo top-level key, got nil")
	}

	if !strings.Contains(err.Error(), "Did you mean 'nodes'?") {
		t.Errorf("Expected 'Did you mean' suggestion for top-level key, got: %v", err)
	}

	badEventManifest := `spine_version: 1

database:
  tables:
    - users

nodes:
  AuthNode:
    emits:
      - event: USER_SIGNUP

routes:
  - on: USER_SIGNP
    steps:
      - action: db.insert
        table: users
`
	badEventPath := dir + "/typo_event.spine"
	if err := os.WriteFile(badEventPath, []byte(badEventManifest), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = manifest.ParseManifest(badEventPath)
	if err == nil {
		t.Fatal("Expected error for typo event, got nil")
	}

	if !strings.Contains(err.Error(), "Did you mean 'USER_SIGNUP'?") {
		t.Errorf("Expected 'Did you mean' suggestion for event name, got: %v", err)
	}
}
