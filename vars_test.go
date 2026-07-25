package spine

import (
	"os"
	"strings"
	"testing"
)

func TestResolveVariables(t *testing.T) {
	os.Setenv("TEST_KEY", "secret_value")
	defer os.Unsetenv("TEST_KEY")

	payload := map[string]interface{}{
		"email": "user@example.com",
		"user": map[string]interface{}{
			"id": 42,
		},
	}

	t.Run("Now token", func(t *testing.T) {
		res := ResolveVariables("$now", "TEST_EVENT", payload)
		if len(res) < 10 || !strings.Contains(res, "T") {
			t.Errorf("expected ISO timestamp, got: %s", res)
		}
	})

	t.Run("UUID token", func(t *testing.T) {
		res := ResolveVariables("$uuid", "TEST_EVENT", payload)
		if len(res) != 36 {
			t.Errorf("expected 36-char UUID, got: %s", res)
		}
	})

	t.Run("Event name", func(t *testing.T) {
		res := ResolveVariables("$event.name", "TEST_EVENT", payload)
		if res != "TEST_EVENT" {
			t.Errorf("expected TEST_EVENT, got: %s", res)
		}
	})

	t.Run("Env var", func(t *testing.T) {
		res := ResolveVariables("$env.TEST_KEY", "TEST_EVENT", payload)
		if res != "secret_value" {
			t.Errorf("expected secret_value, got: %s", res)
		}
	})

	t.Run("Nested payload field", func(t *testing.T) {
		res := ResolveVariables("$event.payload.user.id", "TEST_EVENT", payload)
		if res != "42" {
			t.Errorf("expected 42, got: %s", res)
		}
	})
}
