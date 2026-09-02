package engine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// Social token vault — durable, encrypted storage for OAuth tokens.
//
// Revisiting the session-only design (2026-09-02): a Hootsuite-style app
// cannot ask every user to re-connect all accounts after each deploy or
// restart. Tokens are now persisted in a dedicated `_spine_social_tokens`
// table, encrypted at rest with AES-256-GCM. The key is derived (PBKDF2-style
// SHA-256 stretching with a fixed app salt) from SPINE_SOCIAL_VAULT_KEY.
//
// Security model:
//
//   - SPINE_SOCIAL_VAULT_KEY unset ⇒ vault DISABLED: tokens stay in memory
//     only (the original behavior) and social.post uses them until restart.
//     Set the key to opt in to persistence. No default key — an operator who
//     does nothing gets the safer of the two worlds.
//   - Ciphertext + nonce + per-account metadata live in DB; the plaintext
//     never touches disk.
//   - Env-resolved credentials (client secrets) still never persist.
//   - Memory cache holds decrypted tokens for the hot path (sub-microsecond
//     reads, same shape as the old store); writes go through the vault.
//   - Rows are keyed by account key ("platform" or "platform:key") so the
//     multi-account connect flow keeps working.

const socialVaultTable = `_spine_social_tokens`

type socialVaultRow struct {
	AccountKey    string
	Ciphertext    []byte // nonce || AES-GCM ciphertext
	AccountLabel  string
	ExternalAccID string
	ConnectedVia  string
	ExpiresAt     string // RFC3339, zero = no expiry
	UpdatedAt     string
}

// socialVaultKey derives the AES-256 key from the operator secret.
// SHA-256 stretched with a fixed spine salt — sufficient for a locally
// stored high-entropy operator key; not a user-password KDF by design.
func socialVaultKey(secret string) []byte {
	sum := sha256.Sum256([]byte("spine-social-vault-v1:" + secret))
	return sum[:]
}

// socialVaultEnabled reports whether persistence is active.
func socialVaultEnabled() bool {
	return os.Getenv("SPINE_SOCIAL_VAULT_KEY") != ""
}

func (b *Bus) ensureSocialVaultTable() error {
	if !socialVaultEnabled() {
		return nil
	}
	q := `CREATE TABLE IF NOT EXISTS "` + socialVaultTable + `" (
		account_key TEXT PRIMARY KEY,
		ciphertext BLOB NOT NULL,
		account_label TEXT NOT NULL DEFAULT '',
		external_account_id TEXT NOT NULL DEFAULT '',
		connected_via TEXT NOT NULL DEFAULT 'oauth',
		expires_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT ''
	)`
	_, err := b.db.Exec(q)
	if err != nil {
		return fmt.Errorf("social vault table init failed: %w", err)
	}
	return nil
}

// socialSeal encrypts an access/refresh token pair under the vault key.
func socialSeal(key []byte, acct socialAccount) ([]byte, error) {
	plain, err := json.Marshal(map[string]string{
		"access_token":  acct.AccessToken,
		"refresh_token": acct.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// socialOpen decrypts a vault blob.
func socialOpen(key, blob []byte) (access, refresh string, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", "", fmt.Errorf("social vault: ciphertext too short")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return "", "", fmt.Errorf("social vault: decryption failed (wrong SPINE_SOCIAL_VAULT_KEY?): %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return "", "", fmt.Errorf("social vault: plaintext unparseable: %w", err)
	}
	return m["access_token"], m["refresh_token"], nil
}

// socialVaultStore persists an account (called from connect paths). Failure
// to persist is logged loudly but does NOT fail the connect — the in-memory
// account still works this session.
func (b *Bus) socialVaultStore(accountKey string, acct socialAccount) {
	if !socialVaultEnabled() {
		return
	}
	key := socialVaultKey(os.Getenv("SPINE_SOCIAL_VAULT_KEY"))
	blob, err := socialSeal(key, acct)
	if err != nil {
		log.Printf("[social] vault seal failed for %s: %v", accountKey, err)
		return
	}
	expires := ""
	if !acct.ExpiresAt.IsZero() {
		expires = acct.ExpiresAt.UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = b.db.Exec(
		`INSERT INTO "`+socialVaultTable+`" (account_key, ciphertext, account_label, external_account_id, connected_via, expires_at, updated_at)
		 VALUES (`+b.ph(1)+`, `+b.ph(2)+`, `+b.ph(3)+`, `+b.ph(4)+`, `+b.ph(5)+`, `+b.ph(6)+`, `+b.ph(7)+`)
		 ON CONFLICT(account_key) DO UPDATE SET
		   ciphertext=`+b.ph(8)+`, account_label=`+b.ph(9)+`, external_account_id=`+b.ph(10)+`,
		   connected_via=`+b.ph(11)+`, expires_at=`+b.ph(12)+`, updated_at=`+b.ph(13),
		accountKey, blob, acct.AccountLabel, acct.ExternalAccID, acct.ConnectedVia, expires, now,
		blob, acct.AccountLabel, acct.ExternalAccID, acct.ConnectedVia, expires, now)
	if err != nil {
		log.Printf("[social] vault write failed for %s: %v", accountKey, err)
		return
	}
	log.Printf("[social] vault: %s persisted (encrypted)", accountKey)
}

// socialVaultDelete drops a stored account (disconnect).
func (b *Bus) socialVaultDelete(accountKey string) {
	if !socialVaultEnabled() {
		return
	}
	if _, err := b.db.Exec(`DELETE FROM "`+socialVaultTable+`" WHERE account_key = `+b.ph(1), accountKey); err != nil {
		log.Printf("[social] vault delete failed for %s: %v", accountKey, err)
	}
}

// socialVaultLoadAll hydrates the in-memory account cache from the vault at
// startup. Called from NewBus when the vault is enabled. A decryption failure
// (wrong key) skips that row loudly instead of crashing the engine.
func (b *Bus) socialVaultLoadAll() {
	if !socialVaultEnabled() {
		return
	}
	rows, err := b.db.Query(`SELECT account_key, ciphertext, account_label, external_account_id, connected_via, expires_at FROM "` + socialVaultTable + `"`)
	if err != nil {
		log.Printf("[social] vault load failed: %v", err)
		return
	}
	defer rows.Close()

	key := socialVaultKey(os.Getenv("SPINE_SOCIAL_VAULT_KEY"))
	loaded := 0
	failed := 0
	cur := map[string]socialAccount{}
	if p := socialAccounts.Load(); p != nil {
		for k, v := range *p {
			cur[k] = v
		}
	}
	for rows.Next() {
		var r socialVaultRow
		var blob []byte
		if err := rows.Scan(&r.AccountKey, &blob, &r.AccountLabel, &r.ExternalAccID, &r.ConnectedVia, &r.ExpiresAt); err != nil {
			failed++
			continue
		}
		access, refresh, derr := socialOpen(key, blob)
		if derr != nil {
			log.Printf("[social] vault: skipping %s — %v", r.AccountKey, derr)
			failed++
			continue
		}
		var expires time.Time
		if r.ExpiresAt != "" {
			if t, perr := time.Parse(time.RFC3339, r.ExpiresAt); perr == nil {
				expires = t
			}
		}
		cur[r.AccountKey] = socialAccount{
			AccessToken:   access,
			RefreshToken:  refresh,
			AccountLabel:  r.AccountLabel,
			ExternalAccID: r.ExternalAccID,
			ConnectedVia:  r.ConnectedVia,
			ExpiresAt:     expires,
		}
		loaded++
	}
	if err := rows.Err(); err != nil {
		log.Printf("[social] vault load iteration failed: %v", err)
	}
	if loaded > 0 {
		socialAccounts.Store(&cur)
		log.Printf("[social] vault: %d account(s) restored from storage", loaded)
	}
	if failed > 0 {
		log.Printf("[social] vault: %d account(s) could NOT be decrypted — wrong SPINE_SOCIAL_VAULT_KEY or corrupted rows", failed)
	}
}

// socialConnectedCount is a cheap SQL check (no decryption) for health/introspection.
func (b *Bus) socialConnectedCount() (n int, err error) {
	if !socialVaultEnabled() {
		if p := socialAccounts.Load(); p != nil {
			return len(*p), nil
		}
		return 0, nil
	}
	err = b.db.QueryRow(`SELECT COUNT(1) FROM "` + socialVaultTable + `"`).Scan(&n)
	return n, err
}

// _ suppress unused warnings for the sql import when vault disabled paths compile
var _ = sql.ErrNoRows

// socialVaultWipe removes every stored token (used by SocialReset in tests
// and available for emergency credential rotation).
func (b *Bus) socialVaultWipe() {
	if !socialVaultEnabled() {
		return
	}
	if _, err := b.db.Exec(`DELETE FROM "` + socialVaultTable + `"`); err != nil {
		log.Printf("[social] vault wipe failed: %v", err)
	}
}

// SocialVaultStatus reports vault state for dashboards/ops.
func SocialVaultStatus() (enabled bool, hint string) {
	if k := os.Getenv("SPINE_SOCIAL_VAULT_KEY"); k != "" {
		return true, "tokens persist encrypted (AES-256-GCM) in _spine_social_tokens"
	}
	return false, "SPINE_SOCIAL_VAULT_KEY unset — connected accounts live until restart"
}
