package manifest

import (
	"os"
	"testing"
)

// List-form nodes ("- name: X") must parse identically to map-form ("X:"):
// node registered, payload fields attached, duplicate detection active, and
// route→emits contract validation functional. Regression: list-form nodes
// used to be silently ignored entirely, which zeroed schema.Nodes and
// disabled ALL contract validation (typo'd route events passed silently).

func TestListNodeFormParsesContracts(t *testing.T) {
	m := `spine_version: 3

nodes:
  - name: Booking
    emits:
      - event: BOOK_SLOT
        payload:
          slot_id: string
          capacity: integer
    listens:
      - state: BOOKING_CONFIRMED
        payload:
          status: string

routes:
  - on: BOOK_SLOT
    steps:
      - action: db.insert
        table: slots
`
	if err := os.WriteFile("/tmp/listnode.spine", []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseManifest("/tmp/listnode.spine")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(s.Nodes))
	}
	n := s.Nodes[0]
	if n.Name != "Booking" {
		t.Fatalf("node name = %q", n.Name)
	}
	if len(n.Emits) != 1 || n.Emits[0].Event != "BOOK_SLOT" {
		t.Fatalf("emits not parsed: %+v", n.Emits)
	}
	if len(n.Emits[0].Fields) != 2 {
		t.Fatalf("emit payload fields not parsed: %+v", n.Emits[0].Fields)
	}
	if n.Emits[0].Fields[1].FieldType != "integer" {
		t.Fatalf("payload type not attached: %+v", n.Emits[0].Fields)
	}
	if len(n.Listens) != 1 || len(n.Listens[0].Fields) != 1 {
		t.Fatalf("listens not parsed: %+v", n.Listens)
	}
}

// The headline regression: with list-form nodes parsed, a route referencing
// an undeclared event must now FAIL (it previously passed silently because
// schema.Nodes was empty and validation was skipped).
func TestListNodeFormEnablesRouteValidation(t *testing.T) {
	m := `spine_version: 3

nodes:
  - name: Booking
    emits:
      - event: BOOK_SLOT

routes:
  - on: TYPO_EVENT
    steps:
      - action: db.insert
        table: slots
`
	if err := os.WriteFile("/tmp/listnode_typo.spine", []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest("/tmp/listnode_typo.spine"); err == nil {
		t.Fatal("typo'd route event must fail validation now that list-form nodes are parsed")
	}
}

// Map-form still works and list→map→list transitions keep the shift correct.
func TestMixedNodeForms(t *testing.T) {
	m := `spine_version: 3

nodes:
  - name: First
    emits:
      - event: EVT_ONE
        payload:
          a: integer
  MapNode:
    emits:
      - event: EVT_TWO
        payload:
          b: string
  - name: Third
    emits:
      - event: EVT_THREE

routes:
  - on: EVT_ONE
    steps:
      - action: db.insert
        table: t
`
	if err := os.WriteFile("/tmp/mixednode.spine", []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseManifest("/tmp/mixednode.spine")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(s.Nodes))
	}
	if s.Nodes[0].Name != "First" || s.Nodes[1].Name != "MapNode" || s.Nodes[2].Name != "Third" {
		t.Fatalf("node order/names wrong: %+v", s.Nodes)
	}
	if len(s.Nodes[2].Emits) != 1 || s.Nodes[2].Emits[0].Event != "EVT_THREE" {
		t.Fatalf("third node emits lost: %+v", s.Nodes[2].Emits)
	}
}

func TestDuplicateListNodeNamesRejected(t *testing.T) {
	m := `spine_version: 3

nodes:
  - name: A
    emits:
      - event: E1
  - name: A
    emits:
      - event: E2

routes:
  - on: E1
    steps:
      - action: db.insert
        table: t
`
	if err := os.WriteFile("/tmp/dupe.spine", []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest("/tmp/dupe.spine"); err == nil {
		t.Fatal("duplicate node name must be rejected in list form too")
	}
}
