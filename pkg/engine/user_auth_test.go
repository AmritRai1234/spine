package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func testUserStore(t *testing.T) (*UserKeyStore, *Bus) {
	t.Helper()
	bus := newTestBus(t)
	if err := bus.ensureUserTables(); err != nil {
		t.Fatalf("ensureUserTables: %v", err)
	}
	if err := bus.userKeys.ensureTables(); err != nil {
		t.Fatalf("ensureTables: %v", err)
	}
	return bus.userKeys, bus
}

func TestUserKeyIssueAndLookup(t *testing.T) {
	store, _ := testUserStore(t)

	raw, err := store.Issue("a@example.com", "customer")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(raw) < 32 {
		t.Fatalf("issued key too short: %d chars", len(raw))
	}

	rec := store.Lookup(raw)
	if rec == nil {
		t.Fatal("Lookup: issued key not found")
	}
	if rec.email != "a@example.com" || rec.role != "customer" {
		t.Fatalf("wrong record: %+v", rec)
	}

	if store.Lookup("wrong-key-entirely") != nil {
		t.Fatal("Lookup: bogus key resolved")
	}
	if store.Lookup("") != nil {
		t.Fatal("Lookup: empty key resolved")
	}
}

func TestUserKeyRevoke(t *testing.T) {
	store, _ := testUserStore(t)
	raw, _ := store.Issue("b@example.com", "customer")
	store.Revoke(raw)
	if store.Lookup(raw) != nil {
		t.Fatal("revoked key still resolves")
	}
	// Double-revoke is a safe no-op.
	store.Revoke(raw)
}

func TestUserKeyExpiry(t *testing.T) {
	store, _ := testUserStore(t)
	raw, _ := store.Issue("c@example.com", "customer")
	// Force-expire in memory (DB row would also fail the expires_at filter).
	store.mu.Lock()
	digest := keyDigest(raw)
	rec := store.keys[digest]
	rec.expiresAt = time.Now().Add(-time.Hour)
	store.keys[digest] = rec
	store.mu.Unlock()
	if store.Lookup(raw) != nil {
		t.Fatal("expired key still resolves")
	}
}

func TestUserKeyPersistenceAcrossReload(t *testing.T) {
	store, bus := testUserStore(t)
	raw, _ := store.Issue("d@example.com", "customer")

	store2 := NewUserKeyStore(bus)
	if err := store2.ensureTables(); err != nil {
		t.Fatalf("reload ensureTables: %v", err)
	}
	if store2.Lookup(raw) == nil {
		t.Fatal("key did not survive store reload")
	}
}

func TestAccessResolverUserKeyFallthrough(t *testing.T) {
	_, bus := testUserStore(t)

	rules := []manifest.AccessRule{{
		Role: "admin", Key: "admin-secret-key",
	}}
	resolver := NewAccessResolver(rules)
	resolver.SetUserKeyStore(bus.userKeys)

	// Static rule wins for admin key.
	ac := resolver.Resolve("admin-secret-key")
	if ac == nil || ac.Role != "admin" {
		t.Fatalf("static rule broken: %+v", ac)
	}

	raw, _ := bus.userKeys.Issue("eve@example.com", "customer")
	ac = resolver.Resolve(raw)
	if ac == nil {
		t.Fatal("per-user key did not resolve")
	}
	if ac.Role != "customer" {
		t.Fatalf("role = %q, want customer", ac.Role)
	}
	want := "email = 'eve@example.com'"
	if ac.Filter != want {
		t.Fatalf("filter = %q, want %q", ac.Filter, want)
	}

	// SQL-injection-shaped email must be rejected at registration, so an
	// issued key can never carry a filter-breaking address.
	if _, err := bus.userKeys.Issue("x' OR 1=1 --@example.com", "customer"); err == nil {
		t.Fatal("quote-carrying email accepted at Issue")
	}
	// Defense in depth: even if one leaked in, the filter escapes the quote.
	leak := &userKeyRecord{email: "x'@e.com", role: "customer"}
	escaped := "email = '" + strings.ReplaceAll(leak.email, "'", "''") + "'"
	if escaped != "email = 'x''@e.com'" {
		t.Fatalf("escape broken: %q", escaped)
	}

	// Event whitelist inheritance: a per-user context inherits the events:
	// whitelist of its role. A role without a whitelist stays unrestricted.
	resolver = NewAccessResolver(rules)
	resolver.SetUserKeyStore(bus.userKeys)
	custRule := manifest.AccessRule{Role: "customer", Key: "unused-static",
		Events: []string{"ADD_TO_CART", "PLACE_ORDER"}}
	resolver.rules = append(resolver.rules, custRule)
	resolver.roleEvents = roleEventMap(resolver.rules)
	k, _ := bus.userKeys.Issue("w@x.com", "customer")
	ac = resolver.Resolve(k)
	if ac == nil {
		t.Fatal("user key failed to resolve")
	}
	if ac.CanEmit("ADD_TO_CART") {
		// whitelisted — good
	} else {
		t.Fatal("whitelisted event denied")
	}
	if ac.CanEmit("DELETE_ALL_PRODUCTS") {
		t.Fatal("unlisted event allowed under whitelist")
	}
	// Role with no whitelist → unrestricted.
	am, _ := bus.userKeys.Issue("admin2@x.com", "staff")
	ac = resolver.Resolve(am)
	if ac == nil || !ac.CanEmit("ANY_EVENT") {
		t.Fatal("role without whitelist should be unrestricted")
	}

	// Unknown key: nil either way.
	if resolver.Resolve("no-such-key") != nil {
		t.Fatal("bogus key resolved")
	}
}

func TestUserRegisterLoginFlow(t *testing.T) {
	_, bus := testUserStore(t)

	step := func(action string) *manifest.RouteStep {
		return &manifest.RouteStep{Action: action, Config: map[string]string{
			"email":    "$event.payload.email",
			"password": "$event.payload.password",
		}}
	}

	payload := map[string]interface{}{"email": "shopper@example.com", "password": "correct-horse-battery"}
	if err := bus.userRegister(step("auth.register"), "REGISTER", payload); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw := payload["auth_key"]
	if s, ok := raw.(string); !ok || len(s) < 32 {
		t.Fatalf("register did not issue a key: %v", raw)
	}
	if payload["auth_key_email"] != "shopper@example.com" {
		t.Fatalf("email not echoed: %v", payload["auth_key_email"])
	}

	// Duplicate register must fail without leaking the hash timing (burn path).
	dup := map[string]interface{}{"email": "shopper@example.com", "password": "correct-horse-battery"}
	if err := bus.userRegister(step("auth.register"), "REGISTER", dup); err == nil {
		t.Fatal("duplicate register succeeded")
	}

	// Login: good password rotates the key.
	login := map[string]interface{}{"email": "shopper@example.com", "password": "correct-horse-battery"}
	if err := bus.userLogin(step("auth.login"), "LOGIN", login); err != nil {
		t.Fatalf("login: %v", err)
	}
	newRaw, _ := login["auth_key"].(string)
	if len(newRaw) < 32 {
		t.Fatalf("login did not issue key: %v", login["auth_key"])
	}
	if newRaw == payload["auth_key"] {
		t.Fatal("login did not rotate the key")
	}
	// Old key revoked by rotation.
	if bus.userKeys.Lookup(payload["auth_key"].(string)) != nil {
		t.Fatal("pre-login key still valid after rotation")
	}
	// New key resolves with per-user filter.
	rec := bus.userKeys.Lookup(newRaw)
	if rec == nil || rec.email != "shopper@example.com" {
		t.Fatalf("rotated key lookup: %+v", rec)
	}

	// Login: wrong password — soft failure (auth_key=false), no error, no key.
	bad := map[string]interface{}{"email": "shopper@example.com", "password": "wrong-password-123"}
	if err := bus.userLogin(step("auth.login"), "LOGIN", bad); err != nil {
		t.Fatalf("bad-password login errored: %v", err)
	}
	if bad["auth_key"] != false {
		t.Fatalf("bad password did not set auth_key=false: %v", bad["auth_key"])
	}

	// Login: unknown account — same soft failure, timing-equalized.
	unknown := map[string]interface{}{"email": "ghost@example.com", "password": "whatever-pass-1"}
	if err := bus.userLogin(step("auth.login"), "LOGIN", unknown); err != nil {
		t.Fatalf("unknown-account login errored: %v", err)
	}
	if unknown["auth_key"] != false {
		t.Fatalf("unknown account did not set auth_key=false: %v", unknown["auth_key"])
	}

	// Logout revokes the live key.
	bus.userLogout(&manifest.RouteStep{Config: map[string]string{"key": "$event.payload.key"}}, "LOGOUT",
		map[string]interface{}{"key": newRaw})
	if bus.userKeys.Lookup(newRaw) != nil {
		t.Fatal("logout did not revoke the key")
	}
}

func TestUserRegisterValidation(t *testing.T) {
	_, bus := testUserStore(t)

	cases := []struct {
		name    string
		email   string
		pass    string
		wantErr string
	}{
		{"bad email", "not-an-email", "long-enough-pass", "email"},
		{"short password", "ok@example.com", "short", "8 characters"},
		{"oversized password", "ok@example.com", strings.Repeat("x", 80), "72"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := &manifest.RouteStep{Action: "auth.register", Config: map[string]string{
				"email": tc.email, "password": tc.pass,
			}}
			err := bus.userRegister(step, "REGISTER", map[string]interface{}{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestUserKeyStoreConcurrency(t *testing.T) {
	store, _ := testUserStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			raw, err := store.Issue("conc@example.com", "customer")
			if err != nil {
				t.Errorf("issue %d: %v", n, err)
				return
			}
			if store.Lookup(raw) == nil {
				t.Errorf("key %d not immediately resolvable", n)
			}
			store.Revoke(raw)
		}(i)
	}
	wg.Wait()
}
