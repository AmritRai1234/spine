package manifest

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"
)

// registryData holds the immutable snapshot — swapped atomically.
type registryData struct {
	schema     *SpineSchema
	nodes      map[string]*Node
	routes     map[string][]*Route
	eventEmits map[string][]PayloadField
}

// Registry holds the parsed schema indexed for fast event→route lookups.
// Uses atomic pointer swap for lock-free reads on the hot path.
type Registry struct {
	data unsafe.Pointer // *registryData
}

// NewRegistry builds a Registry from a parsed schema.
func NewRegistry(schema *SpineSchema) *Registry {
	d := &registryData{
		schema:     schema,
		nodes:      make(map[string]*Node),
		routes:     make(map[string][]*Route),
		eventEmits: make(map[string][]PayloadField),
	}

	for i := range schema.Nodes {
		node := &schema.Nodes[i]
		d.nodes[node.Name] = node
		for _, e := range node.Emits {
			if len(e.Fields) > 0 {
				d.eventEmits[e.Event] = e.Fields
			}
		}
	}

	for i := range schema.Routes {
		r := &schema.Routes[i]
		d.routes[r.OnEvent] = append(d.routes[r.OnEvent], r)
	}

	reg := &Registry{}
	atomic.StorePointer(&reg.data, unsafe.Pointer(d))
	return reg
}

func (r *Registry) load() *registryData {
	return (*registryData)(atomic.LoadPointer(&r.data))
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
