package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

// Social token vault acceptance criteria:
//
//	AC1: vault disabled (no SPINE_SOCIAL_VAULT_KEY) — original session-only
//	     behavior; no _spine_social_tokens table created; manual connect works.
//	AC2: vault enabled — connect persists the account; a NEW engine instance
//	     on the same DB restores it (SocialConnections shows it, and the
//	     restored token actually publishes).
//	AC3: wrong vault key at startup — restore skips the row loudly, no crash,
//	     account not connected.
//	AC4: disconnect removes the persisted row (restart does not resurrect).
//	AC5: plaintext tokens never appear in the raw DB file bytes.

func vaultEngine(t *testing.T, dbPath string) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(socialManifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestSocialVaultDisabledSessionOnly(t *testing.T) {
	engine.SocialReset()
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "") // explicitly off

	dbPath := filepath.Join(t.TempDir(), "spine.db")
	eng := vaultEngine(t, dbPath)

	if _, err := eng.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{
		"platform":     "facebook",
		"access_token": "mem-only-token",
	}); err != nil {
		t.Fatalf("manual connect failed: %v", err)
	}
	if _, ok := eng.Bus.SocialConnections()["facebook"]; !ok {
		t.Fatal("in-memory account missing with vault disabled")
	}
	// No vault table should exist.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "_spine_social_tokens") {
		t.Fatal("vault table created although vault is disabled")
	}
}

func TestSocialVaultPersistsAcrossRestart(t *testing.T) {
	engine.SocialReset()
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "vault-secret-1")
	t.Setenv("SOCIAL_FACEBOOK_PAGE_ID", "pg_123")

	dbPath := filepath.Join(t.TempDir(), "spine.db")

	// Session 1: connect via OAuth against the fake provider.
	provider := startTestProvider(t, 1)
	t.Setenv("SPINE_SOCIAL_API_BASE_FACEBOOK", provider.URL)
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_ID", "cid_123")
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_SECRET", "sec_456")
	t.Setenv("SPINE_PUBLIC_URL", "https://social.example.com")

	eng1 := vaultEngine(t, dbPath)
	if _, err := eng1.Bus.Emit("CONNECT_START", map[string]interface{}{"platform": "facebook"}); err != nil {
		t.Fatalf("CONNECT_START failed: %v", err)
	}
	state := socialState(t, eng1, "SOCIAL_AUTH")["social_state"].(string)
	if _, err := eng1.Bus.SocialOAuthCallback("facebook", "authcode_1", state); err != nil {
		t.Fatalf("OAuth callback failed: %v", err)
	}
	eng1.Close() // simulate deploy/restart

	// Session 2: fresh engine on the same DB — account restored.
	eng2 := vaultEngine(t, dbPath)
	conns := eng2.Bus.SocialConnections()
	acct, ok := conns["facebook"]
	if !ok || acct["account_label"] != "Test Page" {
		t.Fatalf("vault restore failed: %v", conns)
	}

	// And the restored token is usable for real publishing (second exchange).
	provider2 := startTestProvider(t, 1)
	t.Setenv("SPINE_SOCIAL_API_BASE_FACEBOOK", provider2.URL)
	if _, err := eng2.Bus.Emit("PUBLISH", map[string]interface{}{"platform": "facebook", "text": "post-restart"}); err != nil {
		t.Fatalf("publish after restore failed: %v", err)
	}
	if socialState(t, eng2, "SOCIAL_PUBLISHED")["social_post_id"] != "fb_post_1" {
		t.Fatal("restored account did not publish")
	}
}

func TestSocialVaultWrongKeySkipsLoudly(t *testing.T) {
	engine.SocialReset()
	t.Setenv("SOCIAL_FACEBOOK_PAGE_ID", "pg_123")

	dbPath := filepath.Join(t.TempDir(), "spine.db")

	provider := startTestProvider(t, 1)
	t.Setenv("SPINE_SOCIAL_API_BASE_FACEBOOK", provider.URL)
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_ID", "cid_123")
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_SECRET", "sec_456")
	t.Setenv("SPINE_PUBLIC_URL", "https://social.example.com")

	// Store under key A.
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "key-A")
	eng1 := vaultEngine(t, dbPath)
	if _, err := eng1.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{
		"platform":     "facebook",
		"access_token": "token-under-key-A",
	}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	eng1.Close()

	// Reopen with key B — restore must skip, not crash.
	engine.SocialReset()
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "key-B")
	eng2 := vaultEngine(t, dbPath)
	if _, ok := eng2.Bus.SocialConnections()["facebook"]; ok {
		t.Fatal("account restored under the WRONG key — encryption broken")
	}
}

func TestSocialVaultDisconnectDeletesRow(t *testing.T) {
	engine.SocialReset()
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "disconnect-test")

	dbPath := filepath.Join(t.TempDir(), "spine.db")
	eng1 := vaultEngine(t, dbPath)
	if _, err := eng1.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{
		"platform":     "facebook",
		"access_token": "doomed-token",
	}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	if _, err := eng1.Bus.Emit("DISCONNECT", map[string]interface{}{"platform": "facebook"}); err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}
	eng1.Close()

	engine.SocialReset()
	eng2 := vaultEngine(t, dbPath)
	if _, ok := eng2.Bus.SocialConnections()["facebook"]; ok {
		t.Fatal("disconnected account resurrected from vault — delete failed")
	}
}

func TestSocialVaultNoPlaintextOnDisk(t *testing.T) {
	engine.SocialReset()
	t.Setenv("SPINE_SOCIAL_VAULT_KEY", "plaintext-check")

	dbPath := filepath.Join(t.TempDir(), "spine.db")
	eng := vaultEngine(t, dbPath)
	defer engine.SocialReset()
	if _, err := eng.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{
		"platform":     "facebook",
		"access_token": "SUPER-SECRET-PLAINTEXT-TOKEN-123",
	}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SUPER-SECRET-PLAINTEXT-TOKEN-123") {
		t.Fatal("plaintext token found in DB file — encryption not applied")
	}
}
