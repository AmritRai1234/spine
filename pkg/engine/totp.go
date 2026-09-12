package engine

// TOTP (RFC 6238) two-factor auth for user accounts.
//
// Adds `auth.totp.setup` / `auth.totp.confirm` / `auth.totp.disable` actions
// and enforces a TOTP code in `auth.login` for enrolled accounts. The design
// is deliberate about enrollment: a secret only protects the account after
// `auth.totp.confirm` verifies one live code against it, so a typo'd or
// intercepted setup can never strand the account holder.
//
// Storage lives in a dedicated `_spine_totp` table (email → base32 secret,
// pending flag, last-used time step). Secrets are stored at rest alongside
// the password hash — the same trust domain; this engine's threat model
// treats the DB as protected-at-rest (no separate vault for symmetric
// secrets; hashed only where the material is verifiable, which a TOTP
// secret is not — it must be recoverable to generate codes).
//
// Codes: HMAC-SHA1, 30s step, 6 digits, ±1 step drift window, and a
// per-account replay guard — a accepted time step cannot be accepted again.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

const (
	totpTable      = "_spine_totp"
	totpPeriod     = 30 * time.Second
	totpDigits     = 6
	totpDriftSteps = 1 // accept t-1, t, t+1
	totpSecretLen  = 20 // 160-bit secret → 32 base32 chars
)

// totpRecord is the durable enrollment state for one account.
type totpRecord struct {
	secret  string  // base32 (no padding)
	pending bool
	used    [3]int64 // most recent accepted time steps (replay guard). Three slots = the full ±1 drift window: any accepted step stays guarded until it leaves the window.
}

// totpStore is the in-memory mirror of _spine_totp (same load-on-demand
// pattern as UserKeyStore; writes go through to the DB).
type totpStore struct {
	mu      sync.RWMutex
	records map[string]totpRecord // email -> record
	loaded  bool
	bus     *Bus
}

func newTOTPStore(bus *Bus) *totpStore {
	return &totpStore{records: map[string]totpRecord{}, bus: bus}
}

func (s *totpStore) ensure() error {
	if s.loaded {
		return nil
	}
	if _, err := s.bus.DB().Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		email      TEXT PRIMARY KEY,
		secret     TEXT NOT NULL,
		pending    INTEGER NOT NULL DEFAULT 1,
		step1      INTEGER NOT NULL DEFAULT 0,
		step2      INTEGER NOT NULL DEFAULT 0,
		step3      INTEGER NOT NULL DEFAULT 0
	)`, totpTable)); err != nil {
		return fmt.Errorf("totp store init failed: %w", err)
	}
	rows, err := s.bus.DB().Query(
		fmt.Sprintf(`SELECT email, secret, pending, step1, step2, step3 FROM %s`, totpTable))
	if err != nil {
		return fmt.Errorf("totp store load failed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r totpRecord
		var email string
		var pending int
		if err := rows.Scan(&email, &r.secret, &pending, &r.used[0], &r.used[1], &r.used[2]); err != nil {
			return err
		}
		r.pending = pending == 1
		s.records[email] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.loaded = true
	return nil
}

// ── RFC 6238 core ────────────────────────────────────────────────────

// b32NoPad is the unpadded base32 alphabet used by otpauth URIs.
var b32NoPad = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// generateTOTPSecret returns a fresh 160-bit secret in unpadded base32.
func generateTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("totp secret generation failed: %w", err)
	}
	return b32NoPad.EncodeToString(buf), nil
}

// hotp computes the RFC 4226 dynamic truncation code for (key, counter).
func hotp(key []byte, counter uint64) uint32 {
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(ctr[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return code % 1_000_000
}

// totpAt computes the 6-digit code for the time step containing t.
func totpAt(secretB32 string, t time.Time) (string, error) {
	key, err := b32NoPad.DecodeString(strings.ToUpper(strings.TrimRight(secretB32, "=")))
	if err != nil {
		return "", fmt.Errorf("totp: bad secret encoding: %w", err)
	}
	step := t.Unix() / int64(totpPeriod.Seconds())
	return fmt.Sprintf("%06d", hotp(key, uint64(step))), nil
}

// verifyTOTP checks code against secret within the drift window, newest
// step first. Returns the matched step (for the replay guard) or -1.
func verifyTOTP(secretB32, code string, t time.Time) (int64, bool) {
	key, err := b32NoPad.DecodeString(strings.ToUpper(strings.TrimRight(secretB32, "=")))
	if err != nil {
		return -1, false
	}
	now := t.Unix() / int64(totpPeriod.Seconds())
	// Constant-time over the whole window regardless of match position —
	// compare all candidates, then report.
	matched := int64(-1)
	for i := totpDriftSteps; i >= -totpDriftSteps; i-- {
		step := now + int64(i)
		want := fmt.Sprintf("%06d", hotp(key, uint64(step)))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && matched == -1 {
			matched = step
		}
	}
	return matched, matched >= 0
}

// otpauthURI builds the provisioning URI for authenticator apps.
func otpauthURI(email, secretB32 string) string {
	label := url.PathEscape("Spine:" + email)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", "Spine")
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return fmt.Sprintf("otpauth://totp/%s?%s", label, q.Encode())
}

// ── Account actions ─────────────────────────────────────────────────

// totpSetup implements `auth.totp.setup` — mint a pending enrollment secret
// for the account. Re-running setup before confirm rotates the pending
// secret; an already-confirmed enrollment is an error (use auth.totp.disable).
// Sets payload[set] = secret (default "totp_secret"), payload[<set>_uri] =
// the otpauth:// provisioning URI.
func (b *Bus) totpSetup(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	email := resolveAuthString(step.Config["email"], eventName, payload)
	if !userEmailRe.MatchString(email) {
		return fmt.Errorf("auth.totp.setup requires a valid 'email'")
	}
	if err := b.ensureUserTables(); err != nil {
		return err
	}
	if err := b.totp.ensure(); err != nil {
		return err
	}
	b.totp.mu.Lock()
	if rec, ok := b.totp.records[email]; ok && !rec.pending {
		b.totp.mu.Unlock()
		return fmt.Errorf("auth.totp.setup: account %s is already enrolled (use auth.totp.disable to unenroll)", email)
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		b.totp.mu.Unlock()
		return err
	}
	b.totp.records[email] = totpRecord{secret: secret, pending: true}
	b.totp.mu.Unlock()
	if _, err := b.DB().Exec(
		fmt.Sprintf(`INSERT INTO %s (email, secret, pending, step1, step2, step3) VALUES (?, ?, 1, 0, 0, 0)
			ON CONFLICT(email) DO UPDATE SET secret = excluded.secret, pending = 1, step1 = 0, step2 = 0, step3 = 0`, totpTable),
		email, secret,
	); err != nil {
		return fmt.Errorf("auth.totp.setup: persist failed: %w", err)
	}
	setKey := step.Config["set"]
	if setKey == "" {
		setKey = "totp_secret"
	}
	payload[setKey] = secret
	payload[setKey+"_uri"] = otpauthURI(email, secret)
	return nil
}

// totpConfirm implements `auth.totp.confirm` — verify one live code against
// the pending secret; success flips pending → enrolled. Failure is a soft
// payload[ok]=false so routes can branch without error noise.
func (b *Bus) totpConfirm(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	email := resolveAuthString(step.Config["email"], eventName, payload)
	code := resolveAuthString(step.Config["code"], eventName, payload)
	if !userEmailRe.MatchString(email) || code == "" {
		return fmt.Errorf("auth.totp.confirm requires 'email' and 'code'")
	}
	if err := b.totp.ensure(); err != nil {
		return err
	}
	b.totp.mu.Lock()
	rec, ok := b.totp.records[email]
	if !ok || !rec.pending {
		b.totp.mu.Unlock()
		payload["totp_ok"] = false
		return nil
	}
	matched, okCode := verifyTOTP(rec.secret, code, time.Now())
	if !okCode {
		b.totp.mu.Unlock()
		payload["totp_ok"] = false
		return nil
	}
	rec.pending = false
	rec.used[2] = rec.used[1]
	rec.used[1] = rec.used[0]
	rec.used[0] = matched
	b.totp.records[email] = rec
	b.totp.mu.Unlock()
	if _, err := b.DB().Exec(
		fmt.Sprintf(`UPDATE %s SET pending = 0, step1 = ?, step2 = ?, step3 = ? WHERE email = ?`, totpTable),
		matched, rec.used[1], rec.used[2], email,
	); err != nil {
		return fmt.Errorf("auth.totp.confirm: persist failed: %w", err)
	}
	payload["totp_ok"] = true
	return nil
}

// totpDisable implements `auth.totp.disable` — unenroll, but only with a
// valid current code (an attacker with a stolen session must not be able to
// silently strip the second factor). Sets payload[set]=true on success.
func (b *Bus) totpDisable(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	email := resolveAuthString(step.Config["email"], eventName, payload)
	code := resolveAuthString(step.Config["code"], eventName, payload)
	if !userEmailRe.MatchString(email) || code == "" {
		return fmt.Errorf("auth.totp.disable requires 'email' and 'code'")
	}
	if err := b.totp.ensure(); err != nil {
		return err
	}
	b.totp.mu.Lock()
	rec, ok := b.totp.records[email]
	if !ok || rec.pending {
		b.totp.mu.Unlock()
		payload["totp_disabled"] = false
		return nil // nothing enrolled — soft success-ish; nothing to strip
	}
	matched, okCode := verifyTOTP(rec.secret, code, time.Now())
	if !okCode {
		b.totp.mu.Unlock()
		payload["totp_disabled"] = false
		return nil
	}
	_ = matched // code valid — proceed
	delete(b.totp.records, email)
	b.totp.mu.Unlock()
	if _, err := b.DB().Exec(
		fmt.Sprintf(`DELETE FROM %s WHERE email = ?`, totpTable), email); err != nil {
		return fmt.Errorf("auth.totp.disable: persist failed: %w", err)
	}
	payload["totp_disabled"] = true
	return nil
}

// totpCheck reports whether (email, code) passes for an enrolled account.
// Enrolled + empty code = fail. Replay guard: the matched step must differ
// from lastStep. Used by auth.login.
func (b *Bus) totpCheck(email, code string, t time.Time) (enrolled bool, ok bool) {
	if err := b.totp.ensure(); err != nil {
		return false, false
	}
	b.totp.mu.RLock()
	rec, ok := b.totp.records[email]
	b.totp.mu.RUnlock()
	if !ok || rec.pending {
		return false, false
	}
	if code == "" {
		return true, false
	}
	matched, okCode := verifyTOTP(rec.secret, code, t)
	if !okCode {
		return true, false
	}
	for _, used := range rec.used {
		if matched == used {
			return true, false // replay within the drift window
		}
	}
	// Persist the replay guard advance: the accepted step joins the ring
	// (most-recent first). Three slots cover the full ±1 drift window, so a
	// code from any step inside the window can never be accepted twice —
	// including the C-1 / C+1 alternate-acceptance hole a 2-slot guard had.
	b.totp.mu.Lock()
	if cur, still := b.totp.records[email]; still && !cur.pending {
		cur.used[2] = cur.used[1]
		cur.used[1] = cur.used[0]
		cur.used[0] = matched
		b.totp.records[email] = cur
	}
	b.totp.mu.Unlock()
	if _, err := b.DB().Exec(
		fmt.Sprintf(`UPDATE %s SET step1 = ?, step2 = ?, step3 = ? WHERE email = ?`, totpTable),
		matched, rec.used[1], rec.used[2], email); err != nil {
		// Guard persists in memory regardless; DB failure is non-fatal here —
		// the in-memory check above still blocks the immediate replay.
		_ = err
	}
	return true, true
}

// totpEnrolled reports whether the account has a confirmed enrollment.
func (b *Bus) totpEnrolled(email string) bool {
	if err := b.totp.ensure(); err != nil {
		return false
	}
	b.totp.mu.RLock()
	defer b.totp.mu.RUnlock()
	rec, ok := b.totp.records[email]
	return ok && !rec.pending
}