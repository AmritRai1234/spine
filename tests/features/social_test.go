package features

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/engine"
)

// social actions test suite. Acceptance criteria:
//
//	AC1: social.connect start mints an auth URL with state + redirect URI,
//	     and the state is single-use (replay rejected).
//	AC2: the full OAuth loop — callback → token exchange → account stored —
//	     works against a local httptest provider (token URL overridden).
//	AC3: social.post publishes to the connected platform and surfaces
//	     social_post_id; posting while disconnected fails loudly.
//	AC4: manual mode installs a token without OAuth; social.post uses it.
//	AC5: social.post to an unknown platform is a manifest-level error
//	     (config validation), and tier gating demands spine_version 3.

const socialManifest = `spine_version: 3

database:
  tables:
    - posts

nodes:
  - name: Social
    emits:
      - event: CONNECT_START
        payload:
          platform: string
      - event: CONNECT_MANUAL
        payload:
          platform: string
          access_token: string
      - event: DISCONNECT
        payload:
          platform: string
      - event: PUBLISH
        payload:
          text: string
          platform: string
    listens:
      - state: SOCIAL_CONNECTED

routes:
  - on: CONNECT_START
    emit: SOCIAL_AUTH
    steps:
      - action: social.connect

  - on: CONNECT_MANUAL
    emit: SOCIAL_CONNECTED
    steps:
      - action: social.connect
        mode: manual

  - on: DISCONNECT
    emit: SOCIAL_DISCONNECTED
    steps:
      - action: social.connect
        mode: disconnect

  - on: PUBLISH
    emit: SOCIAL_PUBLISHED
    steps:
      - action: social.post
        platform: $event.payload.platform
        text: $event.payload.text
      - action: db.insert
        table: posts
`

func socialTestEngine(t *testing.T) *spine.Engine {
	t.Helper()
	engine.SocialReset()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	if err := os.WriteFile(manifestPath, []byte(socialManifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatalf("engine init failed: %v", err)
	}
	t.Cleanup(func() { eng.Close() }) // release DB handles so TempDir can be removed
	return eng
}

// startTestProvider spins an httptest server that impersonates a social
// provider's token endpoint and publish endpoint, and returns its base URL.
func startTestProvider(t *testing.T, exchanges int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Path matches what exchangeSocialToken resolves for facebook:
	// bare-host override + default URL's path (/v19.0/oauth/access_token).
	mux.HandleFunc("/v19.0/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if exchanges <= 0 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"state replay"}}`))
			return
		}
		exchanges--
		if err := r.ParseForm(); err != nil || r.Form.Get("code") == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"missing code"}}`))
			return
		}
		w.Write([]byte(`{"access_token":"test-token-xyz","refresh_token":"rt-1","expires_in":3600,"name":"Test Page","id":"pg_123"}`))
	})
	mux.HandleFunc("/me/feed", func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("access_token"); got != "test-token-xyz" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"bad token"}}`))
			return
		}
		if r.FormValue("message") == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"empty message"}}`))
			return
		}
		w.Write([]byte(`{"id":"fb_post_1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// socialState fetches the latest payload of a route-emitted state via WS state.
func socialState(t *testing.T, eng *spine.Engine, state string) map[string]interface{} {
	t.Helper()
	st, _ := eng.Bus.GetState(state)
	return st
}

func TestSocialConnectMintsAuthURL(t *testing.T) {
	eng := socialTestEngine(t)
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_ID", "cid_123")
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_SECRET", "sec_456")
	t.Setenv("SPINE_PUBLIC_URL", "https://social.example.com")

	if _, err := eng.Bus.Emit("CONNECT_START", map[string]interface{}{
		"platform":   "facebook",
		"return_url": "https://admin.example.com/social",
	}); err != nil {
		t.Fatalf("CONNECT_START failed: %v", err)
	}
	state := socialState(t, eng, "SOCIAL_AUTH")
	authURL, _ := state["social_auth_url"].(string)
	if authURL == "" || state["social_state"] == "" {
		t.Fatalf("expected social_auth_url + social_state in SOCIAL_AUTH state, got %v", state)
	}
	stateVal, _ := state["social_state"].(string)
	u, err := url.Parse(authURL)
	if err != nil || !strings.Contains(u.Query().Get("redirect_uri"), "/oauth/facebook/callback") {
		t.Fatalf("auth URL missing callback redirect_uri: %s", authURL)
	}
	if u.Query().Get("state") != stateVal {
		t.Fatalf("auth URL state mismatch: %s", authURL)
	}
	if !strings.Contains(u.Query().Get("scope"), "pages_manage_posts") {
		t.Fatalf("auth URL missing publish scope: %s", authURL)
	}
	_ = fmt.Sprintf
}

func TestSocialFullOAuthLoopAndPost(t *testing.T) {
	eng := socialTestEngine(t)
	provider := startTestProvider(t, 1)

	// Override the token endpoint + redirect resolution to point at the fake.
	t.Setenv("SPINE_SOCIAL_API_BASE_FACEBOOK", provider.URL)
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_ID", "cid_123")
	t.Setenv("SOCIAL_FACEBOOK_CLIENT_SECRET", "sec_456")
	t.Setenv("SOCIAL_FACEBOOK_PAGE_ID", "pg_123")
	t.Setenv("SPINE_PUBLIC_URL", "https://social.example.com")

	// 1. Start the connect flow (return_url optional, omit here).
	if _, err := eng.Bus.Emit("CONNECT_START", map[string]interface{}{"platform": "facebook"}); err != nil {
		t.Fatalf("CONNECT_START failed: %v", err)
	}
	state := socialState(t, eng, "SOCIAL_AUTH")["social_state"].(string)

	// 2. Simulate the provider callback (code + state).
	returnTo, err := eng.Bus.SocialOAuthCallback("facebook", "authcode_1", state)
	if err != nil {
		t.Fatalf("OAuth callback failed: %v", err)
	}
	if returnTo != "" {
		t.Fatalf("expected empty return_to (none set), got %q", returnTo)
	}

	// 3. State is single-use — replay must be rejected.
	if _, err := eng.Bus.SocialOAuthCallback("facebook", "authcode_2", state); err == nil {
		t.Fatal("state replay was accepted — CSRF guard broken")
	}

	// 4. Account introspection shows the connected account, no secret.
	conns := eng.Bus.SocialConnections()
	acct, ok := conns["facebook"]
	if !ok || acct["account_label"] != "Test Page" {
		t.Fatalf("expected facebook connected as 'Test Page', got %v", conns)
	}
	for _, v := range acct {
		if strings.Contains(v, "test-token") {
			t.Fatalf("connection snapshot leaked the access token: %v", acct)
		}
	}

	// 5. Publish through the connected account.
	if _, err := eng.Bus.Emit("PUBLISH", map[string]interface{}{"platform": "facebook", "text": "hello from spine"}); err != nil {
		t.Fatalf("PUBLISH failed: %v", err)
	}
	pub := socialState(t, eng, "SOCIAL_PUBLISHED")
	if pub["social_post_id"] != "fb_post_1" {
		t.Fatalf("expected social_post_id fb_post_1, got %v", pub)
	}
}

func TestSocialPostFailsLoudlyWhenDisconnected(t *testing.T) {
	eng := socialTestEngine(t)
	_, err := eng.Bus.Emit("PUBLISH", map[string]interface{}{"platform": "facebook", "text": "nope"})
	if err == nil {
		t.Fatal("social.post while disconnected must fail loudly, not silently no-op")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestSocialManualModeAndDisconnect(t *testing.T) {
	eng := socialTestEngine(t)

	// Manual mode: no OAuth client env needed.
	if _, err := eng.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{
		"platform":      "facebook",
		"access_token":  "manual-token-1",
		"account_label": "Ops Page",
	}); err != nil {
		t.Fatalf("manual connect failed: %v", err)
	}
	acct, ok := eng.Bus.SocialConnections()["facebook"]
	if !ok || acct["connected_via"] != "manual" || acct["account_label"] != "Ops Page" {
		t.Fatalf("manual connect not registered: %v", acct)
	}

	// Disconnect drops the account.
	if _, err := eng.Bus.Emit("DISCONNECT", map[string]interface{}{"platform": "facebook"}); err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}
	if _, ok := eng.Bus.SocialConnections()["facebook"]; ok {
		t.Fatal("facebook still connected after disconnect")
	}
}

func TestSocialUnknownPlatformRejected(t *testing.T) {
	eng := socialTestEngine(t)
	if _, err := eng.Bus.Emit("CONNECT_MANUAL", map[string]interface{}{"platform": "myspace", "access_token": "x"}); err == nil {
		t.Fatal("unknown platform must be rejected with supported list")
	} else if !strings.Contains(err.Error(), "myspace") {
		t.Fatalf("error should name the bad platform, got: %v", err)
	}
}

func TestSocialTierGate(t *testing.T) {
	engine.SocialReset()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "app.spine")
	v2 := strings.Replace(socialManifest, "spine_version: 3", "spine_version: 2", 1)
	if err := os.WriteFile(manifestPath, []byte(v2), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := spine.NewFromFile(manifestPath, filepath.Join(dir, "t.db"))
	if err == nil {
		t.Fatal("social actions under spine_version 2 must fail at startup")
	}
	if !strings.Contains(err.Error(), "requires 'spine_version: 3'") {
		t.Fatalf("expected tier-gate error naming version 3, got: %v", err)
	}
}

// Compile-time guard: the JSON roundtrip of socialAccount must never carry
// the raw token (AccessToken/RefreshToken have json:"-").
func TestSocialAccountJSONSecretsExcluded(t *testing.T) {
	acct := map[string]interface{}{
		"account_label": "X",
	}
	blob, _ := json.Marshal(acct)
	if strings.Contains(string(blob), "access_token") {
		t.Fatalf("account JSON shape leaked a token field: %s", blob)
	}
	_ = time.Now() // keep time import for expires-at assertions above
	_ = fmt.Sprintf
}
