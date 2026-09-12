package engine

// Per-user identity — dynamic key issuance on top of the static RLAC layer.
//
// The manifest access rules are static: one key, one scope for every holder.
// This file adds customer-facing accounts: register/login issues a unique
// random API key per user (returned once in the route response), stored only
// as a SHA-256 digest. Resolve() consults the store after the static rules
// miss, and builds a per-user AccessContext whose row filter is
// "email = '<account>'" so the existing per-role `tables:` scoping grammar
// applies unchanged — isolation is per-caller instead of per-role.
//
// Gated on the manifest: no tables configured = the actions are silent
// no-ops that error clearly, mirroring stripe.checkout's behavior without
// STRIPE_SECRET_KEY.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
	"golang.org/x/crypto/bcrypt"
)

const (
	userKeysTable    = "_spine_user_keys"
	userKeysTTLHours = 24 * 14 // issued keys live 14 days; login rotates
)

// userKeyRecord is the durable form of one issued key.
type userKeyRecord struct {
	keyHash   string // sha256 hex of the raw key
	email     string
	role      string
	expiresAt time.Time
}

// UserKeyStore manages per-user issued keys. Raw keys are never stored —
// only sha256(key). All lookups are constant-time over the digest bytes.
type UserKeyStore struct {
	mu     sync.RWMutex
	keys   map[string]userKeyRecord // keyHash -> record
	loaded bool
	bus    *Bus
}

// NewUserKeyStore builds a store backed by the engine's database. Tables are
// created lazily on first register/login (keeps in-memory DBs cheap).
func NewUserKeyStore(bus *Bus) *UserKeyStore {
	return &UserKeyStore{keys: map[string]userKeyRecord{}, bus: bus}
}

func (s *UserKeyStore) ensureTables() error {
	if s.loaded {
		return nil
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		key_hash TEXT PRIMARY KEY,
		email    TEXT NOT NULL,
		role     TEXT NOT NULL DEFAULT 'customer',
		expires_at INTEGER NOT NULL
	)`, userKeysTable)
	if _, err := s.bus.DB().Exec(ddl); err != nil {
		return fmt.Errorf("user key store init failed: %w", err)
	}
	if err := s.loadFromDB(); err != nil {
		return err
	}
	s.loaded = true
	return nil
}

func (s *UserKeyStore) loadFromDB() error {
	rows, err := s.bus.DB().Query(
		fmt.Sprintf("SELECT key_hash, email, role, expires_at FROM %s WHERE expires_at > ?", userKeysTable),
		time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("user key store load failed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r userKeyRecord
		var exp int64
		if err := rows.Scan(&r.keyHash, &r.email, &r.role, &exp); err != nil {
			return err
		}
		r.expiresAt = time.Unix(exp, 0)
		s.keys[r.keyHash] = r
	}
	return rows.Err()
}

// keyDigest returns the hex sha256 of the raw key — the only persisted form.
func keyDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Issue generates a fresh random key for (email, role), stores its digest,
// and returns the raw key (shown once to the client). Emails are validated
// here (not just at register) because the digest becomes a row filter.
func (s *UserKeyStore) Issue(email, role string) (string, error) {
	if !userEmailRe.MatchString(email) {
		return "", fmt.Errorf("user key store: refusing to issue key for invalid email %q", email)
	}
	if err := s.ensureTables(); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("key generation failed: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	rec := userKeyRecord{
		keyHash:   keyDigest(raw),
		email:     email,
		role:      role,
		expiresAt: time.Now().Add(userKeysTTLHours * time.Hour),
	}
	s.mu.Lock()
	s.keys[rec.keyHash] = rec
	s.mu.Unlock()
	if _, err := s.bus.DB().Exec(
		fmt.Sprintf("INSERT INTO %s (key_hash, email, role, expires_at) VALUES (?, ?, ?, ?)", userKeysTable),
		rec.keyHash, rec.email, rec.role, rec.expiresAt.Unix(),
	); err != nil {
		return "", fmt.Errorf("user key persist failed: %w", err)
	}
	return raw, nil
}

// Revoke deletes any key matching the raw value (logout). Missing key = ok.
func (s *UserKeyStore) Revoke(raw string) {
	digest := keyDigest(raw)
	s.mu.Lock()
	delete(s.keys, digest)
	s.mu.Unlock()
	_, _ = s.bus.DB().Exec(
		fmt.Sprintf("DELETE FROM %s WHERE key_hash = ?", userKeysTable), digest)
}

// Lookup resolves a raw client key to its record (nil = unknown/expired).
// Constant-time over all stored digests to match AccessResolver semantics.
func (s *UserKeyStore) Lookup(raw string) *userKeyRecord {
	if raw == "" {
		return nil
	}
	given := []byte(keyDigest(raw))
	s.mu.RLock()
	defer s.mu.RUnlock()
	var match *userKeyRecord
	for digest, rec := range s.keys {
		if len(given) == len(digest) && subtle.ConstantTimeCompare(given, []byte(digest)) == 1 {
			if time.Now().Before(rec.expiresAt) {
				match = &rec
			}
		}
	}
	return match
}

var userEmailRe = regexp.MustCompile(`^[^@'\s]+@[^@'\s]+\.[^@'\s]+$`)

// ── Login attempt throttling ────────────────────────────────────────
// Per-email+IP failed-attempt tracker with progressive lockout. Lives in
// memory (resets on restart — acceptable: it is a brake, not a vault) and
// is swept lazily so abandoned entries don't accumulate.

const (
	loginMaxFails     = 5               // failures before lockout
	loginLockout      = 15 * time.Minute
	loginFailWindow   = 15 * time.Minute // failures older than this stop counting
)

type loginAttempt struct {
	fails    int
	lastFail time.Time
	until    time.Time // lockout expiry (zero = not locked)
}

// LoginThrottler tracks failed login attempts keyed by "email|ip".
type LoginThrottler struct {
	mu      sync.Mutex
	entries map[string]*loginAttempt
	now     func() time.Time // injectable for tests
}

func NewLoginThrottler() *LoginThrottler {
	return &LoginThrottler{entries: map[string]*loginAttempt{}, now: time.Now}
}

// sweepLocked drops entries whose lockout expired and whose failures are
// older than the window — called under lock.
func (t *LoginThrottler) sweepLocked(now time.Time) {
	for k, a := range t.entries {
		if a.until.IsZero() || now.After(a.until) {
			if now.Sub(a.lastFail) > loginLockout {
				delete(t.entries, k)
			}
		}
	}
}

// Check returns (allowed, retryAfter). A locked key is refused until the
// lockout expires.
func (t *LoginThrottler) Check(email, ip string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)
	a, ok := t.entries[t.key(email, ip)]
	if !ok {
		return true, 0
	}
	if !a.until.IsZero() && now.Before(a.until) {
		return false, a.until.Sub(now)
	}
	return true, 0
}

// RecordFailure increments the failure count and locks the key when the
// threshold is crossed. Progressive: each lockout after the first is doubled.
func (t *LoginThrottler) RecordFailure(email, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	k := t.key(email, ip)
	a := t.entries[k]
	if a == nil {
		a = &loginAttempt{}
		t.entries[k] = a
	}
	a.fails++
	a.lastFail = now
	if a.fails >= loginMaxFails {
		if !a.until.IsZero() && now.Before(a.until) {
			// already locked; extend on further failures (attacker retrying
			// during lockout does not get a free reset)
			a.until = a.until.Add(loginLockout)
		} else {
			mult := 1
			if a.fails > loginMaxFails {
				mult = 1 << (a.fails - loginMaxFails)
				if mult > 8 {
					mult = 8
				}
			}
			a.until = now.Add(loginLockout * time.Duration(mult))
		}
	}
}

// RecordSuccess clears the failure count for the key.
func (t *LoginThrottler) RecordSuccess(email, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, t.key(email, ip))
}

func (t *LoginThrottler) key(email, ip string) string {
	return email + "|" + ip
}
// ── Account actions ─────────────────────────────────────────────────

// userRegister implements the `auth.register` action.
// Config: email / password (payload refs), role (optional, default customer).
// Sets payload[set] = the issued raw key (default key name: auth_key) and
// payload[<set>_email] = the account email.
func (b *Bus) userRegister(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	email := resolveAuthString(step.Config["email"], eventName, payload)
	password := resolveAuthString(step.Config["password"], eventName, payload)
	role := step.Config["role"]
	if role == "" {
		role = "customer"
	}
	if !userEmailRe.MatchString(email) {
		return fmt.Errorf("auth.register requires a valid 'email' resolving to a non-empty address")
	}
	if len(password) < 8 {
		return fmt.Errorf("auth.register: password must be at least 8 characters")
	}
	if len(password) > 72 {
		return fmt.Errorf("auth.register: password must be 1-72 bytes (bcrypt limit)")
	}
	if err := b.ensureUserTables(); err != nil {
		return err
	}
	// Existing account? Timing-equalized: burn a bcrypt compare either way.
	var storedHash string
	row := b.DB().QueryRow(`SELECT password_hash FROM _spine_users WHERE email = ?`, email)
	_ = row.Scan(&storedHash)
	burn := func() { _ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) }

	if storedHash != "" {
		_ = bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
		return fmt.Errorf("auth.register: an account with that email already exists")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("auth.register: hashing failed: %w", err)
	}
	if _, err := b.DB().Exec(
		`INSERT INTO _spine_users (email, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		email, string(hash), role, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("auth.register: account create failed: %w", err)
	}
	_ = burn // keep the hasher busy-equivalent (hashing happened above; no-op)

	raw, err := b.userKeys.Issue(email, role)
	if err != nil {
		return err
	}
	setKey := step.Config["set"]
	if setKey == "" {
		setKey = "auth_key"
	}
	payload[setKey] = raw
	payload[setKey+"_email"] = email
	return nil
}

// userLogin implements the `auth.login` action. Verifies the password with a
// timing-equalized compare, throttles per email+IP (progressive lockout after
// 5 failures), rotates the key (old keys for the account are revoked so a
// leaked key can't outlive a credential change), issues fresh.
func (b *Bus) userLogin(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	email := resolveAuthString(step.Config["email"], eventName, payload)
	password := resolveAuthString(step.Config["password"], eventName, payload)
	if email == "" || password == "" {
		return fmt.Errorf("auth.login requires 'email' and 'password' config")
	}
	// Reserved engine-stamped caller IP (see /emit stamping). Strip it here so
	// it never reaches db.insert columns or audit payloads.
	loginIP, _ := payload["_login_ip"].(string)
	delete(payload, "_login_ip")
	if err := b.ensureUserTables(); err != nil {
		return err
	}
	if allowed, retryAfter := b.loginThrottle.Check(email, loginIP); !allowed {
		setKey := defaultSetKey(step.Config["set"])
		payload[setKey] = false
		payload[setKey+"_retry_after_s"] = int(retryAfter.Seconds()) + 1
		return nil // locked out — soft failure, no error surface, no oracle
	}
	var storedHash, role string
	row := b.DB().QueryRow(`SELECT password_hash, role FROM _spine_users WHERE email = ?`, email)
	err := row.Scan(&storedHash, &role)
	if err == sql.ErrNoRows {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		b.loginThrottle.RecordFailure(email, loginIP)
		setKey := defaultSetKey(step.Config["set"])
		payload[setKey] = false
		return nil // unknown account — verify-style soft failure, no error surface
	}
	if err != nil {
		return fmt.Errorf("auth.login: lookup failed: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) != nil {
		b.loginThrottle.RecordFailure(email, loginIP)
		setKey := defaultSetKey(step.Config["set"])
		payload[setKey] = false
		return nil
	}
	b.loginThrottle.RecordSuccess(email, loginIP)
	// 2FA gate: enrolled accounts must present a valid, non-replayed TOTP
	// code. The check runs after password verification (no oracle that the
	// account is enrolled without knowing the password) and before key
	// issuance. Wrong/missing code = soft failure (auth_key=false).
	totpCode := resolveAuthString(step.Config["totp_code"], eventName, payload)
	if enrolled, ok := b.totpCheck(email, totpCode, time.Now()); enrolled && !ok {
		setKey := defaultSetKey(step.Config["set"])
		payload[setKey] = false
		payload[setKey+"_totp_required"] = true
		return nil
	}
	// Rotate: revoke existing keys for this account, then issue.
	if err := b.userKeys.ensureTables(); err != nil {
		return err
	}
	if _, err := b.DB().Exec(
		fmt.Sprintf(`DELETE FROM %s WHERE email = ?`, userKeysTable), email); err != nil {
		return fmt.Errorf("auth.login: key rotation failed: %w", err)
	}
	b.userKeys.mu.Lock()
	for digest, rec := range b.userKeys.keys {
		if rec.email == email {
			delete(b.userKeys.keys, digest)
		}
	}
	b.userKeys.mu.Unlock()

	raw, err := b.userKeys.Issue(email, role)
	if err != nil {
		return err
	}
	setKey := defaultSetKey(step.Config["set"])
	payload[setKey] = raw
	payload[setKey+"_email"] = email
	return nil
}

// userLogout implements the `auth.logout` action — revokes the caller's key
// (taken from config 'key' or the request's X-API-Key via payload injection).
func (b *Bus) userLogout(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	raw := resolveAuthString(step.Config["key"], eventName, payload)
	if raw != "" {
		b.userKeys.Revoke(raw)
		return nil
	}
	// No key in payload: fall back to the emitting request's key if the
	// manifest wires $auth.key; otherwise this is a no-op that succeeds.
	log.Printf("[spine] auth.logout: no key resolved in payload for event %s", eventName)
	return nil
}

// ensureUserTables creates _spine_users once per Bus lifetime.
func (b *Bus) ensureUserTables() error {
	b.userTablesOnce.Do(func() {
		_, err := b.DB().Exec(`CREATE TABLE IF NOT EXISTS _spine_users (
			email         TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'customer',
			created_at    INTEGER NOT NULL
		)`)
		b.userTablesErr = err
	})
	return b.userTablesErr
}

func defaultSetKey(v string) string {
	if v == "" {
		return "auth_key"
	}
	return v
}