package engine

import (
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestAuthHashAndVerify(t *testing.T) {
	b := &Bus{}

	hashPayload := map[string]interface{}{"pw": "hunter2secret"}
	err := b.authHash(&manifest.RouteStep{Config: map[string]string{
		"password": "$event.payload.pw", "set": "hash_out",
	}}, "TEST", hashPayload)
	if err != nil {
		t.Fatalf("auth.hash: %v", err)
	}
	hash, _ := hashPayload["hash_out"].(string)
	if len(hash) < 59 || hash[:4] != "$2a$" && hash[:4] != "$2b$" {
		t.Fatalf("bad bcrypt hash: %q", hash)
	}

	// Correct password → true
	okPayload := map[string]interface{}{"pw": "hunter2secret", "stored": hash}
	if err := b.authVerify(&manifest.RouteStep{Config: map[string]string{
		"password": "$event.payload.pw", "hash": "$event.payload.stored", "set": "ok",
	}}, "TEST", okPayload); err != nil {
		t.Fatalf("auth.verify: %v", err)
	}
	if okPayload["ok"] != true {
		t.Fatalf("expected ok=true, got %v", okPayload["ok"])
	}

	// Wrong password → false, not an error
	badPayload := map[string]interface{}{"pw": "wrong", "stored": hash}
	_ = b.authVerify(&manifest.RouteStep{Config: map[string]string{
		"password": "$event.payload.pw", "hash": "$event.payload.stored",
	}}, "TEST", badPayload)
	if badPayload["auth_ok"] != false {
		t.Fatalf("expected auth_ok=false, got %v", badPayload["auth_ok"])
	}

	// Unknown account (empty hash) → false, no panic
	missPayload := map[string]interface{}{"pw": "whatever", "stored": ""}
	_ = b.authVerify(&manifest.RouteStep{Config: map[string]string{
		"password": "$event.payload.pw", "hash": "$event.payload.stored",
	}}, "TEST", missPayload)
	if missPayload["auth_ok"] != false {
		t.Fatalf("expected auth_ok=false for empty hash, got %v", missPayload["auth_ok"])
	}

	// Empty password → error
	if err := b.authHash(&manifest.RouteStep{}, "TEST", map[string]interface{}{}); err == nil {
		t.Fatal("expected error for empty password")
	}
}
