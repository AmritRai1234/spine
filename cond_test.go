package spine

import (
	"testing"
)

func TestEvaluateCondition(t *testing.T) {
	payload := map[string]interface{}{
		"role":   "admin",
		"age":    25,
		"email":  "alex@example.com",
		"status": "active",
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
	}

	for _, tt := range tests {
		got := EvaluateCondition(tt.cond, "TEST_EVENT", payload)
		if got != tt.expected {
			t.Errorf("EvaluateCondition(%q) = %v; want %v", tt.cond, got, tt.expected)
		}
	}
}
