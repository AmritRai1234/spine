package manifest

import (
	"fmt"
	"strings"
)

// NodeContext is the minimal manifest slice needed to safely work on a single
// node: its event contract plus every route connected to it, in both
// directions. It exists so tooling (human or AI) can edit one page without
// reading the rest of the codebase — the contract is the boundary.
type NodeContext struct {
	Node Node `json:"node"`

	// OutgoingRoutes are triggered by events this node emits.
	OutgoingRoutes []Route `json:"outgoing_routes,omitempty"`

	// IncomingRoutes emit states this node listens to (cross-page dependencies).
	IncomingRoutes []Route `json:"incoming_routes,omitempty"`

	// FailureRoutes handle on_failure states of this node's outgoing routes.
	FailureRoutes []Route `json:"failure_routes,omitempty"`
}

// BuildNodeContext extracts the context slice for nodeName from a parsed schema.
// Returns an error listing available node names when nodeName is not found.
func BuildNodeContext(schema *SpineSchema, nodeName string) (*NodeContext, error) {
	var node *Node
	for i := range schema.Nodes {
		if schema.Nodes[i].Name == nodeName {
			node = &schema.Nodes[i]
			break
		}
	}
	if node == nil {
		names := make([]string, 0, len(schema.Nodes))
		for _, n := range schema.Nodes {
			names = append(names, n.Name)
		}
		return nil, fmt.Errorf("node '%s' not found (available: %s)", nodeName, strings.Join(names, ", "))
	}

	emitted := make(map[string]bool, len(node.Emits))
	for _, e := range node.Emits {
		emitted[e.Event] = true
	}
	listened := make(map[string]bool, len(node.Listens))
	for _, l := range node.Listens {
		listened[l.State] = true
	}

	ctx := &NodeContext{Node: *node}

	failureEvents := make(map[string]bool)
	for _, r := range schema.Routes {
		if emitted[r.OnEvent] {
			ctx.OutgoingRoutes = append(ctx.OutgoingRoutes, r)
			if r.OnFailure != "" {
				failureEvents[r.OnFailure] = true
			}
		}
		if r.EmitState != "" && listened[r.EmitState] {
			ctx.IncomingRoutes = append(ctx.IncomingRoutes, r)
		}
	}
	for _, r := range schema.Routes {
		if failureEvents[r.OnEvent] {
			ctx.FailureRoutes = append(ctx.FailureRoutes, r)
		}
	}

	return ctx, nil
}
