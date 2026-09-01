package manifest

import (
	"os"
	"strings"
	"testing"
)

// Manifest option validation: typo'd action names and typo'd per-action
// config keys must fail at parse time with did-you-mean suggestions.
// Before 2026-09-01 both were silently accepted: a typo'd action dispatched
// to the engine's default case and returned nil (step did nothing, route
// "succeeded"), and a typo'd config key (keyy: under db.upsert) was stored
// in Config and ignored while the real option stayed unset.

func writeAndParse(t *testing.T, body string) error {
	t.Helper()
	path := "/tmp/optval.spine"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseManifest(path)
	return err
}

func TestUnknownActionTypoRejected(t *testing.T) {
	err := writeAndParse(t, `spine_version: 3

nodes:
  A:
    emits:
      - event: E1

routes:
  - on: E1
    steps:
      - action: db.upsertt
        table: bookings
`)
	if err == nil {
		t.Fatal("typo'd action db.upsertt must be rejected at parse time")
	}
	if !strings.Contains(err.Error(), "db.upsert") || !strings.Contains(err.Error(), "Did you mean") {
		t.Fatalf("rejection should carry a did-you-mean suggestion, got: %v", err)
	}
}

func TestUnknownConfigKeyRejected(t *testing.T) {
	err := writeAndParse(t, `spine_version: 3

nodes:
  A:
    emits:
      - event: E1

routes:
  - on: E1
    steps:
      - action: db.upsert
        table: bookings
        keyy: email
`)
	if err == nil {
		t.Fatal("typo'd config key keyy: must be rejected at parse time")
	}
	if !strings.Contains(err.Error(), "keyy") || !strings.Contains(err.Error(), "'key'") {
		t.Fatalf("rejection should suggest 'key' for 'keyy', got: %v", err)
	}
}

func TestScalarKeyStillValid(t *testing.T) {
	// Regression: scalar key: on db.upsert is parser-intercepted into
	// Config["key"] — validation must accept it (identity semantics).
	err := writeAndParse(t, `spine_version: 3

nodes:
  A:
    emits:
      - event: E1

routes:
  - on: E1
    steps:
      - action: db.upsert
        table: customers
        key: email
`)
	if err != nil {
		t.Fatalf("scalar key: must remain valid: %v", err)
	}
}

func TestSetActionAcceptsFreeFormKeys(t *testing.T) {
	// set is free-form BY DESIGN: every pair becomes a payload field.
	err := writeAndParse(t, `spine_version: 3

nodes:
  A:
    emits:
      - event: E1

routes:
  - on: E1
    steps:
      - action: set
        whatever: yes
        anything_else: hello
`)
	if err != nil {
		t.Fatalf("set must accept arbitrary keys: %v", err)
	}
}

func TestUnknownPluginStyleActionAllowedAtParse(t *testing.T) {
	// A name with no resemblance to any builtin is presumed to be a plugin
	// action (registered via Bus.RegisterAction, possibly after parsing) —
	// parse must NOT reject it. The engine's dispatch fails loudly at
	// runtime if nothing is registered under the name (dispatchAction no
	// longer returns nil silently).
	err := writeAndParse(t, `spine_version: 3

nodes:
  A:
    emits:
      - event: E1

routes:
  - on: E1
    steps:
      - action: notify.slack
        channel: alerts
`)
	if err != nil {
		t.Fatalf("plugin-style action must parse (runtime dispatch validates registration): %v", err)
	}
}
