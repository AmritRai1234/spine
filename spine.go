package spine

import (
	"github.com/AmritRai1234/spine/pkg/engine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Version represents the current release version of Spine.
const Version = "2.1.0"

// Type aliases for top-level engine & manifest components.
type Engine = engine.Engine
type Bus = engine.Bus
type Hub = engine.Hub
type SpineSchema = manifest.SpineSchema
type RouteStep = manifest.RouteStep
type Route = manifest.Route
type Node = manifest.Node

// New creates a fully wired Engine from a parsed schema.
func New(schema *manifest.SpineSchema, dbPath string) (*engine.Engine, error) {
	return engine.New(schema, dbPath)
}

// NewFromFile parses a manifest and creates an Engine.
func NewFromFile(spineFile, dbPath string) (*engine.Engine, error) {
	return engine.NewFromFile(spineFile, dbPath)
}

// ParseManifest parses a .spine manifest file into an AST schema.
func ParseManifest(path string) (*manifest.SpineSchema, error) {
	return manifest.ParseManifest(path)
}
