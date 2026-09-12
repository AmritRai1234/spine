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
	// Tables is the per-table read scope (nil = all tables readable). When
	// non-nil, GET /tables/{name} is restricted to the listed tables (each
	// ANDed with its filter) and unlisted tables return 403. Static per-role
	// scoping — see manifest.AccessRule for the isolation caveats.
	Tables map[string]string
}

// TableReadScope returns (filter, allowed) for reading the named table.
// nil Tables map = everything allowed, no extra filter. A missing entry in
// a non-nil map = denied.
func (ac *AccessContext) TableReadScope(table string) (string, bool) {
	if ac.Tables == nil {
		return ac.Filter, true
	}
	filter, ok := ac.Tables[table]
	return filter, ok
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

// CanReceive returns true if this access context may observe state broadcasts
// and audit-log entries originating from the given event. Mirrors CanEmit:
// nil whitelist = everything, otherwise only whitelisted events.
func (ac *AccessContext) CanReceive(event string) bool {
	if ac.Events == nil {
		return true
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
	rules     []manifest.AccessRule
	userKeys  *UserKeyStore      // optional per-user dynamic keys (auth.login/register)
	roleEvents map[string][]string // role → event whitelist (for per-user inheritance)
}

// NewAccessResolver builds a resolver from manifest access rules.
// userKeys (optional) provides dynamic per-user keys: after the static rules
// miss, a matching issued key resolves to a per-user AccessContext whose row
// filter isolates the caller's rows by email.
func NewAccessResolver(rules []manifest.AccessRule) *AccessResolver {
	if len(rules) == 0 {
		return nil
	}
	return &AccessResolver{rules: rules, roleEvents: roleEventMap(rules)}
}

// roleEventMap indexes each role's event whitelist so per-user contexts can
// inherit it. Roles with no whitelist (nil = unrestricted) are absent.
func roleEventMap(rules []manifest.AccessRule) map[string][]string {
	m := make(map[string][]string, len(rules))
	for _, r := range rules {
		if r.Events != nil {
			m[r.Role] = r.Events
		}
	}
	return m
}

// SetUserKeyStore attaches the per-user key store to the resolver.
func (ar *AccessResolver) SetUserKeyStore(store *UserKeyStore) {
	if ar != nil {
		ar.userKeys = store
	}
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
	var matched *manifest.AccessRule

	for i := range ar.rules {
		keyBytes := []byte(ar.rules[i].Key)
		if len(apiKeyBytes) == len(keyBytes) && subtle.ConstantTimeCompare(apiKeyBytes, keyBytes) == 1 {
			matched = &ar.rules[i]
		}
	}

	if matched == nil {
		return ar.resolveUserKey(apiKey)
	}

	return &AccessContext{
		Role:     matched.Role,
		ReadOnly: matched.ReadOnly,
		Filter:   matched.Filter,
		Events:   matched.Events,
		Tables:   matched.Tables,
	}
}

// resolveUserKey builds a per-user AccessContext from an issued key record:
// the caller's row filter is pinned to their account email so table reads
// return only their own rows. Static role rules always win (they are the
// admin/staff tier); this only fires on a full static miss.
func (ar *AccessResolver) resolveUserKey(apiKey string) *AccessContext {
	if ar == nil || ar.userKeys == nil {
		return nil
	}
	rec := ar.userKeys.Lookup(apiKey)
	if rec == nil {
		return nil
	}
	return &AccessContext{
		Role:   rec.role,
		Filter: "email = '" + strings.ReplaceAll(rec.email, "'", "''") + "'",
		Events: ar.roleEvents[rec.role],
	}
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
