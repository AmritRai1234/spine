package engine

import (
	"fmt"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// ManifestDiff describes structural differences between two manifest schemas.
type ManifestDiff struct {
	AddedTables     []string
	RemovedTables   []string
	AddedRoutes     []string
	RemovedRoutes   []string
	AddedNodes      []string
	RemovedNodes    []string
	AccessChanged   bool
}

// HasChanges returns true if any structural changes exist between old and new schema.
func (d ManifestDiff) HasChanges() bool {
	return len(d.AddedTables) > 0 || len(d.RemovedTables) > 0 ||
		len(d.AddedRoutes) > 0 || len(d.RemovedRoutes) > 0 ||
		len(d.AddedNodes) > 0 || len(d.RemovedNodes) > 0 ||
		d.AccessChanged
}

// Summary returns a human-readable summary of the diff.
func (d ManifestDiff) Summary() string {
	if !d.HasChanges() {
		return "no manifest changes"
	}
	return fmt.Sprintf("+%d/-%d tables, +%d/-%d routes, +%d/-%d nodes (access changed: %t)",
		len(d.AddedTables), len(d.RemovedTables),
		len(d.AddedRoutes), len(d.RemovedRoutes),
		len(d.AddedNodes), len(d.RemovedNodes),
		d.AccessChanged)
}

// DiffManifests compares an old schema against a new schema and calculates topology differences.
func DiffManifests(oldSchema, newSchema *manifest.SpineSchema) ManifestDiff {
	diff := ManifestDiff{}

	// Diff Tables
	oldTables := make(map[string]bool)
	for _, t := range oldSchema.DbTables {
		oldTables[t] = true
	}
	newTables := make(map[string]bool)
	for _, t := range newSchema.DbTables {
		newTables[t] = true
		if !oldTables[t] {
			diff.AddedTables = append(diff.AddedTables, t)
		}
	}
	for _, t := range oldSchema.DbTables {
		if !newTables[t] {
			diff.RemovedTables = append(diff.RemovedTables, t)
		}
	}

	// Diff Routes (keyed by on_event)
	oldRoutes := make(map[string]bool)
	for _, r := range oldSchema.Routes {
		oldRoutes[r.OnEvent] = true
	}
	newRoutes := make(map[string]bool)
	for _, r := range newSchema.Routes {
		newRoutes[r.OnEvent] = true
		if !oldRoutes[r.OnEvent] {
			diff.AddedRoutes = append(diff.AddedRoutes, r.OnEvent)
		}
	}
	for _, r := range oldSchema.Routes {
		if !newRoutes[r.OnEvent] {
			diff.RemovedRoutes = append(diff.RemovedRoutes, r.OnEvent)
		}
	}

	// Diff Nodes
	oldNodes := make(map[string]bool)
	for _, n := range oldSchema.Nodes {
		oldNodes[n.Name] = true
	}
	newNodes := make(map[string]bool)
	for _, n := range newSchema.Nodes {
		newNodes[n.Name] = true
		if !oldNodes[n.Name] {
			diff.AddedNodes = append(diff.AddedNodes, n.Name)
		}
	}
	for _, n := range oldSchema.Nodes {
		if !newNodes[n.Name] {
			diff.RemovedNodes = append(diff.RemovedNodes, n.Name)
		}
	}

	// Diff Access Rules
	if len(oldSchema.Access) != len(newSchema.Access) {
		diff.AccessChanged = true
	} else {
		for i := range oldSchema.Access {
			if oldSchema.Access[i].Role != newSchema.Access[i].Role ||
				oldSchema.Access[i].Key != newSchema.Access[i].Key ||
				oldSchema.Access[i].Filter != newSchema.Access[i].Filter ||
				oldSchema.Access[i].ReadOnly != newSchema.Access[i].ReadOnly {
				diff.AccessChanged = true
				break
			}
		}
	}

	return diff
}
