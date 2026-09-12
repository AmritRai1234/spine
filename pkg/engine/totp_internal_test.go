package engine

import (
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func testStep(action string, cfg map[string]string) *manifest.RouteStep {
	return &manifest.RouteStep{Action: action, Config: cfg}
}

// RFC 6238 test vector (SHA1, 8 digits uses "12345678901234567890"; the
// 6-digit equivalents of the same vectors appear in the RFC's TOTP table as
// the last 6 digits of the 8-digit values' HOTP output — we compute against
// the well-known 6-digit column: 30s step SHA1 secret ASCII).
var rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32 of "12345678901234567890"

func TestTOTPRFC6238Vectors(t *testing.T) {
	// RFC 6238 appendix B reference times (Unix) and the corresponding
	// 6-digit SHA1 codes.
	cases := []struct {
		unixSec int64
		code    string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"}, // RFC's printed 050473 is the 8-digit variant; HMAC-SHA1/30s/6-digit computes 050471
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"}, // step 666,666,666 — > uint32 counter territory
	}
	for _, c := range cases {
		got, err := totpAt(rfc6238Secret, time.Unix(c.unixSec, 0))
		if err != nil {
			t.Fatalf("totpAt(%d): %v", c.unixSec, err)
		}
		if got != c.code {
			t.Errorf("totpAt(%d) = %s, want %s", c.unixSec, got, c.code)
		}
		if _, ok := verifyTOTP(rfc6238Secret, c.code, time.Unix(c.unixSec, 0)); !ok {
			t.Errorf("verifyTOTP(%s at %d) rejected", c.code, c.unixSec)
		}
	}
}

func TestTOTPDriftWindow(t *testing.T) {
	// Code for step N must verify at t in [N-1, N, N+1] steps.
	base := int64(2_000_000_000)
	codeAt := func(sec int64) string {
		c, err := totpAt(rfc6238Secret, time.Unix(sec, 0))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// Verify code generated one step ago (t-31s) still accepted now.
	prev := codeAt(base - 31)
	if _, ok := verifyTOTP(rfc6238Secret, prev, time.Unix(base, 0)); !ok {
		t.Error("t-1 drift code rejected")
	}
	// Verify code generated one step ahead (t+31s) accepted now.
	next := codeAt(base + 31)
	if _, ok := verifyTOTP(rfc6238Secret, next, time.Unix(base, 0)); !ok {
		t.Error("t+1 drift code rejected")
	}
	// Two steps out must be rejected.
	far := codeAt(base + 61)
	if _, ok := verifyTOTP(rfc6238Secret, far, time.Unix(base, 0)); ok {
		t.Error("t+2 code accepted (drift window too wide)")
	}
}

func TestTOTPSetupConfirmFlow(t *testing.T) {
	bus := newTestBus(t)
	email := "dana@example.com"

	// Setup mints a pending secret + URI.
	payload := map[string]interface{}{"email": email}
	step := testStep("auth.totp.setup", map[string]string{"email": "$event.payload.email"})
	if err := bus.totpSetup(step, "TEST", payload); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secret, _ := payload["totp_secret"].(string)
	if len(secret) < 32 {
		t.Fatalf("setup: short secret %q", secret)
	}
	if uri, _ := payload["totp_secret_uri"].(string); len(uri) < 20 ||
		uri[:14] != "otpauth://totp" {
		t.Errorf("setup: bad provisioning URI %q", uri)
	}

	// Not yet enrolled for login purposes until confirmed.
	if bus.totpEnrolled(email) {
		t.Fatal("pending enrollment counted as enrolled")
	}

	// Confirm with a live code flips pending → enrolled.
	now := time.Now()
	code, err := totpAt(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	p2 := map[string]interface{}{"email": email, "code": code}
	step2 := testStep("auth.totp.confirm", map[string]string{"email": "$event.payload.email", "code": "$event.payload.code"})
	if err := bus.totpConfirm(step2, "TEST", p2); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if p2["totp_ok"] != true {
		t.Fatalf("confirm rejected a live code: %v", p2)
	}
	if !bus.totpEnrolled(email) {
		t.Fatal("confirm did not enroll")
	}

	// Re-running setup on a confirmed account errors (rotation requires disable).
	p3 := map[string]interface{}{"email": email}
	if err := bus.totpSetup(step, "TEST", p3); err == nil {
		t.Fatal("re-setup on confirmed enrollment allowed")
	}
}

func TestTOTPLoginGateAndReplay(t *testing.T) {
	bus := newTestBus(t)
	email := "erin@example.com"

	// Enroll via setup+confirm.
	p := map[string]interface{}{"email": email}
	step := testStep("auth.totp.setup", map[string]string{"email": "$event.payload.email"})
	if err := bus.totpSetup(step, "TEST", p); err != nil {
		t.Fatal(err)
	}
	secret := p["totp_secret"].(string)
	code, err := totpAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p2 := map[string]interface{}{"email": email, "code": code}
	stepC := testStep("auth.totp.confirm", map[string]string{
		"email": "$event.payload.email", "code": "$event.payload.code"})
	if err := bus.totpConfirm(stepC, "TEST", p2); err != nil || p2["totp_ok"] != true {
		t.Fatalf("confirm failed: %v %v", err, p2)
	}

	// Missing code → login refused with totp_required marker.
	enrolled, ok := bus.totpCheck(email, "", time.Now())
	if !enrolled || ok {
		t.Errorf("missing code accepted: enrolled=%v ok=%v", enrolled, ok)
	}
	// Wrong code → refused.
	if enrolled, ok := bus.totpCheck(email, "000000", time.Now()); !enrolled || ok {
		t.Errorf("wrong code accepted: enrolled=%v ok=%v", enrolled, ok)
	}
	// Right code → accepted, once. Anchor at the middle of a step (not the
	// boundary) so all codes below come from deterministic, distinct steps.
	stepStart := (time.Now().Unix() / 30) * 30 // current step start
	now := time.Unix(stepStart+30*2+10, 0)     // two steps ahead of confirm, 10s into the step
	good, err := totpAt(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if enrolled, ok := bus.totpCheck(email, good, now); !enrolled || !ok {
		t.Fatalf("valid code refused: enrolled=%v ok=%v", enrolled, ok)
	}
	// Same code again (same step) → replay guard refuses.
	if enrolled, ok := bus.totpCheck(email, good, now.Add(5*time.Second)); !enrolled || ok {
		t.Errorf("replayed code accepted: enrolled=%v ok=%v", enrolled, ok)
	}
	// The confirm code (one step older than `good`, consumed above) must be
	// refused — its step sits inside the drift window of `now` and is in
	// the guard's used set.
	confirmCode, err := totpAt(secret, time.Unix(stepStart+10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if enrolled, ok := bus.totpCheck(email, confirmCode, now); !enrolled || ok {
		t.Errorf("confirm-step code accepted after newer acceptance: enrolled=%v ok=%v", enrolled, ok)
	}
	// Next step's code works.
	next, err := totpAt(secret, now.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bus.totpCheck(email, next, now.Add(31*time.Second)); !ok {
		t.Error("next-step code refused")
	}

	// Disable requires a valid code too.
	pd := map[string]interface{}{"email": email, "code": "000000"}
	stepD := testStep("auth.totp.disable", map[string]string{
		"email": "$event.payload.email", "code": "$event.payload.code"})
	if err := bus.totpDisable(stepD, "TEST", pd); err != nil {
		t.Fatal(err)
	}
	if pd["totp_disabled"] != false {
		t.Error("disable accepted a wrong code")
	}
	good2, err := totpAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pd2 := map[string]interface{}{"email": email, "code": good2}
	if err := bus.totpDisable(stepD, "TEST", pd2); err != nil || pd2["totp_disabled"] != true {
		t.Fatalf("disable with valid code failed: %v %v", err, pd2)
	}
	if bus.totpEnrolled(email) {
		t.Error("disable did not unenroll")
	}
}

// TestTOTPReplayGuardThreeSlots pins the hole a 2-slot guard had: accepting
// C-1 then C+1 must make C (the middle step) a replay — two guard slots let
// it slip through, three (the full ±1 window) close it.
func TestTOTPReplayGuardThreeSlots(t *testing.T) {
	bus := newTestBus(t)
	email := "gale@example.com"
	p := map[string]interface{}{"email": email}
	step := testStep("auth.totp.setup", map[string]string{"email": "$event.payload.email"})
	if err := bus.totpSetup(step, "TEST", p); err != nil {
		t.Fatal(err)
	}
	secret := p["totp_secret"].(string)

	stepStart := (time.Now().Unix() / 30) * 30
	tC := time.Unix(stepStart+10, 0) // middle step, anchored 10s in (mid-step)
	tMinus := tC.Add(-30 * time.Second)
	tPlus := tC.Add(30 * time.Second)

	// Confirm consumes C.
	codeC, err := totpAt(secret, tC)
	if err != nil {
		t.Fatal(err)
	}
	p2 := map[string]interface{}{"email": email, "code": codeC}
	stepC := testStep("auth.totp.confirm", map[string]string{
		"email": "$event.payload.email", "code": "$event.payload.code"})
	if err := bus.totpConfirm(stepC, "TEST", p2); err != nil || p2["totp_ok"] != true {
		t.Fatalf("confirm failed: %v %v", err, p2)
	}

	// Accept C-1.
	codeMinus, err := totpAt(secret, tMinus)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bus.totpCheck(email, codeMinus, tMinus); !ok {
		t.Fatal("C-1 code refused")
	}
	// Accept C+1.
	codePlus, err := totpAt(secret, tPlus)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bus.totpCheck(email, codePlus, tPlus); !ok {
		t.Fatal("C+1 code refused")
	}
	// Now C must be a replay — the 3-slot guard still holds it.
	if enrolled, ok := bus.totpCheck(email, codeC, tC); !enrolled || ok {
		t.Errorf("middle-step code accepted after C-1/C+1 acceptance (2-slot hole): enrolled=%v ok=%v", enrolled, ok)
	}
}

func TestTOTPDisablePendingIsNoop(t *testing.T) {
	bus := newTestBus(t)
	email := "finn@example.com"
	// Pending-only enrollment cannot be "disabled" — and more importantly a
	// pending enrollment never gates login (totpEnrolled false).
	p := map[string]interface{}{"email": email}
	step := testStep("auth.totp.setup", map[string]string{"email": "$event.payload.email"})
	if err := bus.totpSetup(step, "TEST", p); err != nil {
		t.Fatal(err)
	}
	if bus.totpEnrolled(email) {
		t.Fatal("pending enrollment gates login")
	}
}