package features

// Hot-reload tests: manifest + include watching, atomic registry swap,
// rollback on invalid manifests, and newly declared table creation.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// writeFileAtomic replaces a manifest via write-to-temp + rename so the
// watcher never observes a partially written file.
func writeFileAtomic(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("failed to rename into %s: %v", path, err)
	}
}

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

const hotReloadBaseManifest = `spine_version: 1

database:
  tables:
    - base_items

nodes:
  BaseNode:
    emits:
      - event: ADD_BASE_ITEM
        payload:
          name: string

routes:
  - on: ADD_BASE_ITEM
    steps:
      - action: db.insert
        table: base_items
`

func startHotReloadEngine(t *testing.T, manifestPath string) *spine.Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "reload.db")
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	eng.SetHotReloadInterval(100 * time.Millisecond)
	eng.StartHotReload()
	return eng
}

func TestHotReloadAddsRouteAndTable(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	writeFileAtomic(t, manifestPath, hotReloadBaseManifest)

	eng := startHotReloadEngine(t, manifestPath)
	defer eng.Close()

	// Sanity: base route works
	res, err := eng.Bus.Emit("ADD_BASE_ITEM", map[string]interface{}{"name": "pre"})
	if err != nil || res["status"] != "ok" {
		t.Fatalf("base emit failed: %v (res=%v)", err, res)
	}

	// Rewrite manifest with an additional node/event/route/table
	reloaded := hotReloadBaseManifest + `
nodes:
  ExtraNode:
    emits:
      - event: ADD_EXTRA_ITEM
        payload:
          title: string

routes:
  - on: ADD_EXTRA_ITEM
    steps:
      - action: db.insert
        table: extra_items
`
	// Full replacement manifest (append of nodes would duplicate keys)
	reloaded = `spine_version: 1

database:
  tables:
    - base_items
    - extra_items

nodes:
  BaseNode:
    emits:
      - event: ADD_BASE_ITEM
        payload:
          name: string
  ExtraNode:
    emits:
      - event: ADD_EXTRA_ITEM
        payload:
          title: string

routes:
  - on: ADD_BASE_ITEM
    steps:
      - action: db.insert
        table: base_items
  - on: ADD_EXTRA_ITEM
    steps:
      - action: db.insert
        table: extra_items
`
	writeFileAtomic(t, manifestPath, reloaded)

	// Wait for the watcher to swap the registry
	ok := waitForCond(t, 5*time.Second, func() bool {
		_, found := eng.Bus.GetRegistry().GetRoutes("ADD_EXTRA_ITEM")
		return found
	})
	if !ok {
		t.Fatal("hot-reload did not pick up new route ADD_EXTRA_ITEM within 5s")
	}

	// New route executes end-to-end
	if _, err := eng.Bus.Emit("ADD_EXTRA_ITEM", map[string]interface{}{"title": "post"}); err != nil {
		t.Fatalf("emit after reload failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	rows, err := eng.Bus.GetTableRows("extra_items", 10, 0)
	if err != nil {
		t.Fatalf("newly declared table missing after reload: %v", err)
	}
	if len(rows) != 1 || rows[0]["title"] != "post" {
		t.Errorf("expected 1 row {title: post} in extra_items, got %v", rows)
	}

	// Old route still intact after reload
	if _, err := eng.Bus.Emit("ADD_BASE_ITEM", map[string]interface{}{"name": "post2"}); err != nil {
		t.Errorf("base route broke after reload: %v", err)
	}
}

func TestHotReloadRollbackOnInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	writeFileAtomic(t, manifestPath, hotReloadBaseManifest)

	eng := startHotReloadEngine(t, manifestPath)
	defer eng.Close()

	// Write a syntactically invalid manifest
	writeFileAtomic(t, manifestPath, "spine_version: 1\nrouts:\n  - bad key here")

	// Give the watcher ample time to (attempt) reload
	time.Sleep(time.Second)

	// Previous schema must still serve traffic
	if _, err := eng.Bus.Emit("ADD_BASE_ITEM", map[string]interface{}{"name": "survivor"}); err != nil {
		t.Fatalf("previous schema should keep serving after failed reload: %v", err)
	}

	// Recovery: fixing the manifest resumes reloading
	writeFileAtomic(t, manifestPath, hotReloadBaseManifest+`
extra_marker: nothing`[0:0]) // same valid content; then add route via full rewrite below

	valid := `spine_version: 1

database:
  tables:
    - base_items
    - recovery_items

nodes:
  BaseNode:
    emits:
      - event: ADD_BASE_ITEM
        payload:
          name: string
      - event: ADD_RECOVERY_ITEM
        payload:
          tag: string

routes:
  - on: ADD_BASE_ITEM
    steps:
      - action: db.insert
        table: base_items
  - on: ADD_RECOVERY_ITEM
    steps:
      - action: db.insert
        table: recovery_items
`
	writeFileAtomic(t, manifestPath, valid)

	ok := waitForCond(t, 5*time.Second, func() bool {
		_, found := eng.Bus.GetRegistry().GetRoutes("ADD_RECOVERY_ITEM")
		return found
	})
	if !ok {
		t.Fatal("hot-reload did not recover after fixing the manifest")
	}
}

func TestHotReloadWatchesIncludes(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.spine")
	childPath := filepath.Join(dir, "child.spine")

	writeFileAtomic(t, childPath, `spine_version: 1

database:
  tables:
    - child_things

nodes:
  ChildNode:
    emits:
      - event: ADD_CHILD_THING
        payload:
          label: string

routes:
  - on: ADD_CHILD_THING
    steps:
      - action: db.insert
        table: child_things
`)
	writeFileAtomic(t, mainPath, `spine_version: 1

includes:
  - child.spine
`)

	eng := startHotReloadEngine(t, mainPath)
	defer eng.Close()

	// Edit ONLY the included file — root untouched
	writeFileAtomic(t, childPath, `spine_version: 1

database:
  tables:
    - child_things
    - child_extra

nodes:
  ChildNode:
    emits:
      - event: ADD_CHILD_THING
        payload:
          label: string
      - event: CHILD_PING
        payload:
          code: number

routes:
  - on: ADD_CHILD_THING
    steps:
      - action: db.insert
        table: child_things
  - on: CHILD_PING
    steps:
      - action: db.insert
        table: child_extra
`)

	ok := waitForCond(t, 5*time.Second, func() bool {
		_, found := eng.Bus.GetRegistry().GetRoutes("CHILD_PING")
		return found
	})
	if !ok {
		t.Fatal("editing an included manifest did not trigger hot-reload")
	}

	// Route from included file works end-to-end incl. new table creation
	if _, err := eng.Bus.Emit("CHILD_PING", map[string]interface{}{"code": 7.0}); err != nil {
		t.Fatalf("emit from included-file route failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	rows, err := eng.Bus.GetTableRows("child_extra", 10, 0)
	if err != nil {
		t.Fatalf("table declared in included manifest missing: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row in child_extra, got %d", len(rows))
	}
}

func TestHotReloadCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	writeFileAtomic(t, manifestPath, hotReloadBaseManifest)

	eng := startHotReloadEngine(t, manifestPath)
	if err := eng.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	// Second Close must not panic (double channel close guard)
	if err := eng.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}
