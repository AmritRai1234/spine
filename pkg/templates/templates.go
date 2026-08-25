// Package templates holds embedded project scaffolds for `spine init`.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:shadcn
var shadcnFS embed.FS

// ShadcnManifest is the starter .spine manifest for the shadcn/ui template:
// NEW_TASK events persist to a tasks table and broadcast TASK_CREATED states.
const ShadcnManifest = `spine_version: 1

database:
  tables:
    - tasks

nodes:
  Dashboard:
    emits:
      - event: NEW_TASK
        payload:
          title: string
    listens:
      - state: TASK_CREATED
        payload:
          title: string

routes:
  - on: NEW_TASK
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.insert
        table: tasks
      - action: log.write
        message: "Task created: $event.payload.title"
    emit: TASK_CREATED
`

// ShadcnEnvExample is the environment template accompanying the scaffold.
const ShadcnEnvExample = `SPINE_URL=http://localhost:8080
SPINE_API_KEY=
SPINE_PORT=8080
SPINE_DB=spine.db
`

// ScaffoldShadcn writes the shadcn/ui template project into targetDir:
// app.spine, .env.example, and a Vite + React + Tailwind v4 + shadcn/ui web/ tree.
func ScaffoldShadcn(targetDir string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "app.spine"), []byte(ShadcnManifest), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(targetDir, ".env.example"), []byte(ShadcnEnvExample), 0644); err != nil {
		return err
	}

	return fs.WalkDir(shadcnFS, "shadcn/web", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel("shadcn/web", path)
		if relErr != nil {
			return relErr
		}
		dest := filepath.Join(targetDir, "web", rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		data, readErr := shadcnFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(dest, data, 0644)
	})
}
