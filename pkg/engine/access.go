package engine

import (
	"crypto/subtle"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// AccessContext holds the resolved permissions for an authenticated request.
type AccessContext struct {
	Role     string
	ReadOnly bool
	Filter   string   // WHERE clause to inject on table queries
	Events   []string // nil = all events allowed
}

// CanEmit returns true if this access context permits emitting the given event.
func (ac *AccessContext) CanEmit(event string) bool {
	if ac.ReadOnly {
		return false
	}
	if ac.Events == nil {
		return true // no whitelist = all events allowed
	}
	for _, e := range ac.Events {
		if e == event {
			return true
		}
	}
	return false
}

// AccessResolver maps API keys to access contexts using constant-time comparison.
type AccessResolver struct {
	rules []manifest.AccessRule
}

// NewAccessResolver builds a resolver from manifest access rules.
func NewAccessResolver(rules []manifest.AccessRule) *AccessResolver {
	if len(rules) == 0 {
		return nil
	}
	return &AccessResolver{rules: rules}
}

// HasRules returns true if access rules are configured.
func (ar *AccessResolver) HasRules() bool {
	return ar != nil && len(ar.rules) > 0
}

// Resolve finds the access context for the given API key.
// Uses constant-time comparison against all keys to prevent timing attacks.
// Returns nil if no matching key is found (unauthorized).
func (ar *AccessResolver) Resolve(apiKey string) *AccessContext {
	if ar == nil || len(ar.rules) == 0 {
		return nil
	}

	apiKeyBytes := []byte(apiKey)
	for _, rule := range ar.rules {
		if subtle.ConstantTimeCompare(apiKeyBytes, []byte(rule.Key)) == 1 {
			return &AccessContext{
				Role:     rule.Role,
				ReadOnly: rule.ReadOnly,
				Filter:   rule.Filter,
				Events:   rule.Events,
			}
		}
	}
	return nil
}

// extractAPIKey extracts the API key from standard HTTP auth headers.
func extractAPIKey(xApiKey, authHeader string) string {
	if xApiKey != "" {
		return xApiKey
	}
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}
