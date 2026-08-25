package tests

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// The ecommerce app manifest — kept in sync with apps/ecommerce/app.spine.
// CI parses it against a temp DB and pins every public contract plus the
// Phase-1 integrity rules (stock math, price-guard rejection, cancel-restock).
const ecommerceManifest = `spine_version: 1

database:
  tables:
    - products
    - product_variants
    - cart_items
    - orders
    - order_items
    - coupons
    - store_settings
    - shipping_zones
    - tax_rules

access:
  - role: admin
    key: "sk_admin_test"
  - role: staff
    key: "sk_staff_test"
    events:
      - UPDATE_ORDER_STATUS
      - RESTOCK_ORDER_ITEM
  - role: shopper
    key: "sk_shopper_key"
    events:
      - ADD_TO_CART
      - UPDATE_CART_ITEM
      - REMOVE_FROM_CART
      - PLACE_ORDER
      - ADD_ORDER_ITEM
      - VALIDATE_COUPON

nodes:
  Catalog:
    emits:
      - event: PUBLISH_PRODUCT
        payload:
          sku: string
          name: string
          price: number
          stock: integer
          description: string
          image_url: string
          category: string
      - event: PUBLISH_VARIANT
        payload:
          sku: string
          product_id: string
          option1_name: string
          option1_value: string
          price: number
          stock: integer

  Checkout:
    emits:
      - event: PLACE_ORDER
        payload:
          cart_id: string
          email: string
          order_id: string
      - event: ADD_ORDER_ITEM
        payload:
          order_id: string
          product_id: string
          name: string
          price: number
          qty: integer
      - event: VALIDATE_COUPON
        payload:
          cart_id: string
          code: string

  Storefront:
    emits:
      - event: ADD_TO_CART
        payload:
          cart_id: string
          product_id: string
          name: string
          price: number
          qty: integer
          variant_id: string
      - event: UPDATE_CART_ITEM
        payload:
          cart_id: string
          product_id: string
          variant_id: string
          qty: integer
      - event: REMOVE_FROM_CART
        payload:
          cart_id: string
          product_id: string
          variant_id: string

  Operations:
    emits:
      - event: UPDATE_ORDER_STATUS
        payload:
          id: string
          status: string
          actor: string
      - event: RESTOCK_ORDER_ITEM
        payload:
          order_id: string
          product_id: string
          qty: integer
          actor: string
      - event: CREATE_COUPON
        payload:
          code: string
          percent_off: number
          fixed_off: number
          active: string
      - event: SAVE_SHIPPING_ZONE
        payload:
          country: string
          rate: number
      - event: SAVE_TAX_RULE
        payload:
          country: string
          rate: number

routes:
  - on: PUBLISH_PRODUCT
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.upsert
        table: products
        key: sku
    emit: PRODUCT_PUBLISHED

  - on: PUBLISH_VARIANT
    steps:
      - action: set
        id: $uuid
        created_at: $now
      - action: db.upsert
        table: product_variants
        key: sku
    emit: VARIANT_PUBLISHED

  - on: ADD_TO_CART
    steps:
      - action: set
        line_key: "$event.payload.cart_id|$event.payload.product_id|$event.payload.variant_id"
        updated_at: $now
      - action: db.upsert
        table: cart_items
        key: line_key
    emit: CART_UPDATED

  - on: UPDATE_CART_ITEM
    steps:
      - action: assert
        condition: "$event.payload.qty >= 1"
        message: "cart quantity must be at least 1"
      - action: set
        line_key: "$event.payload.cart_id|$event.payload.product_id|$event.payload.variant_id"
        updated_at: $now
      - action: db.update
        table: cart_items
        where: "line_key = '$event.payload.line_key'"
    emit: CART_UPDATED

  - on: REMOVE_FROM_CART
    steps:
      - action: set
        line_key: "$event.payload.cart_id|$event.payload.product_id|$event.payload.variant_id"
      - action: db.delete
        table: cart_items
        where: "line_key = '$event.payload.line_key'"
    emit: CART_UPDATED

  - on: ADD_ORDER_ITEM
    on_failure: CHECKOUT_FAILED
    steps:
      - action: db.lookup
        table: product_variants
        key_column: id
        value_expr: $event.payload.variant_id
        as: variant_
        if: "$event.payload.variant_id exists"
      - action: assert
        condition: "$event.payload.price == $event.payload.variant_price"
        message: "variant price mismatch"
        if: "$event.payload.variant_id exists"
      - action: assert
        condition: "$event.payload.qty <= $event.payload.variant_stock"
        message: "insufficient variant stock"
        if: "$event.payload.variant_id exists"
      - action: db.lookup
        table: products
        key_column: id
        value_expr: $event.payload.product_id
        as: product_
        if: "$event.payload.variant_id == ''"
      - action: assert
        condition: "$event.payload.price == $event.payload.product_price"
        message: "price mismatch"
        if: "$event.payload.variant_id == ''"
      - action: assert
        condition: "$event.payload.qty <= $event.payload.product_stock"
        message: "insufficient stock"
        if: "$event.payload.variant_id == ''"
      - action: math.calc
        set: line_total
        expr: "$event.payload.price * $event.payload.qty"
      - action: unset
        fields: "product_sku product_name product_price product_stock product_description product_image_url product_category product_created_at variant_sku variant_option1_name variant_option1_value variant_option2_name variant_option2_value variant_price variant_stock variant_created_at"
      - action: db.insert
        table: order_items
        sync: true
      - action: db.adjust
        table: product_variants
        column: stock
        by: "-$event.payload.qty"
        floor: "0"
        where: "id = $event.payload.variant_id"
        if: "$event.payload.variant_id exists"
      - action: db.adjust
        table: products
        column: stock
        by: "-$event.payload.qty"
        floor: "0"
        where: "id = $event.payload.product_id"
        if: "$event.payload.variant_id == ''"
    emit: ORDER_ITEM_ADDED

  - on: RESTOCK_ORDER_ITEM
    steps:
      - action: db.adjust
        table: product_variants
        column: stock
        by: $event.payload.qty
        where: "id = $event.payload.variant_id"
        if: "$event.payload.variant_id exists"
      - action: db.adjust
        table: products
        column: stock
        by: $event.payload.qty
        where: "id = $event.payload.product_id"
        if: "$event.payload.variant_id == ''"
    emit: STOCK_ADJUSTED

  - on: UPDATE_ORDER_STATUS
    steps:
      - action: set
        updated_at: $now
      - action: db.update
        table: orders
    emit: ORDER_STATUS_CHANGED

  - on: PLACE_ORDER
    on_failure: CHECKOUT_FAILED
    steps:
      - action: set
        id: $event.payload.order_id
        status: pending
        created_at: $now
        coupon_discount: 0
      - action: db.sum
        table: order_items
        column: line_total
        where: "order_id = '$event.payload.order_id'"
        as: subtotal
      - action: assert
        condition: "$event.payload.subtotal > 0"
        message: "cannot place an order with no items"
      - action: db.sum
        table: shipping_zones
        column: rate
        where: "country = '$event.payload.country'"
        as: shipping_cost
      - action: db.sum
        table: tax_rules
        column: rate
        where: "country = '$event.payload.country'"
        as: tax_rate
      - action: db.lookup
        table: coupons
        key_column: code
        value_expr: $event.payload.coupon_code
        as: coupon_
        if: "$event.payload.coupon_code exists"
      - action: assert
        condition: "$event.payload.coupon_active == true"
        message: "coupon not active"
        if: "$event.payload.coupon_code exists"
      - action: math.calc
        set: tax_amount
        expr: "$event.payload.subtotal * $event.payload.tax_rate / 100"
      - action: math.calc
        set: coupon_discount
        expr: "$event.payload.subtotal * $event.payload.coupon_percent_off / 100"
        if: "$event.payload.coupon_code exists"
      - action: math.calc
        set: total
        expr: "$event.payload.subtotal + $event.payload.shipping_cost + $event.payload.tax_amount - $event.payload.coupon_discount"
      - action: unset
        fields: "coupon_active coupon_percent_off coupon_fixed_off coupon_created_at tax_rate"
      - action: db.insert
        table: orders
    emit: ORDER_CREATED

  - on: VALIDATE_COUPON
    on_failure: COUPON_REJECTED
    steps:
      - action: db.lookup
        table: coupons
        key_column: code
        value_expr: $event.payload.code
        as: coupon_
      - action: assert
        condition: "$event.payload.coupon_active == true"
    emit: COUPON_VALIDATED

  - on: CREATE_COUPON
    steps:
      - action: set
        created_at: $now
      - action: db.upsert
        table: coupons
        key: code
    emit: COUPON_SAVED

  - on: SAVE_SHIPPING_ZONE
    steps:
      - action: set
        created_at: $now
      - action: db.upsert
        table: shipping_zones
        key: country
    emit: SHIPPING_ZONE_SAVED

  - on: SAVE_TAX_RULE
    steps:
      - action: set
        created_at: $now
      - action: db.upsert
        table: tax_rules
        key: country
    emit: TAX_RULE_SAVED
`

func setupEcommerceEngine(t *testing.T) (*spine.Engine, func()) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "ecommerce.spine")
	dbPath := filepath.Join(dir, "ecommerce.db")

	if err := os.WriteFile(manifestPath, []byte(ecommerceManifest), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	return eng, func() { eng.Close() }
}

func emitOK(t *testing.T, handler interface{ ServeHTTP(a, b interface{}) }, apiKey, event string, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	return nil
}

// waitUntil polls a condition for up to 5s — db.insert/upsert flow through the
// sharded batched writer, so synchronous test reads must wait out the flush.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
// publishProduct upserts a catalog row and waits for the batched write to
// land, returning the engine-assigned id.
func publishProduct(t *testing.T, bus *spine.Bus, sku string, price float64, stock int) string {
	t.Helper()
	res, err := bus.Emit("PUBLISH_PRODUCT", map[string]interface{}{
		"sku": sku, "name": "Product " + sku, "price": price,
		"stock": stock, "description": "", "image_url": "", "category": "",
	})
	if err != nil || res["status"] != "ok" {
		t.Fatalf("publish %s failed: %v %v", sku, err, res)
	}
	var productID string
	waitUntil(t, "product row "+sku, func() bool {
		return bus.DB().QueryRow(`SELECT id FROM products WHERE sku = ?`, sku).Scan(&productID) == nil
	})
	return productID
}

// TestEcommerceStockDecrementsOnOrder is the Phase 1 gate: selling must move stock.
func TestEcommerceStockDecrementsOnOrder(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "test-sku", 10.0, 5)

	orderID := "ord-test-1"
	// Checkout flow: line items are added BEFORE the order is placed (the
	// server refuses to place an empty order — subtotal must be > 0).
	if res, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": orderID, "product_id": productID,
		"name": "Product test-sku", "price": 10.0, "qty": 2,
	}); err != nil || res["status"] != "ok" {
		t.Fatalf("add_order_item failed: %v %v", err, res)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "cart1", "email": "buyer@test.dev", "order_id": orderID,
	}); err != nil {
		t.Fatalf("place_order failed: %v", err)
	}

	// Stock must drop 5 → 3 via atomic db.adjust (synchronous — no poll needed)
	var stock int
	if err := bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 3 {
		t.Fatalf("expected stock decremented to 3, got %d", stock)
	}
}

// TestEcommerceTamperedPriceRejected is the Phase 1 gate: client-trusted prices
// must be rejected server-side via CHECKOUT_FAILED.
func TestEcommerceTamperedPriceRejected(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "tamper-sku", 50.0, 10)

	_, _ = bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "cart2", "email": "thief@test.dev", "order_id": "ord-tamper",
	})

	// Attacker claims the $50 product costs $1
	res, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-tamper", "product_id": productID,
		"name": "Product tamper-sku", "price": 1.0, "qty": 1,
	})
	if err == nil {
		t.Fatalf("tampered price accepted: %v", res)
	}
	if !strings.Contains(err.Error(), "price mismatch") {
		t.Fatalf("expected price mismatch error, got: %v", err)
	}

	// No line item may exist for the rejected claim, and stock stays intact.
	// (order_items may not even have its columns yet if no insert ever landed —
	// a missing table/column means zero rows, which is exactly what we assert.)
	count := 0
	_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM order_items WHERE order_id = 'ord-tamper'`).Scan(&count)
	if count != 0 {
		t.Fatalf("rejected line was persisted (%d rows)", count)
	}
	var stock int
	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 10 {
		t.Fatalf("stock moved on a rejected sale: %d", stock)
	}
}

// TestEcommerceInsufficientStockRejected: phantom inventory cannot be sold.
func TestEcommerceInsufficientStockRejected(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "scarce-sku", 20.0, 2)

	_, _ = bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "cart3", "email": "buyer@test.dev", "order_id": "ord-scarce",
	})
	_, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-scarce", "product_id": productID,
		"name": "Product scarce-sku", "price": 20.0, "qty": 9,
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("oversell not blocked: %v", err)
	}
}

// TestEcommerceCancelRestocks is the Phase 1 gate: cancelling restores stock.
func TestEcommerceCancelRestocks(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "restock-sku", 15.0, 4)

	orderID := "ord-restock"
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": orderID, "product_id": productID,
		"name": "Product restock-sku", "price": 15.0, "qty": 4,
	}); err != nil {
		t.Fatalf("add_order_item failed: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "cart4", "email": "buyer@test.dev", "order_id": orderID,
	}); err != nil {
		t.Fatalf("place_order failed: %v", err)
	}

	// Sold out — adjust is synchronous so read straight away
	var stock int
	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 0 {
		t.Fatalf("expected stock 0 after selling out, got %d", stock)
	}

	// Admin cancels → per-line restock driven from admin UI
	if _, err := bus.Emit("UPDATE_ORDER_STATUS", map[string]interface{}{
		"id": "ord-restock", "status": "cancelled", "actor": "admin@panel",
	}); err != nil {
		t.Fatalf("status update failed: %v", err)
	}
	if _, err := bus.Emit("RESTOCK_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-restock", "product_id": productID, "qty": 4, "actor": "admin@panel",
	}); err != nil {
		t.Fatalf("restock failed: %v", err)
	}

	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 4 {
		t.Fatalf("cancel did not restore stock: %d", stock)
	}
	status := ""
	waitUntil(t, "cancelled status", func() bool {
		return bus.DB().QueryRow(`SELECT status FROM orders WHERE id = 'ord-restock'`).Scan(&status) == nil && status == "cancelled"
	})
}

// TestEcommerceCouponFlow: validation rejects bad codes and honors good ones;
// PLACE_ORDER re-verifies the claimed discount server-side.
func TestEcommerceCouponFlow(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	// Unknown code fails validation → COUPON_REJECTED broadcast, error returned
	res, err := bus.Emit("VALIDATE_COUPON", map[string]interface{}{"cart_id": "c1", "code": "NOPE"})
	if err == nil {
		t.Fatalf("unknown coupon accepted without error: %v", res)
	}
	states, _ := res["emitted_states"].([]string)
	if len(states) == 0 || states[0] != "COUPON_REJECTED" {
		t.Fatalf("expected COUPON_REJECTED, got %v", states)
	}

	// Admin creates an active 20% code
	if _, err := bus.Emit("CREATE_COUPON", map[string]interface{}{
		"code": "SAVE20", "percent_off": 20.0, "fixed_off": 0.0, "active": "true",
	}); err != nil {
		t.Fatalf("create_coupon failed: %v", err)
	}
	waitUntil(t, "coupon row", func() bool {
		var n int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM coupons WHERE code = 'SAVE20'`).Scan(&n)
		return n == 1
	})

	res, err = bus.Emit("VALIDATE_COUPON", map[string]interface{}{"cart_id": "c1", "code": "SAVE20"})
	if err != nil {
		t.Fatalf("valid coupon rejected: %v", err)
	}
	states, _ = res["emitted_states"].([]string)
	if len(states) == 0 || states[0] != "COUPON_VALIDATED" {
		t.Fatalf("expected COUPON_VALIDATED, got %v", states)
	}

	// Order claiming the coupon — the discount is now COMPUTED server-side
	// (subtotal × percent), never taken from the client. Add an item first
	// (subtotal must be > 0 to place).
	productID := publishProduct(t, bus, "coupon-sku", 50.0, 5)
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-coupon", "product_id": productID,
		"name": "Product coupon-sku", "price": 50.0, "qty": 2,
	}); err != nil {
		t.Fatalf("add_order_item failed: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c1", "email": "b@t.dev", "order_id": "ord-coupon",
		"coupon_code": "SAVE20",
	}); err != nil {
		t.Fatalf("place_order with coupon failed: %v", err)
	}
	var discount float64
	waitUntil(t, "discount persisted", func() bool {
		return bus.DB().QueryRow(`SELECT coupon_discount FROM orders WHERE id = 'ord-coupon'`).Scan(&discount) == nil && discount == 20
	})

	// A client-claimed discount is now IMPOSSIBLE to tamper with: the claim is
	// ignored and the server-computed value (subtotal × 20% = 20) persists.
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-tampered-discount", "product_id": productID,
		"name": "Product coupon-sku", "price": 50.0, "qty": 2,
	}); err != nil {
		t.Fatalf("add_order_item (tampered order) failed: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c1", "email": "b@t.dev", "order_id": "ord-tampered-discount",
		"coupon_code": "SAVE20", "coupon_discount": 90.0, "total": 1.0,
	}); err != nil {
		t.Fatalf("place_order with tampered claims should still succeed (claims ignored): %v", err)
	}
	var tamperedDiscount, tamperedTotal float64
	waitUntil(t, "tampered order persisted", func() bool {
		return bus.DB().QueryRow(`SELECT coupon_discount, total FROM orders WHERE id = 'ord-tampered-discount'`).Scan(&tamperedDiscount, &tamperedTotal) == nil
	})
	if tamperedDiscount != 20 {
		t.Fatalf("client claim of 90%% off must be ignored; server-computed discount should be 20, got %v", tamperedDiscount)
	}
	if tamperedTotal != 80 {
		t.Fatalf("client claim of total=1 must be ignored; server-computed total should be 80, got %v", tamperedTotal)
	}
}

// TestEcommerceServerCalculatedTotals pins the Phase-6 gate: subtotal,
// shipping, tax and total are all computed SERVER-SIDE from persisted line
// items and admin-configured rules — no client-supplied dollar amount can
// reach the order row.
func TestEcommerceServerCalculatedTotals(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "total-sku", 10.0, 10)

	// Admin configures US: $5.99 flat shipping, 8.25% tax
	if _, err := bus.Emit("SAVE_SHIPPING_ZONE", map[string]interface{}{"country": "US", "rate": 5.99}); err != nil {
		t.Fatalf("save shipping zone failed: %v", err)
	}
	if _, err := bus.Emit("SAVE_TAX_RULE", map[string]interface{}{"country": "US", "rate": 8.25}); err != nil {
		t.Fatalf("save tax rule failed: %v", err)
	}
	waitUntil(t, "zone row", func() bool {
		var n int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM shipping_zones WHERE country = 'US'`).Scan(&n)
		return n == 1
	})
	waitUntil(t, "tax row", func() bool {
		var n int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM tax_rules WHERE country = 'US'`).Scan(&n)
		return n == 1
	})

	orderID := "ord-totals"
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": orderID, "product_id": productID,
		"name": "Product total-sku", "price": 10.0, "qty": 2,
	}); err != nil {
		t.Fatalf("add_order_item failed: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c-totals", "email": "b@t.dev", "order_id": orderID, "country": "US",
	}); err != nil {
		t.Fatalf("place_order failed: %v", err)
	}

	var subtotal, shipping, tax, discount, total float64
	waitUntil(t, "order totals row", func() bool {
		return bus.DB().QueryRow(`SELECT subtotal, shipping_cost, tax_amount, coupon_discount, total FROM orders WHERE id = ?`, orderID).
			Scan(&subtotal, &shipping, &tax, &discount, &total) == nil
	})
	// line_total = 10 × 2 = 20 (server-computed at ADD_ORDER_ITEM)
	if subtotal != 20.0 {
		t.Errorf("subtotal: expected 20, got %v", subtotal)
	}
	if shipping != 5.99 {
		t.Errorf("shipping: expected 5.99 (US zone), got %v", shipping)
	}
	if tax != 1.65 {
		t.Errorf("tax: expected 1.65 (20 × 8.25%%), got %v", tax)
	}
	if discount != 0 {
		t.Errorf("discount: expected 0, got %v", discount)
	}
	if total != 27.64 {
		t.Errorf("total: expected 27.64 (20 + 5.99 + 1.65), got %v", total)
	}
}

// TestEcommerceUnconfiguredZoneDefaultsToZero: missing zone/tax config must
// not break checkout — shipping 0, tax 0 (documented default; the admin UI
// makes the missing rules visible).
func TestEcommerceUnconfiguredZoneDefaultsToZero(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "free-sku", 7.0, 3)
	orderID := "ord-free"
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": orderID, "product_id": productID,
		"name": "Product free-sku", "price": 7.0, "qty": 1,
	}); err != nil {
		t.Fatalf("add_order_item failed: %v", err)
	}
	if _, err := bus.Emit("PLACE_ORDER", map[string]interface{}{
		"cart_id": "c-free", "email": "b@t.dev", "order_id": orderID, "country": "ZZ",
	}); err != nil {
		t.Fatalf("place_order failed: %v", err)
	}

	var subtotal, shipping, tax, total float64
	waitUntil(t, "free order row", func() bool {
		return bus.DB().QueryRow(`SELECT subtotal, shipping_cost, tax_amount, total FROM orders WHERE id = ?`, orderID).
			Scan(&subtotal, &shipping, &tax, &total) == nil
	})
	if subtotal != 7.0 || shipping != 0 || tax != 0 || total != 7.0 {
		t.Fatalf("expected 7/0/0/7 for unconfigured country, got subtotal=%v shipping=%v tax=%v total=%v", subtotal, shipping, tax, total)
	}
}

// publishVariant upserts a product_variants row (admin event) and returns the
// engine-assigned variant id.
func publishVariant(t *testing.T, bus *spine.Bus, sku, productID, optName, optValue string, price float64, stock int) string {
	t.Helper()
	res, err := bus.Emit("PUBLISH_VARIANT", map[string]interface{}{
		"sku": sku, "product_id": productID,
		"option1_name": optName, "option1_value": optValue,
		"price": price, "stock": stock,
	})
	if err != nil || res["status"] != "ok" {
		t.Fatalf("publish variant %s failed: %v %v", sku, err, res)
	}
	var variantID string
	waitUntil(t, "variant row "+sku, func() bool {
		return bus.DB().QueryRow(`SELECT id FROM product_variants WHERE sku = ?`, sku).Scan(&variantID) == nil
	})
	return variantID
}

// TestEcommerceVariantPricingAndStock pins the variants contract: per-SKU
// price/stock are enforced against product_variants, tampered variant prices
// are rejected, variant oversell is blocked, header product stock is
// untouched by variant sales, and the no-variant path still works.
func TestEcommerceVariantPricingAndStock(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "variant-sku", 10.0, 5)
	variantID := publishVariant(t, bus, "variant-sku-s", productID, "Size", "S", 10.5, 3)

	// Order the S variant at its correct per-SKU price
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-1", "product_id": productID, "variant_id": variantID,
		"name": "Product variant-sku (S)", "price": 10.5, "qty": 2,
	}); err != nil {
		t.Fatalf("variant order failed: %v", err)
	}

	// Variant stock decremented 3 → 1; header product stock untouched
	var vStock int
	if err := bus.DB().QueryRow(`SELECT stock FROM product_variants WHERE id = ?`, variantID).Scan(&vStock); err != nil {
		t.Fatal(err)
	}
	if vStock != 1 {
		t.Fatalf("expected variant stock 1, got %d", vStock)
	}
	var pStock int
	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&pStock)
	if pStock != 5 {
		t.Fatalf("header stock must not move on variant sale, got %d", pStock)
	}
	var storedVariant string
	if err := bus.DB().QueryRow(`SELECT variant_id FROM order_items WHERE order_id = 'ord-var-1'`).Scan(&storedVariant); err != nil {
		t.Fatal(err)
	}
	if storedVariant != variantID {
		t.Fatalf("order_items must carry variant_id, got %q", storedVariant)
	}

	// Tampered variant price rejected
	_, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-2", "product_id": productID, "variant_id": variantID,
		"name": "Product variant-sku (S)", "price": 1.0, "qty": 1,
	})
	if err == nil || !strings.Contains(err.Error(), "variant price mismatch") {
		t.Fatalf("tampered variant price not rejected: %v", err)
	}

	// Variant oversell blocked
	_, err = bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-3", "product_id": productID, "variant_id": variantID,
		"name": "Product variant-sku (S)", "price": 10.5, "qty": 99,
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient variant stock") {
		t.Fatalf("variant oversell not blocked: %v", err)
	}

	// Backward compatibility: no variant_id → product path still works
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-4", "product_id": productID,
		"name": "Product variant-sku", "price": 10.0, "qty": 1,
	}); err != nil {
		t.Fatalf("plain product order failed after variant support: %v", err)
	}
	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&pStock)
	if pStock != 4 {
		t.Fatalf("expected header stock 4 after plain order, got %d", pStock)
	}
}

// TestEcommerceVariantCancelRestocks: cancelling a variant line restores the
// VARIANT stock counter (not the header).
func TestEcommerceVariantCancelRestocks(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "var-restock-sku", 20.0, 5)
	variantID := publishVariant(t, bus, "var-restock-m", productID, "Size", "M", 21.0, 3)

	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-r", "product_id": productID, "variant_id": variantID,
		"name": "Product var-restock-sku (M)", "price": 21.0, "qty": 2,
	}); err != nil {
		t.Fatalf("variant order failed: %v", err)
	}
	var vStock int
	_ = bus.DB().QueryRow(`SELECT stock FROM product_variants WHERE id = ?`, variantID).Scan(&vStock)
	if vStock != 1 {
		t.Fatalf("expected variant stock 1 after sale, got %d", vStock)
	}

	if _, err := bus.Emit("RESTOCK_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-var-r", "product_id": productID, "variant_id": variantID,
		"qty": 2, "actor": "admin@panel",
	}); err != nil {
		t.Fatalf("variant restock failed: %v", err)
	}
	_ = bus.DB().QueryRow(`SELECT stock FROM product_variants WHERE id = ?`, variantID).Scan(&vStock)
	if vStock != 3 {
		t.Fatalf("cancel did not restore variant stock: %d", vStock)
	}
	var pStock int
	_ = bus.DB().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&pStock)
	if pStock != 5 {
		t.Fatalf("header stock must not change on variant restock, got %d", pStock)
	}
}

// TestEcommerceGalleryRoundTrip: uploaded gallery images ride along through
// schema evolution and round-trip as a JSON column (data URLs supported).
func TestEcommerceGalleryRoundTrip(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	res, err := bus.Emit("PUBLISH_PRODUCT", map[string]interface{}{
		"sku": "gallery-sku", "name": "Gallery Item", "price": 9.0, "stock": 5,
		"description": "", "image_url": "https://example.com/a.jpg", "category": "",
		"gallery": []interface{}{"data:image/jpeg;base64,AAA", "https://example.com/b.jpg"},
	})
	if err != nil || res["status"] != "ok" {
		t.Fatalf("publish with gallery failed: %v %v", err, res)
	}
	var raw string
	waitUntil(t, "gallery row", func() bool {
		return bus.DB().QueryRow(`SELECT gallery FROM products WHERE sku = 'gallery-sku'`).Scan(&raw) == nil
	})
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		t.Fatalf("gallery not stored as JSON array: %q (%v)", raw, err)
	}
	if len(arr) != 2 || arr[0] != "data:image/jpeg;base64,AAA" || arr[1] != "https://example.com/b.jpg" {
		t.Fatalf("gallery round-trip mismatch: %v", arr)
	}
}

// TestEcommerceCartLifecycle: ADD_TO_CART writes a cart_items line keyed by
// (cart|product|variant), re-adding upserts instead of duplicating, variant
// lines stay separate, and UPDATE_CART_ITEM / REMOVE_FROM_CART mutate the
// same line. (The cart write routes were missing from the manifest entirely
// — this test pins them so the storefront's cart can never silently no-op.)
func TestEcommerceCartLifecycle(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	bus := eng.Bus

	productID := publishProduct(t, bus, "cart-sku", 8.0, 10)
	variantID := publishVariant(t, bus, "cart-sku-m", productID, "Size", "M", 9.0, 4)

	add := func(cart, pid, vid string, qty int) {
		t.Helper()
		if _, err := bus.Emit("ADD_TO_CART", map[string]interface{}{
			"cart_id": cart, "product_id": pid, "variant_id": vid,
			"name": "Cart Item", "price": 8.0, "qty": qty,
		}); err != nil {
			t.Fatalf("ADD_TO_CART failed: %v", err)
		}
	}
	waitRow := func(cart, pid, vid string) {
		t.Helper()
		key := cart + "|" + pid + "|" + vid
		waitUntil(t, "cart line "+key, func() bool {
			var lineKey string
			return bus.DB().QueryRow(`SELECT line_key FROM cart_items WHERE line_key = ?`, key).Scan(&lineKey) == nil
		})
	}
	rowCount := func() int {
		var n int
		bus.DB().QueryRow(`SELECT COUNT(*) FROM cart_items`).Scan(&n)
		return n
	}

	add("cart-1", productID, "", 1)
	waitRow("cart-1", productID, "")

	// Re-add the same line → upsert (still one row), qty takes the new value
	add("cart-1", productID, "", 2)
	waitUntil(t, "upserted qty", func() bool {
		var q int
		bus.DB().QueryRow(`SELECT qty FROM cart_items WHERE line_key = ?`, "cart-1|"+productID+"|").Scan(&q)
		return q == 2
	})
	if got := rowCount(); got != 1 {
		t.Fatalf("re-add must upsert, not duplicate: %d rows", got)
	}

	// Variant line is a separate row
	add("cart-1", productID, variantID, 1)
	waitRow("cart-1", productID, variantID)
	if got := rowCount(); got != 2 {
		t.Fatalf("variant line must be separate: %d rows", got)
	}

	// Update qty on the plain line
	if _, err := bus.Emit("UPDATE_CART_ITEM", map[string]interface{}{
		"cart_id": "cart-1", "product_id": productID, "variant_id": "", "qty": 3,
	}); err != nil {
		t.Fatalf("UPDATE_CART_ITEM failed: %v", err)
	}
	waitUntil(t, "updated qty", func() bool {
		var q int
		bus.DB().QueryRow(`SELECT qty FROM cart_items WHERE line_key = ?`, "cart-1|"+productID+"|").Scan(&q)
		return q == 3
	})

	// Remove the plain line; the variant line survives
	if _, err := bus.Emit("REMOVE_FROM_CART", map[string]interface{}{
		"cart_id": "cart-1", "product_id": productID, "variant_id": "",
	}); err != nil {
		t.Fatalf("REMOVE_FROM_CART failed: %v", err)
	}
	waitUntil(t, "line removed", func() bool {
		var n int
		bus.DB().QueryRow(`SELECT COUNT(*) FROM cart_items WHERE line_key = ?`, "cart-1|"+productID+"|").Scan(&n)
		return n == 0
	})
	if got := rowCount(); got != 1 {
		t.Fatalf("expected only the variant line to remain, got %d rows", got)
	}

	// Quantity guard: 0 is rejected
	_, err := bus.Emit("UPDATE_CART_ITEM", map[string]interface{}{
		"cart_id": "cart-1", "product_id": productID, "variant_id": "", "qty": 0,
	})
	if err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("qty 0 must be rejected: %v", err)
	}
}

// TestEcommerceShopperCannotRestock: RLAC keeps fulfilment events out of
// shopper hands.
func TestEcommerceShopperCannotRestock(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()
	payload, _ := json.Marshal(map[string]interface{}{
		"event":   "RESTOCK_ORDER_ITEM",
		"payload": map[string]interface{}{"order_id": "o", "product_id": "p", "qty": 99},
	})
	req := httptest.NewRequest("POST", "/emit", strings.NewReader(string(payload)))
	req.Header.Set("X-API-Key", "sk_shopper_key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("shopper restock not forbidden: HTTP %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestEcommerceStaffRoleCanFulfilOnly: staff key ships orders but cannot
// publish products (Phase 4 gate).
func TestEcommerceStaffRoleCanFulfilOnly(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()

	handler := eng.HTTPHandler()

	// Staff may move orders through the pipeline
	payload, _ := json.Marshal(map[string]interface{}{
		"event":   "UPDATE_ORDER_STATUS",
		"payload": map[string]interface{}{"id": "o1", "status": "shipped", "actor": "staff@panel"},
	})
	req := httptest.NewRequest("POST", "/emit", strings.NewReader(string(payload)))
	req.Header.Set("X-API-Key", "sk_staff_test")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("staff fulfilment blocked: HTTP %d body=%s", rr.Code, rr.Body.String())
	}

	// …but may not touch the catalog
	payload, _ = json.Marshal(map[string]interface{}{
		"event":   "PUBLISH_PRODUCT",
		"payload": map[string]interface{}{"sku": "x", "name": "x", "price": 1.0, "stock": 1},
	})
	req = httptest.NewRequest("POST", "/emit", strings.NewReader(string(payload)))
	req.Header.Set("X-API-Key", "sk_staff_test")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("staff publish not forbidden: HTTP %d body=%s", rr.Code, rr.Body.String())
	}
}
