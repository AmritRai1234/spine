package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Auth actions — password hashing and verification for user accounts.
//
//	auth.hash:   hash a plaintext password into a bcrypt hash
//	             config: password (value expr), set (output key, default "password_hash")
//	auth.verify: compare a plaintext password against a stored bcrypt hash
//	             config: password (value expr), hash (value expr), set (output key, default "auth_ok")
//	             Sets the output key to true/false — use `assert` for fail-fast routes.
//
// Uses bcrypt with the library default cost (10). Timing is constant for the
// compare path; a missing user still burns a bcrypt round-trip via the dummy
// hash below so login timing doesn't leak account existence.

// dummyHash is burned when auth.verify runs against an empty/missing hash so
// that "no such user" takes the same wall-clock time as "wrong password".
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("spine-dummy-password"), bcrypt.DefaultCost)

func resolveAuthString(expr string, eventName string, payload map[string]interface{}) string {
	if expr == "" {
		return ""
	}
	v := ResolveValue(expr, eventName, payload)
	s, _ := v.(string)
	return s
}

func (b *Bus) authHash(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	setKey := step.Config["set"]
	if setKey == "" {
		setKey = "password_hash"
	}
	pw := resolveAuthString(step.Config["password"], eventName, payload)
	if pw == "" {
		return fmt.Errorf("auth.hash requires 'password' config resolving to a non-empty value")
	}
	if len(pw) > 72 || !utf8.ValidString(pw) {
		// bcrypt truncates at 72 bytes — reject rather than silently weaken.
		return fmt.Errorf("auth.hash: password must be 1-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("auth.hash failed: %w", err)
	}
	payload[setKey] = string(hash)
	return nil
}

func (b *Bus) authVerify(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	setKey := step.Config["set"]
	if setKey == "" {
		setKey = "auth_ok"
	}
	pw := resolveAuthString(step.Config["password"], eventName, payload)
	hash := resolveAuthString(step.Config["hash"], eventName, payload)
	if pw == "" {
		return fmt.Errorf("auth.verify requires 'password' config")
	}
	if !strings.HasPrefix(hash, "$2") {
		// No stored hash (unknown account) — burn a compare anyway.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		payload[setKey] = false
		return nil
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw))
	payload[setKey] = err == nil
	return nil
}
