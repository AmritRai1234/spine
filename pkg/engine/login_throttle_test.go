package engine

import (
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func newTestThrottler() *LoginThrottler {
	return &LoginThrottler{entries: map[string]*loginAttempt{}, now: time.Now}
}

func TestLoginThrottleAllowsUntilThreshold(t *testing.T) {
	tr := newTestThrottler()
	for i := 0; i < loginMaxFails-1; i++ {
		tr.RecordFailure("a@x.com", "1.2.3.4")
		if ok, _ := tr.Check("a@x.com", "1.2.3.4"); !ok {
			t.Fatalf("locked after only %d failures", i+1)
		}
	}
	tr.RecordFailure("a@x.com", "1.2.3.4") // 5th
	if ok, retry := tr.Check("a@x.com", "1.2.3.4"); ok {
		t.Fatal("not locked after 5 failures")
	} else if retry <= 0 {
		t.Fatalf("retryAfter not positive: %v", retry)
	}
}

func TestLoginThrottlePerKeyIsolation(t *testing.T) {
	tr := newTestThrottler()
	// Fail 5x for one email — a different email (same IP) unaffected.
	for i := 0; i < loginMaxFails; i++ {
		tr.RecordFailure("victim@x.com", "9.9.9.9")
	}
	if ok, _ := tr.Check("other@x.com", "9.9.9.9"); !ok {
		t.Fatal("different email same IP got locked")
	}
	if ok, _ := tr.Check("victim@x.com", "8.8.8.8"); !ok {
		t.Fatal("same email different IP got locked")
	}
	// IP-only spray doesn't lock unrelated email+IP pairs (email is in the key).
	if ok, _ := tr.Check("victim@x.com", "9.9.9.9"); ok {
		t.Fatal("exact key not locked")
	}
}

func TestLoginThrottleSuccessResets(t *testing.T) {
	tr := newTestThrottler()
	for i := 0; i < loginMaxFails-1; i++ {
		tr.RecordFailure("b@x.com", "1.1.1.1")
	}
	tr.RecordSuccess("b@x.com", "1.1.1.1")
	// Fresh slate: full budget available again.
	for i := 0; i < loginMaxFails-1; i++ {
		tr.RecordFailure("b@x.com", "1.1.1.1")
	}
	if ok, _ := tr.Check("b@x.com", "1.1.1.1"); !ok {
		t.Fatal("success did not reset the counter")
	}
}

func TestLoginThrottleLockoutExpiresAndSweeps(t *testing.T) {
	tr := newTestThrottler()
	now := time.Now()
	tr.now = func() time.Time { return now }
	for i := 0; i < loginMaxFails; i++ {
		tr.RecordFailure("c@x.com", "2.2.2.2")
	}
	if ok, _ := tr.Check("c@x.com", "2.2.2.2"); ok {
		t.Fatal("expected lockout")
	}
	now = now.Add(loginLockout + time.Minute)
	if ok, _ := tr.Check("c@x.com", "2.2.2.2"); !ok {
		t.Fatal("lockout did not expire")
	}
	tr.mu.Lock()
	n := len(tr.entries)
	tr.mu.Unlock()
	if n != 0 {
		t.Fatalf("sweep left %d entries behind", n)
	}
}

func TestLoginThrottleProgressiveLockout(t *testing.T) {
	tr := newTestThrottler()
	now := time.Now()
	tr.now = func() time.Time { return now }
	for i := 0; i < loginMaxFails; i++ {
		tr.RecordFailure("d@x.com", "3.3.3.3")
	}
	_, first := tr.Check("d@x.com", "3.3.3.3")
	// Fail while locked — retrying during lockout must not reset it shorter.
	tr.RecordFailure("d@x.com", "3.3.3.3")
	_, second := tr.Check("d@x.com", "3.3.3.3")
	if second <= first {
		t.Fatalf("progressive lockout not applied: first=%v second=%v", first, second)
	}
}

func TestLoginActionThrottlesEndToEnd(t *testing.T) {
	_, bus := testUserStore(t)

	reg := &manifest.RouteStep{Action: "auth.register", Config: map[string]string{
		"email": "$event.payload.email", "password": "$event.payload.password",
	}}
	login := &manifest.RouteStep{Action: "auth.login", Config: map[string]string{
		"email": "$event.payload.email", "password": "$event.payload.password",
	}}

	// Register with an IP stamp present (as /emit would).
	regPayload := map[string]interface{}{"email": "throttle@x.com", "password": "correct-horse-1", "_login_ip": "5.5.5.5"}
	if err := bus.userRegister(reg, "REGISTER", regPayload); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 5 bad logins from the same email+IP.
	for i := 0; i < loginMaxFails; i++ {
		p := map[string]interface{}{"email": "throttle@x.com", "password": "wrong-pass-999", "_login_ip": "5.5.5.5"}
		if err := bus.userLogin(login, "LOGIN", p); err != nil {
			t.Fatalf("bad login %d: %v", i, err)
		}
		if p["auth_key"] != false {
			t.Fatalf("bad login %d did not soft-fail", i)
		}
	}
	// 6th attempt — even with the CORRECT password — must be locked out.
	good := map[string]interface{}{"email": "throttle@x.com", "password": "correct-horse-1", "_login_ip": "5.5.5.5"}
	if err := bus.userLogin(login, "LOGIN", good); err != nil {
		t.Fatalf("locked login errored: %v", err)
	}
	if good["auth_key"] != false {
		t.Fatal("lockout did not block correct password")
	}
	if _, ok := good["auth_key_retry_after_s"]; !ok {
		t.Fatal("retry_after not surfaced")
	}
	// IP stamped key was stripped from the payload.
	if _, has := good["_login_ip"]; has {
		t.Fatal("_login_ip not stripped from payload")
	}
	// A different IP can still log in.
	otherIP := map[string]interface{}{"email": "throttle@x.com", "password": "correct-horse-1", "_login_ip": "6.6.6.6"}
	if err := bus.userLogin(login, "LOGIN", otherIP); err != nil {
		t.Fatalf("other-IP login errored: %v", err)
	}
	if s, _ := otherIP["auth_key"].(string); len(s) < 32 {
		t.Fatalf("other-IP login failed: %v", otherIP["auth_key"])
	}
}
