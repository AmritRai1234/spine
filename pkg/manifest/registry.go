package manifest

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// registryData holds the immutable snapshot — swapped atomically.
type registryData struct {
	schema     *SpineSchema
	nodes      map[string]*Node
	routes     map[string][]*Route
	eventEmits map[string][]PayloadField
	fieldTypes map[string]map[string]string // pre-computed: event -> field -> type
}

// Registry holds the parsed schema indexed for fast event→route lookups.
// Uses atomic pointer swap for lock-free reads on the hot path.
type Registry struct {
	data atomic.Pointer[registryData] // *registryData
}

// NewRegistry builds a Registry from a parsed schema.
func NewRegistry(schema *SpineSchema) *Registry {
	d := &registryData{
		schema:     schema,
		nodes:      make(map[string]*Node),
		routes:     make(map[string][]*Route),
		eventEmits: make(map[string][]PayloadField),
		fieldTypes: make(map[string]map[string]string),
	}

	for i := range schema.Nodes {
		node := &schema.Nodes[i]
		d.nodes[node.Name] = node
		for _, e := range node.Emits {
			if len(e.Fields) > 0 {
				d.eventEmits[e.Event] = e.Fields
				// Pre-compute field types map for this event
				ft := make(map[string]string, len(e.Fields))
				for _, f := range e.Fields {
					ft[f.Name] = f.FieldType
				}
				d.fieldTypes[e.Event] = ft
			}
		}
	}

	for i := range schema.Routes {
		r := &schema.Routes[i]
		d.routes[r.OnEvent] = append(d.routes[r.OnEvent], r)
	}

	reg := &Registry{}
	reg.data.Store(d)
	return reg
}

func (r *Registry) load() *registryData {
	return r.data.Load()
}

// ValidatePayload checks a payload against the schema-defined types for an event.
func (r *Registry) ValidatePayload(event string, payload map[string]interface{}) error {
	d := r.load()

	expectedFields, ok := d.eventEmits[event]
	if !ok {
		return nil
	}

	for _, field := range expectedFields {
		val, exists := payload[field.Name]
		if !exists || val == nil {
			return fmt.Errorf("missing required field '%s' (expected type %s)", field.Name, field.FieldType)
		}

		t := strings.ToLower(field.FieldType)
		switch t {
		case "string", "str", "text":
			if _, ok := val.(string); !ok {
				return fmt.Errorf("field '%s' must be a string (got %T)", field.Name, val)
			}
		case "number", "float", "int", "integer":
			switch v := val.(type) {
			case float64, float32, int, int64, int32:
				// valid
			case string:
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					return fmt.Errorf("field '%s' must be a number (got invalid string '%s')", field.Name, v)
				}
			default:
				return fmt.Errorf("field '%s' must be a number (got %T)", field.Name, val)
			}
		case "bool", "boolean":
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("field '%s' must be a boolean (got %T)", field.Name, val)
			}
		}
	}

	return nil
}

// GetRoutes returns routes matching an event name. Lock-free.
func (r *Registry) GetRoutes(event string) ([]*Route, bool) {
	d := r.load()
	routes, ok := d.routes[event]
	return routes, ok
}

// GetSchema returns the underlying schema. Lock-free.
func (r *Registry) GetSchema() *SpineSchema {
	return r.load().schema
}

// GetNode returns a node by name. Lock-free.
func (r *Registry) GetNode(name string) (*Node, bool) {
	d := r.load()
	n, ok := d.nodes[name]
	return n, ok
}

// GetFieldTypes returns a map of field name → declared type for an event.
// Returns nil if the event has no declared fields. Lock-free.
// The returned map is pre-computed and immutable — zero allocations per call.
func (r *Registry) GetFieldTypes(event string) map[string]string {
	d := r.load()
	return d.fieldTypes[event]
}
