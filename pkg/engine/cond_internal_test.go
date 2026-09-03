package engine

import "testing"

// The `matches` operator is the engine primitive behind server-side email
// format guards (app.spine asserts). If these regress, malformed emails
// bypass the API again — so the guard cases live here permanently.
func TestEvaluateConditionMatches(t *testing.T) {
	// Email pattern identical to the one asserted in app.spine routes —
	// same semantics as web/src/lib/validate.ts (every domain label ≥2 chars).
	const emailRe = `^[A-Za-z0-9._%+-]+@[A-Za-z0-9][A-Za-z0-9-]*[A-Za-z0-9](?:\.[A-Za-z0-9][A-Za-z0-9-]*[A-Za-z0-9])+$`

	payload := map[string]interface{}{
		"email": "jane.doe+news@shop.co",
	}

	if !EvaluateCondition("$event.payload.email matches '"+emailRe+"'", "E", payload) {
		t.Fatal("valid email must match the format guard")
	}
	if EvaluateCondition("$event.payload.email matches '"+emailRe+"'", "E", map[string]interface{}{"email": "test@test"}) {
		t.Fatal("TLD-less address (test@test) must fail the format guard")
	}
	if EvaluateCondition("$event.payload.email matches '"+emailRe+"'", "E", map[string]interface{}{"email": "foo bar@x.com"}) {
		t.Fatal("space in local part must fail the format guard")
	}
	if EvaluateCondition("$event.payload.email matches '"+emailRe+"'", "E", map[string]interface{}{"email": "a@@b.com"}) {
		t.Fatal("double @ must fail the format guard")
	}
	if EvaluateCondition("$event.payload.email matches '"+emailRe+"'", "E", map[string]interface{}{"email": "jane@x.c"}) {
		t.Fatal("one-char TLD must fail the format guard")
	}
	// Unanchored by design: a substring pattern matches inside a longer value.
	if !EvaluateCondition("$event.payload.email matches '@shop'", "E", payload) {
		t.Fatal("matches must be substring (RE2) unless the pattern anchors itself")
	}
	// Invalid pattern is a manifest bug: clause fails loudly, never true.
	if EvaluateCondition("$event.payload.email matches '([unclosed'", "E", payload) {
		t.Fatal("invalid regex pattern must evaluate to false")
	}
}