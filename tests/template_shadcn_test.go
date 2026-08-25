package tests

// Shadcn/ui template tests: the `spine init --template shadcn` scaffold must
// produce a valid manifest (bootable engine) and complete Vite frontend files.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/templates"
)

func TestShadcnTemplateScaffold(t *testing.T) {
	dir := t.TempDir()
	if err := templates.ScaffoldShadcn(dir); err != nil {
		t.Fatalf("ScaffoldShadcn failed: %v", err)
	}

	// Required project files
	for _, f := range []string{
		"app.spine",
		".env.example",
		filepath.Join("web", "package.json"),
		filepath.Join("web", "vite.config.ts"),
		filepath.Join("web", "tsconfig.json"),
		filepath.Join("web", "components.json"),
		filepath.Join("web", "index.html"),
		filepath.Join("web", "src", "main.tsx"),
		filepath.Join("web", "src", "App.tsx"),
		filepath.Join("web", "src", "index.css"),
		filepath.Join("web", "src", "lib", "spine.ts"),
		filepath.Join("web", "src", "lib", "utils.ts"),
		filepath.Join("web", "src", "hooks", "use-spine.ts"),
		filepath.Join("web", "src", "components", "ui", "button.tsx"),
		filepath.Join("web", "src", "components", "ui", "card.tsx"),
		filepath.Join("web", "src", "components", "ui", "input.tsx"),
		filepath.Join("web", "src", "components", "ui", "badge.tsx"),
		filepath.Join("web", "src", "components", "ui", "table.tsx"),
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("template file missing: %s", f)
		}
	}

	// shadcn-native: components.json must point at the vendored UI aliases
	data, err := os.ReadFile(filepath.Join(dir, "web", "components.json"))
	if err != nil {
		t.Fatalf("components.json unreadable: %v", err)
	}
	for _, want := range []string{`"@/components/ui"`, `"new-york"`, `"cssVariables": true`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("components.json missing %s", want)
		}
	}
}

func TestShadcnTemplateManifestBootsAndFlows(t *testing.T) {
	dir := t.TempDir()
	if err := templates.ScaffoldShadcn(dir); err != nil {
		t.Fatalf("ScaffoldShadcn failed: %v", err)
	}

	dbPath := filepath.Join(dir, "test.db")
	eng, err := spine.NewFromFile(filepath.Join(dir, "app.spine"), dbPath)
	if err != nil {
		t.Fatalf("template manifest failed to boot: %v", err)
	}
	defer eng.Close()

	res, err := eng.Bus.Emit("NEW_TASK", map[string]interface{}{"title": "hello"})
	if err != nil || res["status"] != "ok" {
		t.Fatalf("NEW_TASK emit failed: %v (res=%v)", err, res)
	}

	states, _ := res["emitted_states"].([]string)
	if len(states) == 0 || states[0] != "TASK_CREATED" {
		t.Errorf("expected TASK_CREATED broadcast, got %v", states)
	}

	time.Sleep(300 * time.Millisecond)
	rows, err := eng.Bus.GetTableRows("tasks", 10, 0)
	if err != nil {
		t.Fatalf("tasks table missing: %v", err)
	}
	if len(rows) != 1 || rows[0]["title"] != "hello" {
		t.Errorf("expected 1 task {title: hello}, got %v", rows)
	}

	// Frontend contract: rows expose id/title/created_at for the table UI
	row := rows[0]
	for _, col := range []string{"id", "title", "created_at"} {
		if _, ok := row[col]; !ok {
			t.Errorf("row missing %q column expected by web/src/App.tsx", col)
		}
	}
}
