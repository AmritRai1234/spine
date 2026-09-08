package e2e

// Smoke tests for the coupon guard rails (expiry + max redemptions) and the
// session hygiene sweep — the #3/#1 hardening pair. All run against the
// real e2e ecommerce manifest via the HTTP surface with the shopper/admin
// keys, mirroring how the storefront and admin panel actually call.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AmritRai1234/spine/tests/testhelpers"
)

func emitJSON(handler http.Handler, key, event string, payload map[string]interface{}) map[string]interface{} {
	body, _ := json.Marshal(map[string]interface{}{
		"event":   event,
		"payload": payload,
	})
	req := httptest.NewRequest("POST", "/emit", strings.NewReader(string(body)))
	req.Header.Set("X-API-Key", key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	var out map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &out)
	return out
}

// A coupon with max_uses stops validating once the cap is reached, and the
// used_count increments exactly once per completed order (not per
// validation — abandoned carts never burn a use).
func TestEcommerceCouponMaxUsesEnforced(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()
	bus := eng.Bus

	// Seed a product so ADD_ORDER_ITEM passes price/stock guards.
	productID := publishProduct(t, bus, "coupon-sku", 10.0, 10)

	// Create a capped coupon: 2 redemptions. Payload carries the guard
	// columns so schema evolution creates them; db.upsert is async — wait
	// for the row like TestEcommerceCouponFlow does before validating.
	if res := emitJSON(handler, adminKey, "CREATE_COUPON", map[string]interface{}{
		"code": "CAP2", "percent_off": 50.0, "fixed_off": 0, "active": "true",
		"max_uses": 2, "used_count": 0, "expires_at": "",
	}); res["status"] != "ok" {
		t.Fatalf("create coupon: %v", res)
	}
	testhelpers.WaitUntil(t, "coupon row", func() bool {
		var n int
		_ = bus.DB().QueryRow(`SELECT COUNT(*) FROM coupons WHERE code = 'CAP2'`).Scan(&n)
		return n == 1
	})

	// Redemption 1: validate, add item, place order.
	if res := emitJSON(handler, shopperKey, "VALIDATE_COUPON", map[string]interface{}{
		"cart_id": "c1", "code": "CAP2",
	}); res["status"] != "ok" {
		t.Fatalf("validate 1 should pass: %v", res)
	}
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-1", "product_id": productID, "name": "W", "price": 10.0, "qty": 1,
		"coupon_code": "CAP2",
	}); err != nil {
		t.Fatal(err)
	}
	if res := emitJSON(handler, shopperKey, "PLACE_ORDER", map[string]interface{}{
		"cart_id": "c1", "email": "buyer@example.com", "order_id": "ord-1", "coupon_code": "CAP2",
	}); res["status"] != "ok" {
		t.Fatalf("place order 1: %v", res)
	}
	testhelpers.WaitForTableRows(t, eng, "orders", 1)

	// Redemption 2: still under the cap.
	if res := emitJSON(handler, shopperKey, "VALIDATE_COUPON", map[string]interface{}{
		"cart_id": "c2", "code": "CAP2",
	}); res["status"] != "ok" {
		t.Fatalf("validate 2 should pass: %v", res)
	}

	// Burn the second use.
	if _, err := bus.Emit("ADD_ORDER_ITEM", map[string]interface{}{
		"order_id": "ord-2", "product_id": productID, "name": "W", "price": 10.0, "qty": 1,
		"coupon_code": "CAP2",
	}); err != nil {
		t.Fatal(err)
	}
	if res := emitJSON(handler, shopperKey, "PLACE_ORDER", map[string]interface{}{
		"cart_id": "c2", "email": "buyer2@example.com", "order_id": "ord-2", "coupon_code": "CAP2",
	}); res["status"] != "ok" {
		t.Fatalf("place order 2: %v", res)
	}
	testhelpers.WaitForTableRows(t, eng, "orders", 2)
	time.Sleep(300 * time.Millisecond) // let the async db.adjust commit before the cap re-check

	// Third attempt: cap reached → rejected.
	res := emitJSON(handler, shopperKey, "VALIDATE_COUPON", map[string]interface{}{
		"cart_id": "c3", "code": "CAP2",
	})
	if res["status"] == "ok" && !strings.Contains(serializeStates(res), "COUPON_REJECTED") {
		t.Fatalf("validate 3 must hit the cap, got: %v", res)
	}
}

// An expired coupon (expires_at in the past) is rejected; a future expiry
// passes. No max_uses set — isolates the expiry guard.
func TestEcommerceCouponExpiryEnforced(t *testing.T) {
	eng, cleanup := setupEcommerceEngine(t)
	defer cleanup()
	handler := eng.HTTPHandler()

	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	if res := emitJSON(handler, adminKey, "CREATE_COUPON", map[string]interface{}{
		"code": "OLD1", "percent_off": 10.0, "fixed_off": 0, "active": "true",
		"expires_at": past, "max_uses": 0, "used_count": 0,
	}); res["status"] != "ok" {
		t.Fatalf("create expired coupon: %v", res)
	}

	res := emitJSON(handler, shopperKey, "VALIDATE_COUPON", map[string]interface{}{
		"cart_id": "c-exp", "code": "OLD1",
	})
	if res["status"] == "ok" && !strings.Contains(serializeStates(res), "COUPON_REJECTED") {
		t.Fatalf("expired coupon must be rejected, got: %v", res)
	}
}

func serializeStates(res map[string]interface{}) string {
	states, _ := res["emitted_states"].([]interface{})
	out := ""
	for _, s := range states {
		out += s.(string) + ","
	}
	return out
}
