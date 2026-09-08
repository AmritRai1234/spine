package engine

import (
	"strings"
	"testing"
)

// sweepNumeric is the strict numeric-coercion helper for sweep/fanout row
// scans. It replaces the old `_, _ = strconv.ParseX` pattern that silently
// coerced garbage to zero — a corrupted row becoming a $0 subscription or a
// 0-month interval with no trace anywhere.
func TestSweepNumericRejectsGarbage(t *testing.T) {
	cases := []struct {
		name    string
		v       interface{}
		wantErr string
	}{
		{"null", nil, "NULL"},
		{"empty string", "", "empty/null"},
		{"garbage text", "19.99abc", "not numeric"},
		{"expression injection", "0+9999", "not numeric"},
		{"bool", true, "unsupported type bool"},
		{"map", map[string]interface{}{"a": 1}, "unsupported type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sweepNumeric("unit_price", "row-1", tc.v)
			if err == nil {
				t.Fatalf("expected error for %v, got nil", tc.v)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q must mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSweepNumericAcceptsValidForms(t *testing.T) {
	cases := []struct {
		name string
		v    interface{}
		want float64
	}{
		{"float64", float64(19.99), 19.99},
		{"int64", int64(3), 3},
		{"int", 3, 3},
		{"numeric string", "19.99", 19.99},
		{"integer string with spaces", " 3 ", 3},
		{"bytes", []byte("2.5"), 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sweepNumeric("unit_price", "row-1", tc.v)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
