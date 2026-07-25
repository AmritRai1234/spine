package spine

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Registry holds the parsed schema indexed for fast event→route lookups
// and payload validation. It is safe for concurrent reads.
type Registry struct {
	mu         sync.RWMutex
	schema     *SpineSchema
	nodes      map[string]*Node
	routes     map[string][]*Route
	eventEmits map[string][]PayloadField
}

// NewRegistry builds a Registry from a parsed schema.
func NewRegistry(schema *SpineSchema) *Registry {
	reg := &Registry{
		schema:     schema,
		nodes:      make(map[string]*Node),
		routes:     make(map[string][]*Route),
		eventEmits: make(map[string][]PayloadField),
	}

	for i := range schema.Nodes {
		node := &schema.Nodes[i]
		reg.nodes[node.Name] = node
		for _, e := range node.Emits {
			if len(e.Fields) > 0 {
				reg.eventEmits[e.Event] = e.Fields
			}
		}
	}

	for i := range schema.Routes {
		r := &schema.Routes[i]
		reg.routes[r.OnEvent] = append(reg.routes[r.OnEvent], r)
	}

	return reg
}

// ValidatePayload checks a payload against the schema-defined types for an event.
// Returns nil if valid or no schema is defined for the event.
func (r *Registry) ValidatePayload(event string, payload map[string]interface{}) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	expectedFields, ok := r.eventEmits[event]
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

// GetRoutes returns routes matching an event name.
func (r *Registry) GetRoutes(event string) ([]*Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	routes, ok := r.routes[event]
	return routes, ok
}

// GetSchema returns the underlying schema.
func (r *Registry) GetSchema() *SpineSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.schema
}

// GetNode returns a node by name.
func (r *Registry) GetNode(name string) (*Node, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[name]
	return n, ok
}
