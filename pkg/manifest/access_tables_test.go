package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Per-table read scoping (access.roles[].tables) — parse tests. The security
// enforcement lives in the engine; here we pin the grammar and the
// startup-time fail-loud validation.

func parseAccessTables(t *testing.T, manifest string) (*SpineSchema, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	return ParseManifest(path)
}

const tablesScopeManifest = `spine_version: 1
database:
  tables:
    - orders
    - cart_items
    - secrets

access:
  - role: admin
    key: "admin-key"

  - role: shopper
    key: "shopper-key"
    tables:
      - orders: "email = 'x@example.com'"
      - cart_items: "cart_id = 'c_123'"

nodes:
  - name: n
    emits:
      - event: EVT
`

func TestAccessTablesParsing(t *testing.T) {
	schema, err := parseAccessTables(t, tablesScopeManifest)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var shopper *AccessRule
	for i := range schema.Access {
		if schema.Access[i].Role == "shopper" {
			shopper = &schema.Access[i]
		}
	}
	if shopper == nil {
		t.Fatal("shopper role not parsed")
	}
	if shopper.Tables == nil {
		t.Fatal("shopper Tables map must be non-nil (deny-by-default marker)")
	}
	if got := shopper.Tables["orders"]; got != "email = 'x@example.com'" {
		t.Errorf("orders scope = %q", got)
	}
	if got := shopper.Tables["cart_items"]; got != "cart_id = 'c_123'" {
		t.Errorf("cart_items scope = %q", got)
	}
	if len(shopper.Tables) != 2 {
		t.Errorf("expected exactly 2 scoped tables, got %d", len(shopper.Tables))
	}

	// Admin (no tables: key) keeps nil = full read.
	for i := range schema.Access {
		if schema.Access[i].Role == "admin" && schema.Access[i].Tables != nil {
			t.Error("admin without tables: must keep nil Tables (full read)")
		}
	}
}

func TestAccessTablesEmptyFilterMeansWholeTable(t *testing.T) {
	m := strings.Replace(tablesScopeManifest, "cart_id = 'c_123'", "", 1)
	schema, err := parseAccessTables(t, m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i := range schema.Access {
		if schema.Access[i].Role == "shopper" {
			if got, ok := schema.Access[i].Tables["cart_items"]; !ok || got != "" {
				t.Errorf("empty filter must parse as allowed-with-no-filter, got %q ok=%v", got, ok)
			}
		}
	}
}

func TestAccessTablesUnknownTableFailsStartup(t *testing.T) {
	m := strings.Replace(tablesScopeManifest, "- orders: \"email = 'x@example.com'\"", "- orderz: \"email = 'x'\"", 1)
	_, err := parseAccessTables(t, m)
	if err == nil {
		t.Fatal("unknown table in tables: scope must fail startup")
	}
	if !strings.Contains(err.Error(), "unknown table") {
		t.Errorf("error must name the problem, got: %v", err)
	}
}

func TestAccessTablesMalformedFilterFailsStartup(t *testing.T) {
	// Compound condition — would bind garbage as one parameter.
	m := strings.Replace(tablesScopeManifest,
		"- orders: \"email = 'x@example.com'\"",
		"- orders: \"email = 'x' AND cart_id = 'y'\"", 1)
	_, err := parseAccessTables(t, m)
	if err == nil {
		t.Fatal("compound filter must fail startup")
	}
	if !strings.Contains(err.Error(), "invalid filter") {
		t.Errorf("error must name the invalid filter, got: %v", err)
	}
}

func TestAccessTablesNoOperatorFailsStartup(t *testing.T) {
	m := strings.Replace(tablesScopeManifest,
		"- orders: \"email = 'x@example.com'\"",
		"- orders: \"just_a_column\"", 1)
	_, err := parseAccessTables(t, m)
	if err == nil {
		t.Fatal("filter without operator must fail startup")
	}
}

func TestAccessTablesDuplicateEntryFailsStartup(t *testing.T) {
	m := strings.Replace(tablesScopeManifest,
		"      - cart_items: \"cart_id = 'c_123'\"",
		"      - orders: \"email = 'other@example.com'\"", 1)
	_, err := parseAccessTables(t, m)
	if err == nil {
		t.Fatal("duplicate tables entry must fail startup")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error must name the duplicate, got: %v", err)
	}
}

func TestValidateTableFilterGrammar(t *testing.T) {
	valid := []string{
		"",
		"email = 'x@example.com'",
		"cart_id = 'c_123'",
		"count >= 5",
		"visible_until > $now",
		"tenant = $env.TENANT_ID",
		"status != 'archived'",
		"title = 'fish AND chips'", // quoted AND is a plain value
	}
	for _, f := range valid {
		if err := ValidateTableFilter(f); err != nil {
			t.Errorf("ValidateTableFilter(%q) = %v, want nil", f, err)
		}
	}
	invalid := []string{
		"just_a_column",
		"email = 'x' AND cart_id = 'y'",
		"email = 'x' OR", // trailing compound — engine would bind "'x' OR" as one param
		"a = ",           // empty value
		" = 'x'",         // empty column
		"a = b OR c AND d = 2",
	}
	for _, f := range invalid {
		if err := ValidateTableFilter(f); err == nil {
			t.Errorf("ValidateTableFilter(%q) = nil, want error", f)
		}
	}
}
