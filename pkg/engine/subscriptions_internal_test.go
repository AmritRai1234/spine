package engine

import (
	"testing"
	"time"
)

func TestSubscriptionsRollMonthly(t *testing.T) {
	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	got := rollMonthly(base, 1)
	want := base.AddDate(0, 1, 0)
	if !got.Equal(want) {
		t.Fatalf("rollMonthly 1 = %v, want %v", got, want)
	}
	got = rollMonthly(base, 6)
	if got.Month() != time.February || got.Year() != 2027 {
		t.Fatalf("rollMonthly 6 = %v, want Feb 2027", got)
	}
}

func TestSplitTopLevelCondition(t *testing.T) {
	payload := map[string]interface{}{
		"variant_id":     "",
		"purchase_mode":  "subscription",
		"sub_prod_flag":  float64(1),
	}
	// && with a false clause must be false
	if EvaluateCondition("$event.payload.variant_id == '' && $event.payload.purchase_mode == 'onetime'", "E", payload) {
		t.Fatal("AND clause with false second operand must be false")
	}
	if !EvaluateCondition("$event.payload.variant_id == '' && $event.payload.purchase_mode == 'subscription'", "E", payload) {
		t.Fatal("AND of two true clauses must be true")
	}
	if !EvaluateCondition("$event.payload.purchase_mode == 'onetime' || $event.payload.sub_prod_flag == 1", "E", payload) {
		t.Fatal("OR with one true clause must be true")
	}
	// quoted pipes/ampersands never split
	if EvaluateCondition("$event.payload.purchase_mode == 'a||b'", "E", map[string]interface{}{}) {
		t.Fatal("quoted separator must not split")
	}
}
