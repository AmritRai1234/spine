package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// writeManifest writes a manifest string to a temp file and returns the path.
func writeManifest(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
	return path
}

// --- Test 1: Full round-trip parse of a valid manifest ---

func TestParseValidManifest(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

database:
  tables:
    - users
    - orders

nodes:
  LoginPage:
    owns_files:
      - frontend/Login.tsx
    emits:
      - event: USER_LOGIN
        payload:
          email: string
          password: string
    listens:
      - state: AUTH_STATUS
        payload:
          status: string

  Dashboard:
    emits:
      - event: CREATE_ORDER
        payload:
          item: string
          quantity: number

routes:
  - on: USER_LOGIN
    steps:
      - action: db.insert
        table: users
    emit: AUTH_STATUS

  - on: CREATE_ORDER
    parallel: true
    steps:
      - action: db.insert
        table: orders
      - action: log.write
        message: "Order created for $event.payload.item"
    emit: ORDER_CREATED
`
	path := writeManifest(t, dir, "app.spine", content)

	schema, err := manifest.ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if schema.SpineVersion != 1 {
		t.Errorf("expected spine_version 1, got %d", schema.SpineVersion)
	}
	if len(schema.DbTables) != 2 {
		t.Errorf("expected 2 tables, got %d", len(schema.DbTables))
	}
	if len(schema.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(schema.Nodes))
	}
	if len(schema.Routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(schema.Routes))
	}

	// Verify node details
	if schema.Nodes[0].Name != "LoginPage" {
		t.Errorf("expected first node 'LoginPage', got '%s'", schema.Nodes[0].Name)
	}
	if len(schema.Nodes[0].OwnsFiles) != 1 {
		t.Errorf("expected 1 owns_file, got %d", len(schema.Nodes[0].OwnsFiles))
	}
	if len(schema.Nodes[0].Emits) != 1 {
		t.Errorf("expected 1 emit, got %d", len(schema.Nodes[0].Emits))
	}
	if len(schema.Nodes[0].Emits[0].Fields) != 2 {
		t.Errorf("expected 2 payload fields on USER_LOGIN, got %d", len(schema.Nodes[0].Emits[0].Fields))
	}
	if len(schema.Nodes[0].Listens) != 1 {
		t.Errorf("expected 1 listen, got %d", len(schema.Nodes[0].Listens))
	}

	// Verify route details
	if schema.Routes[0].OnEvent != "USER_LOGIN" {
		t.Errorf("expected route on 'USER_LOGIN', got '%s'", schema.Routes[0].OnEvent)
	}
	if schema.Routes[0].EmitState != "AUTH_STATUS" {
		t.Errorf("expected emit state 'AUTH_STATUS', got '%s'", schema.Routes[0].EmitState)
	}
	if schema.Routes[1].Parallel != true {
		t.Errorf("expected second route to be parallel")
	}
}

// --- Test 2: Error messages include line numbers ---

func TestParseErrorLineNumbers(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

nodes:
  MyNode:
    emits:
      - event: TEST
        payload:
          name: string

  MyNode:
    emits:
      - event: TEST2
        payload:
          id: string

database:
  tables:
    - test_table

routes:
  - on: TEST
    steps:
      - action: db.insert
        table: test_table
`
	path := writeManifest(t, dir, "err.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected an error for duplicate node, got nil")
	}

	errStr := err.Error()
	// Error should reference file name
	if !strings.Contains(errStr, "err.spine") {
		t.Errorf("error should contain filename, got: %s", errStr)
	}
	// Error should contain a line number
	if !strings.Contains(errStr, ":10:") && !strings.Contains(errStr, "line") {
		t.Errorf("error should contain line number context, got: %s", errStr)
	}
	// Error should mention duplicate
	if !strings.Contains(errStr, "duplicate") {
		t.Errorf("error should mention 'duplicate', got: %s", errStr)
	}
}

// --- Test 3: Duplicate node names produce error ---

func TestDuplicateNodeNames(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

database:
  tables:
    - data

nodes:
  Widget:
    emits:
      - event: WIDGET_CLICK
        payload:
          id: string

  Widget:
    emits:
      - event: WIDGET_SUBMIT
        payload:
          id: string

routes:
  - on: WIDGET_CLICK
    steps:
      - action: db.insert
        table: data
`
	path := writeManifest(t, dir, "dup.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for duplicate node name 'Widget', got nil")
	}
	if !strings.Contains(err.Error(), "duplicate node name 'Widget'") {
		t.Errorf("expected duplicate node error, got: %s", err.Error())
	}
	// Should reference both line numbers
	if !strings.Contains(err.Error(), "first declared at line") {
		t.Errorf("expected line number reference, got: %s", err.Error())
	}
}

// --- Test 4: Circular includes produce error ---

func TestCircularIncludes(t *testing.T) {
	dir := t.TempDir()

	aContent := `spine_version: 1
includes:
  - b.spine

database:
  tables:
    - a_table

nodes:
  NodeA:
    emits:
      - event: EVENT_A
        payload:
          id: string

routes:
  - on: EVENT_A
    steps:
      - action: db.insert
        table: a_table
`

	bContent := `spine_version: 1
includes:
  - a.spine

database:
  tables:
    - b_table

nodes:
  NodeB:
    emits:
      - event: EVENT_B
        payload:
          id: string

routes:
  - on: EVENT_B
    steps:
      - action: db.insert
        table: b_table
`

	writeManifest(t, dir, "a.spine", aContent)
	writeManifest(t, dir, "b.spine", bContent)

	_, err := manifest.ParseManifest(filepath.Join(dir, "a.spine"))
	if err == nil {
		t.Fatal("expected circular include error, got nil")
	}
	if !strings.Contains(err.Error(), "circular include") {
		t.Errorf("expected 'circular include' error, got: %s", err.Error())
	}
}

// --- Test 5: Tab indentation parses correctly ---

func TestTabIndentation(t *testing.T) {
	dir := t.TempDir()
	// Using tabs instead of spaces (each \t = 2 spaces = 1 indent level)
	content := "spine_version: 1\n\ndatabase:\n\ttables:\n\t\t- tab_users\n\nnodes:\n\tTabNode:\n\t\temits:\n\t\t\t- event: TAB_EVENT\n\t\t\t\tpayload:\n\t\t\t\t\tname: string\n\nroutes:\n\t- on: TAB_EVENT\n\t\tsteps:\n\t\t\t- action: db.insert\n\t\t\t\ttable: tab_users\n"
	path := writeManifest(t, dir, "tabs.spine", content)

	schema, err := manifest.ParseManifest(path)
	if err != nil {
		t.Fatalf("tab-indented manifest should parse, got: %v", err)
	}

	if len(schema.DbTables) != 1 || schema.DbTables[0] != "tab_users" {
		t.Errorf("expected table 'tab_users', got %v", schema.DbTables)
	}
	if len(schema.Nodes) != 1 || schema.Nodes[0].Name != "TabNode" {
		t.Errorf("expected node 'TabNode', got %v", schema.Nodes)
	}
	if len(schema.Routes) != 1 {
		t.Errorf("expected 1 route, got %d", len(schema.Routes))
	}
	if len(schema.Nodes[0].Emits) != 1 || len(schema.Nodes[0].Emits[0].Fields) != 1 {
		t.Errorf("expected 1 emit with 1 field, got emits=%v", schema.Nodes[0].Emits)
	}
}

// --- Test 6: Mixed tabs and spaces produce error ---

func TestMixedWhitespace(t *testing.T) {
	dir := t.TempDir()
	// Line with both tab and spaces as leading whitespace
	content := "spine_version: 1\n\ndatabase:\n \ttables:\n    - users\n"
	path := writeManifest(t, dir, "mixed.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for mixed tabs and spaces, got nil")
	}
	if !strings.Contains(err.Error(), "mixed tabs and spaces") {
		t.Errorf("expected mixed whitespace error, got: %s", err.Error())
	}
}

// --- Test 7: Empty manifest produces error ---

func TestEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	content := `# This is just a comment, nothing else
`
	path := writeManifest(t, dir, "empty.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for missing spine_version, got nil")
	}
	if !strings.Contains(err.Error(), "spine_version") {
		t.Errorf("expected missing spine_version error, got: %s", err.Error())
	}
}

// --- Test 8: Route with no steps produces error ---

func TestRouteWithNoSteps(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

nodes:
  TestNode:
    emits:
      - event: EMPTY_ROUTE
        payload:
          id: string

routes:
  - on: EMPTY_ROUTE
    emit: SOME_STATE
`
	path := writeManifest(t, dir, "nosteps.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for route with no steps, got nil")
	}
	if !strings.Contains(err.Error(), "no steps") {
		t.Errorf("expected 'no steps' error, got: %s", err.Error())
	}
}

// --- Test 9: Unknown top-level key produces error ---

func TestUnknownTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1
routs:
  - on: TYPO_EVENT
`
	path := writeManifest(t, dir, "typo.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for unknown key 'routs', got nil")
	}
	if !strings.Contains(err.Error(), "unknown top-level key") {
		t.Errorf("expected unknown key error, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "routs") {
		t.Errorf("error should mention the bad key 'routs', got: %s", err.Error())
	}
}

// --- Test 10: Missing spine_version ---

func TestMissingSpineVersion(t *testing.T) {
	dir := t.TempDir()
	content := `database:
  tables:
    - users

nodes:
  Node1:
    emits:
      - event: E1
        payload:
          id: string

routes:
  - on: E1
    steps:
      - action: db.insert
        table: users
`
	path := writeManifest(t, dir, "noversion.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for missing spine_version, got nil")
	}
	if !strings.Contains(err.Error(), "spine_version") {
		t.Errorf("expected spine_version error, got: %s", err.Error())
	}
}

// --- Test 11: Duplicate table deduplication ---

func TestDuplicateTableDedup(t *testing.T) {
	dir := t.TempDir()

	childContent := `spine_version: 1

database:
  tables:
    - shared_table
    - child_only

nodes:
  ChildNode:
    emits:
      - event: CHILD_EVENT
        payload:
          id: string

routes:
  - on: CHILD_EVENT
    steps:
      - action: db.insert
        table: child_only
`
	writeManifest(t, dir, "child.spine", childContent)

	parentContent := `spine_version: 1

includes:
  - child.spine

database:
  tables:
    - shared_table
    - parent_only

nodes:
  ParentNode:
    emits:
      - event: PARENT_EVENT
        payload:
          id: string

routes:
  - on: PARENT_EVENT
    steps:
      - action: db.insert
        table: parent_only
`
	parentPath := writeManifest(t, dir, "parent.spine", parentContent)

	schema, err := manifest.ParseManifest(parentPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	// shared_table should appear only once
	count := 0
	for _, tbl := range schema.DbTables {
		if tbl == "shared_table" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'shared_table' to appear once (deduplicated), appeared %d times in %v", count, schema.DbTables)
	}

	// Both unique tables should be present
	tableSet := make(map[string]bool)
	for _, tbl := range schema.DbTables {
		tableSet[tbl] = true
	}
	if !tableSet["parent_only"] {
		t.Error("expected 'parent_only' table to be present")
	}
	if !tableSet["child_only"] {
		t.Error("expected 'child_only' table to be present")
	}
}

// --- Test 12: Deep payload fields ---

func TestDeepPayloadFields(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

database:
  tables:
    - records

nodes:
  FormNode:
    emits:
      - event: SUBMIT_FORM
        payload:
          first_name: string
          last_name: string
          age: number
          email: string
          is_active: boolean

routes:
  - on: SUBMIT_FORM
    steps:
      - action: db.insert
        table: records
`
	path := writeManifest(t, dir, "deep.spine", content)

	schema, err := manifest.ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if len(schema.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(schema.Nodes))
	}
	if len(schema.Nodes[0].Emits) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(schema.Nodes[0].Emits))
	}

	fields := schema.Nodes[0].Emits[0].Fields
	if len(fields) != 5 {
		t.Fatalf("expected 5 payload fields, got %d", len(fields))
	}

	expectedFields := map[string]string{
		"first_name": "string",
		"last_name":  "string",
		"age":        "number",
		"email":      "string",
		"is_active":  "boolean",
	}

	for _, f := range fields {
		expected, ok := expectedFields[f.Name]
		if !ok {
			t.Errorf("unexpected field '%s'", f.Name)
			continue
		}
		if f.FieldType != expected {
			t.Errorf("field '%s' expected type '%s', got '%s'", f.Name, expected, f.FieldType)
		}
	}
}

// --- Test 13: Multiple includes merge correctly ---

func TestMultipleIncludes(t *testing.T) {
	dir := t.TempDir()

	authContent := `spine_version: 1

database:
  tables:
    - auth_tokens

nodes:
  AuthNode:
    emits:
      - event: AUTH_LOGIN
        payload:
          token: string

routes:
  - on: AUTH_LOGIN
    steps:
      - action: db.insert
        table: auth_tokens
`

	billingContent := `spine_version: 1

database:
  tables:
    - invoices

nodes:
  BillingNode:
    emits:
      - event: CREATE_INVOICE
        payload:
          amount: number

routes:
  - on: CREATE_INVOICE
    steps:
      - action: db.insert
        table: invoices
`

	writeManifest(t, dir, "auth.spine", authContent)
	writeManifest(t, dir, "billing.spine", billingContent)

	mainContent := `spine_version: 1

includes:
  - auth.spine
  - billing.spine

database:
  tables:
    - main_data

nodes:
  MainNode:
    emits:
      - event: MAIN_EVENT
        payload:
          key: string

routes:
  - on: MAIN_EVENT
    steps:
      - action: db.insert
        table: main_data
`
	mainPath := writeManifest(t, dir, "main.spine", mainContent)

	schema, err := manifest.ParseManifest(mainPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	// 3 nodes total: MainNode + AuthNode + BillingNode
	if len(schema.Nodes) != 3 {
		t.Errorf("expected 3 nodes (merged from 3 files), got %d", len(schema.Nodes))
	}

	// 3 routes total
	if len(schema.Routes) != 3 {
		t.Errorf("expected 3 routes (merged from 3 files), got %d", len(schema.Routes))
	}

	// 3 unique tables
	if len(schema.DbTables) != 3 {
		t.Errorf("expected 3 tables, got %d: %v", len(schema.DbTables), schema.DbTables)
	}
}

// --- Test 14: Route referencing unknown event produces error ---

func TestRouteUnknownEventError(t *testing.T) {
	dir := t.TempDir()
	content := `spine_version: 1

database:
  tables:
    - data

nodes:
  MyNode:
    emits:
      - event: REAL_EVENT
        payload:
          id: string

routes:
  - on: REAL_EVENT
    steps:
      - action: db.insert
        table: data

  - on: TYPO_EVNT
    steps:
      - action: log.write
        message: "This event doesn't exist"
`
	path := writeManifest(t, dir, "unknown_event.spine", content)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("expected error for route referencing unknown event 'TYPO_EVNT', got nil")
	}
	if !strings.Contains(err.Error(), "TYPO_EVNT") {
		t.Errorf("error should mention the unknown event 'TYPO_EVNT', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "not declared") {
		t.Errorf("error should say event is not declared, got: %s", err.Error())
	}
}
