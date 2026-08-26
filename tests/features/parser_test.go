package features

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

// --- Manifest schema version tiers ---
// spine_version is both a format contract and a capability tier: gated
// actions demand the declared version, and versions newer than this engine
// fail loudly at startup instead of best-effort parsing.

func TestSupportedManifestVersionAccepted(t *testing.T) {
	for _, v := range []string{"1", "2", "3"} {
		dir := t.TempDir()
		path := writeManifest(t, dir, "v.spine", "spine_version: "+v+"\n\ndatabase:\n  tables:\n    - items\n\nnodes:\n  N:\n    emits:\n      - event: EV\n        payload:\n          name: string\n\nroutes:\n  - on: EV\n    steps:\n      - action: db.insert\n        table: items\n")

		schema, err := manifest.ParseManifest(path)
		if err != nil {
			t.Fatalf("spine_version %s must parse, got error: %v", v, err)
		}
		want := 1
		if v == "2" {
			want = 2
		}
		if v == "3" {
			want = 3
		}
		if schema.SpineVersion != want {
			t.Errorf("expected SpineVersion %d, got %d", want, schema.SpineVersion)
		}
	}
}

func TestUnsupportedManifestVersionRejected(t *testing.T) {
	// Note: "0" is excluded — Sscanf reduces it to the zero value, which the
	// parser reports as a *missing* spine_version (covered by TestEmptyManifest).
	for _, v := range []string{"4", "99", "-1"} {
		dir := t.TempDir()
		path := writeManifest(t, dir, "future.spine", "spine_version: "+v+"\n")
		_, err := manifest.ParseManifest(path)
		if err == nil {
			t.Fatalf("spine_version %s must be rejected, got nil error", v)
		}
		if !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "versions 1 to 3") {
			t.Errorf("spine_version %s: wrong error message: %s", v, err.Error())
		}
	}
}

// stripe.checkout is tier 3 — legal in a v3 manifest, refused below it.
func TestTierThreeActionGating(t *testing.T) {
	checkoutRoute := `
nodes:
  N:
    emits:
      - event: PAY
        payload:
          order_id: string

routes:
  - on: PAY
    steps:
      - action: stripe.checkout
        order_id: $event.payload.order_id
        amount: 12.00
        success_url: "https://shop.example.com/#/orders"
        cancel_url: "https://shop.example.com/#/catalog"
`
	v3dir := t.TempDir()
	v3Path := writeManifest(t, v3dir, "v3.spine", "spine_version: 3\n"+checkoutRoute)
	if _, err := manifest.ParseManifest(v3Path); err != nil {
		t.Fatalf("v3 manifest using stripe.checkout must parse, got: %v", err)
	}

	v2dir := t.TempDir()
	v2Path := writeManifest(t, v2dir, "v2.spine", "spine_version: 2\n"+checkoutRoute)
	_, err := manifest.ParseManifest(v2Path)
	if err == nil {
		t.Fatal("v2 manifest using stripe.checkout must be rejected")
	}
	for _, want := range []string{"stripe.checkout", "spine_version: 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %s", want, err.Error())
		}
	}
}

func TestGatedActionRequiresNewerVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "gated.spine", `spine_version: 1

nodes:
  N:
    emits:
      - event: NOTIFY
        payload:
          email: string

routes:
  - on: NOTIFY
    steps:
      - action: email.send
        to: $event.payload.email
        subject: hi
        body: yo
`)

	_, err := manifest.ParseManifest(path)
	if err == nil {
		t.Fatal("v1 manifest using email.send must be rejected")
	}
	for _, want := range []string{"email.send", "spine_version: 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %s", want, err.Error())
		}
	}
}

func TestGatedActionUnlockedAtV2(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "unlocked.spine", `spine_version: 2

database:
  tables:
    - subscribers

nodes:
  N:
    emits:
      - event: CAMPAIGN
        payload:
          subject: string
          body: string

routes:
  - on: CAMPAIGN
    steps:
      - action: email.broadcast
        table: subscribers
        where: "unsubscribed = 0"
        subject: $event.payload.subject
        body: $event.payload.body
`)

	if _, err := manifest.ParseManifest(path); err != nil {
		t.Fatalf("v2 manifest using email.broadcast must parse, got: %v", err)
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

// --- Test 15: Root routes may reference events declared in included files ---

func TestRootRouteReferencingIncludedEvent(t *testing.T) {
	dir := t.TempDir()

	eventsContent := `spine_version: 1

nodes:
  SharedNode:
    emits:
      - event: SHARED_EVENT
        payload:
          email: string
`
	writeManifest(t, dir, "events.spine", eventsContent)

	// The root file routes on an event declared ONLY in the included file.
	// Previously validation ran before the include merge, so this failed
	// with a misleading "route references event ... (possible typo?)" error.
	mainContent := `spine_version: 1

includes:
  - events.spine

database:
  tables:
    - logs

routes:
  - on: SHARED_EVENT
    steps:
      - action: db.insert
        table: logs
`
	mainPath := writeManifest(t, dir, "main.spine", mainContent)

	schema, err := manifest.ParseManifest(mainPath)
	if err != nil {
		t.Fatalf("root route referencing an included event must parse, got: %v", err)
	}
	if len(schema.Nodes) != 1 || len(schema.Routes) != 1 {
		t.Errorf("expected 1 merged node and 1 route, got %d nodes / %d routes", len(schema.Nodes), len(schema.Routes))
	}
}

// --- Test 16: Node names must be unique across includes ---

func TestDuplicateNodeNameAcrossIncludes(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "a.spine", `spine_version: 1
nodes:
  DupNode:
    emits:
      - event: EVENT_A
`)
	writeManifest(t, dir, "b.spine", `spine_version: 1
nodes:
  DupNode:
    emits:
      - event: EVENT_B
`)

	mainPath := writeManifest(t, dir, "main.spine", `spine_version: 1
includes:
  - a.spine
  - b.spine
routes:
  - on: EVENT_A
    steps:
      - action: log.write
`)
	_, err := manifest.ParseManifest(mainPath)
	if err == nil {
		t.Fatal("duplicate node name across includes must fail, got nil error")
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Errorf("expected duplicate-node error, got: %v", err)
	}
}

// --- Test 17: Fail-closed boolean parsing (read_only / parallel) ---

func TestBooleanFlagParsing(t *testing.T) {
	dir := t.TempDir()

	// "True" (capitalized) must parse as TRUE — previously exact-match
	// `v == "true"` silently turned it into false (read-only role became
	// writable with no warning).
	upperPath := writeManifest(t, dir, "upper.spine", `spine_version: 1
database:
  tables:
    - data
access:
  - role: admin
    key: sk_admin
    read_only: True
routes:
  - on: ANY_EVENT
    steps:
      - action: db.insert
        table: data
`)
	schema, err := manifest.ParseManifest(upperPath)
	if err != nil {
		t.Fatalf("'True' must be accepted as true, got: %v", err)
	}
	if len(schema.Access) != 1 || !schema.Access[0].ReadOnly {
		t.Errorf("read_only: True must parse to true, got %+v", schema.Access)
	}

	// Non-boolean values must fail loudly, not silently default to false.
	badPath := writeManifest(t, dir, "bad.spine", `spine_version: 1
database:
  tables:
    - data
access:
  - role: admin
    key: sk_admin
    read_only: yes
routes:
  - on: ANY_EVENT
    steps:
      - action: db.insert
        table: data
`)
	_, err = manifest.ParseManifest(badPath)
	if err == nil {
		t.Fatal("read_only: yes must be a parse error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid boolean value") {
		t.Errorf("expected invalid-boolean error, got: %v", err)
	}

	// Same fail-closed behavior for route parallel.
	parPath := writeManifest(t, dir, "par.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: EVT
routes:
  - on: EVT
    parallel: True
    steps:
      - action: log.write
`)
	schema, err = manifest.ParseManifest(parPath)
	if err != nil {
		t.Fatalf("parallel: True must be accepted, got: %v", err)
	}
	if len(schema.Routes) != 1 || !schema.Routes[0].Parallel {
		t.Errorf("parallel: True must parse to true, got %+v", schema.Routes)
	}

	// "1" is a valid strconv.ParseBool literal (true) — fail-closed, not an
	// error. A genuinely non-boolean value ("yes") must be a parse error.
	parOnePath := writeManifest(t, dir, "parone.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: EVT
routes:
  - on: EVT
    parallel: 1
    steps:
      - action: log.write
`)
	schema, err = manifest.ParseManifest(parOnePath)
	if err != nil {
		t.Fatalf("parallel: 1 must be accepted as true, got: %v", err)
	}
	if len(schema.Routes) != 1 || !schema.Routes[0].Parallel {
		t.Errorf("parallel: 1 must parse to true, got %+v", schema.Routes)
	}

	parBadPath := writeManifest(t, dir, "parbad.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: EVT
routes:
  - on: EVT
    parallel: yes
    steps:
      - action: log.write
`)
	_, err = manifest.ParseManifest(parBadPath)
	if err == nil {
		t.Fatal("parallel: yes must be a parse error, got nil")
	}
}

// --- Test 18: Duplicate event declarations must agree on payload shape ---

func TestDuplicateEventShapeConflict(t *testing.T) {
	dir := t.TempDir()

	// Identical re-declarations (two nodes emitting the same event with the
	// same shape) are legitimate and must parse.
	okPath := writeManifest(t, dir, "ok.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  A:
    emits:
      - event: SHARED
        payload:
          email: string
  B:
    emits:
      - event: SHARED
        payload:
          email: string
routes:
  - on: SHARED
    steps:
      - action: log.write
`)
	if _, err := manifest.ParseManifest(okPath); err != nil {
		t.Fatalf("identical duplicate event declarations must parse, got: %v", err)
	}

	// Conflicting shapes must fail: the registry (last-wins) and codegen
	// (most-fields) would otherwise generate divergent contracts.
	conflictPath := writeManifest(t, dir, "conflict.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  A:
    emits:
      - event: SHARED
        payload:
          email: string
  B:
    emits:
      - event: SHARED
        payload:
          email: number
routes:
  - on: SHARED
    steps:
      - action: log.write
`)
	_, err := manifest.ParseManifest(conflictPath)
	if err == nil {
		t.Fatal("conflicting duplicate event shapes must fail, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting payload shapes") {
		t.Errorf("expected conflicting-shapes error, got: %v", err)
	}
}

// --- Test 19: Inline comments must not corrupt declared field types ---

func TestInlineCommentDoesNotCorruptFieldType(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "comment.spine", `spine_version: 1
database:
  tables:
    - data
nodes:
  N:
    emits:
      - event: WITH_COMMENT
        payload:
          email: string # primary contact
          count: number
routes:
  - on: WITH_COMMENT
    steps:
      - action: db.insert
        table: data
`)
	schema, err := manifest.ParseManifest(path)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// The comment must be stripped: the type must be exactly "string", not
	// "string # primary contact" (which silently disabled type checks).
	emailType := ""
	for _, e := range schema.Nodes[0].Emits {
		if e.Event == "WITH_COMMENT" {
			for _, f := range e.Fields {
				if f.Name == "email" {
					emailType = f.FieldType
				}
			}
		}
	}
	if emailType != "string" {
		t.Errorf("expected field type 'string', got %q (comment corrupted the type)", emailType)
	}
}
