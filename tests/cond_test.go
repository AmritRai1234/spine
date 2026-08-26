package tests

import (
	"testing"

	"github.com/AmritRai1234/spine/pkg/engine"
)

func TestEvaluateCondition(t *testing.T) {
	payload := map[string]interface{}{
		"role":   "admin",
		"age":    25,
		"email":  "alex@example.com",
		"status": "active",
		"title":  "big > small",
	}

	tests := []struct {
		cond     string
		expected bool
	}{
		{"", true},
		{"$event.payload.role == 'admin'", true},
		{"$event.payload.role == 'user'", false},
		{"$event.payload.role != 'user'", true},
		{"$event.payload.email contains 'example.com'", true},
		{"$event.payload.email contains 'gmail.com'", false},
		{"$event.payload.age > 18", true},
		{"$event.payload.age < 18", false},
		{"$event.payload.age >= 25", true},
		{"$event.payload.age <= 20", false},
		{"$event.payload.status exists", true},
		{"$event.payload.nonexistent exists", false},

		// equalsValue numeric coercion edge cases
		{"15.0 == 15", true},    // decimal + integer — equal floats
		{"007 == 7", false},     // leading zeros — string compare
		{"1e3 == 1000", false},  // scientific notation — string compare
		{" 42  == 42", true},    // whitespace-trimmed — equal floats
		{"abc == abc", true},    // same non-numeric string
		{"abc == 1", false},     // non-numeric vs number — not equal
		{"-2.50 == -2.5", true}, // negative trailing zero — equal floats
		{"007 != 7", true},      // leading zeros != — not equal
		{"15.0 != 7", true},     // decimal != int — different numbers

		// P3-4: single '=' is an alias for '=='
		{"$event.payload.status = active", true},
		{"$event.payload.status = 'inactive'", false},
		{"$event.payload.age = 25", true},

		// P3-4: operators inside quoted values must not split the condition
		{"$event.payload.title contains 'big > small'", true},
		{"$event.payload.title contains 'small > big'", false},

		// P3-4: numeric comparisons between non-numeric operands are FALSE
		// (no silent lexicographic fallback), unless both sides are quoted.
		{"$event.payload.status > 'active'", false},
		{"'abc' > '100'", true}, // quoted both sides — explicit string compare
		{"'100' < 'abc'", true},
	}

	for _, tt := range tests {
		got := engine.EvaluateCondition(tt.cond, "TEST_EVENT", payload)
		if got != tt.expected {
			t.Errorf("EvaluateCondition(%q) = %v; want %v", tt.cond, got, tt.expected)
		}
	}
}
