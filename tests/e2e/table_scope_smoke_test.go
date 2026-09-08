package e2e

// Smoke tests pinning the storefront's actual read patterns against the
// shopper role's tables: scope. The UI (MyOrders, Checkout, CartDrawer,
// Catalog, ProductDetail) reads exactly these tables through GET
// /tables/{name} with the shopper key — after the tables: scoping landed,
// these queries must still return what the frontend consumes.
//
// The e2e ecommerce manifest does not declare tables: scopes by default
// (the admin key is the unrestricted reader in most e2e tests); these tests
// build a scoped variant at setup by injecting the same tables: block the
// real apps/ecommerce/app.spine carries, then run the storefront's queries
// against it. The pure scoping-enforcement tests live in
// tests/security/table_scope_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/tests/testhelpers"
)

const shopperKey = "sk_shopper_key"  // matches the e2e manifest's shopper role key
const adminKey = "sk_admin_test"     // matches the e2e manifest's admin role key

// scopeAnchor marks where the shopper role's events list ends in the e2e
// manifest; the tables: block is injected right after it.
const scopeAnchor = "      - CREATE_CHECKOUT\n"

// buildScopedManifest injects the shopper tables: scope into the e2e
// ecommerce manifest, mirroring apps/ecommerce/app.spine.
func buildScopedManifest(t *testing.T) string {
	t.Helper()
	m := strings.Replace(ecommerceManifest, scopeAnchor, scopeAnchor+
		"    tables:\n"+
		"      - products:\n"+
		"      - product_variants:\n"+
		"      - shipping_zones:\n"+
		"      - tax_rules:\n"+
		"      - store_settings:\n"+
		"      - orders: \"email = 'mine@example.com'\"\n"+
		"      - order_items:\n"+
		"      - cart_items:\n", 1)
	if m == ecommerceManifest {
		t.Fatal("scope injection failed — anchor not found in e2e manifest")
	}
	return m
}

func setupScopedEcommerceEngine(t *testing.T) (*spine.Engine, func()) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "ecommerce.spine")
	dbPath := filepath.Join(dir, "ecommerce.db")
	if err := os.WriteFile(manifestPath, []byte(buildScopedManifest(t)), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	return eng, func() { _ = eng.Close() }
}

func tableGet(handler http.Handler, key, path string) (int, string) {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-API-Key", key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

// MyOrders: shopper reads orders narrowed by their own email — must return
// their rows and never another shopper's. Driven through the real
// ADD_ORDER_ITEM → PLACE_ORDER flow the storefront checkout uses.
func TestEcommerceShopperTableScopeMyOrders(t *testing.T) {
	eng, cleanup := setupScopedEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()
	bus := eng.Bus

	productID := publishProduct(t, bus, "scope-sku", 10.0, 5)
	orderID := "ord-mine"

	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": orderID, "product_id": productID, "name": "Widget", "price": 10.0, "qty": 1,
	}); err != nil {
		t.Fatalf("add order item: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c_mine", "email": "mine@example.com", "order_id": orderID,
	}); err != nil {
		t.Fatalf("place order: %v", err)
	}
	testhelpers.WaitForTableRows(t, eng, "orders", 1)

	// Second shopper's order — needs its own line item first.
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-theirs", "product_id": productID, "name": "Widget", "price": 10.0, "qty": 1,
	}); err != nil {
		t.Fatalf("add their order item: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c_theirs", "email": "theirs@example.com", "order_id": "ord-theirs",
	}); err != nil {
		t.Fatalf("place their order: %v", err)
	}
	testhelpers.WaitForTableRows(t, eng, "orders", 2)

	// The storefront query: /tables/orders?where=email:<mine>.
	code, body := tableGet(handler, shopperKey, "/tables/orders?where=email:mine@example.com")
	if code != 200 {
		t.Fatalf("shopper reading orders: %d %s", code, body)
	}
	if !strings.Contains(body, "mine@example.com") {
		t.Errorf("own order missing: %s", body)
	}
	if strings.Contains(body, "theirs@example.com") {
		t.Fatal("SCOPE LEAK: other shopper's order visible")
	}

	// MyOrders item drill-down: order_items by order_id.
	code, body = tableGet(handler, shopperKey, "/tables/order_items?where=order_id:"+orderID)
	if code != 200 {
		t.Fatalf("shopper reading order_items: %d %s", code, body)
	}
}

// Checkout: shopper reads the public pricing tables (no filter = whole
// table) — shipping zones, tax rules, products, variants, plans, settings.
func TestEcommerceShopperTableScopePublicTables(t *testing.T) {
	eng, cleanup := setupScopedEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()

	for _, path := range []string{
		"/tables/products", "/tables/product_variants",
		"/tables/shipping_zones", "/tables/tax_rules", "/tables/store_settings",
	} {
		code, body := tableGet(handler, shopperKey, path)
		if code != 200 {
			t.Errorf("shopper reading %s: %d %s", path, code, body)
		}
	}
}

// CartDrawer / catalog: cart_items by the browser's cart_id.
func TestEcommerceShopperTableScopeCart(t *testing.T) {
	eng, cleanup := setupScopedEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()
	bus := eng.Bus

	productID := publishProduct(t, bus, "cart-sku", 5.0, 10)
	if _, err := bus.Emit("ADD_TO_CART", map[string]interface{}{
		"cart_id": "c_mine", "product_id": productID, "name": "Widget",
		"price": 5.0, "qty": 1, "variant_id": "",
	}); err != nil {
		t.Fatalf("add to cart: %v", err)
	}
	// db.upsert on this route goes through the async batched writer — wait
	// for the row like the storefront does (its badge refreshes on the
	// CART_UPDATED broadcast, which lands only after the flush).
	testhelpers.WaitForTableRows(t, eng, "cart_items", 1)

	code, body := tableGet(handler, shopperKey, "/tables/cart_items?where=cart_id:c_mine")
	if code != 200 {
		t.Fatalf("shopper reading cart_items: %d %s", code, body)
	}
	if !strings.Contains(body, "Widget") {
		t.Errorf("cart rows missing: %s", body)
	}
}

// The sensitive tables stay denied for the shopper key; admin keeps full read.
func TestEcommerceShopperTableScopeSensitiveDenied(t *testing.T) {
	eng, cleanup := setupScopedEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()

	for _, path := range []string{
		"/tables/users", "/tables/sessions", "/tables/payments",
		"/tables/password_resets", "/tables/coupons",
	} {
		code, body := tableGet(handler, shopperKey, path)
		if code != 403 {
			t.Errorf("sensitive table %s: expected 403, got %d %s", path, code, body)
		}
	}

	code, body := tableGet(handler, adminKey, "/tables/payments")
	if code != 200 {
		t.Fatalf("admin reading payments: %d %s", code, body)
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.Status != "ok" {
		t.Errorf("admin payments read: %s err=%v", body, err)
	}
}
