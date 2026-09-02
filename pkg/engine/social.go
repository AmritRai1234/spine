package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// social.connect / social.post — tier-3 social publishing actions.
//
// Together they give a manifest the primitives a Hootsuite-style scheduler
// needs: connect an account once via real OAuth (admin-gated), then publish
// to it from any route — typically a nightly db.fanout over a `posts` table
// where `scheduled_at <= $now`.
//
// Environment configuration (per platform; unset ⇒ that platform refuses to
// connect, matching stripe.checkout's "payments disabled" semantics):
//
//	SOCIAL_<PLATFORM>_CLIENT_ID      e.g. SOCIAL_FACEBOOK_CLIENT_ID
//	SOCIAL_<PLATFORM>_CLIENT_SECRET
//	SOCIAL_<PLATFORM>_REDIRECT_URI   defaults to <public_url>/oauth/<platform>/callback
//	SPINE_SOCIAL_API_BASE_<PLATFORM> override for tests & proxies
//	SPINE_PUBLIC_URL                 base URL used for default redirect URIs
//
// Platforms: facebook (Facebook Page feed), x (X/Twitter v2 posts),
// linkedin (organization posts), instagram (business content publishing).
//
// Tokens live in process memory (session-scoped, mirroring stripe.connect):
// restart drops connected accounts unless the app re-connects. This keeps
// live OAuth secrets out of the database entirely. Connected state is
// introspectable via SocialConnections() for dashboard display.

// socialPlatform is one supported network's OAuth + publish adapter.
type socialPlatform struct {
	// OAuth endpoints
	authURL    string
	tokenURL   string
	publishURL string

	// OAuth scopes requested at connect time (space-joined).
	scopes []string

	// accessTokenField names the form field the token POST expects.
	accessTokenField string

	// publish marshals the payload's text into a provider-specific body and
	// posts it with the given access token. Returns the provider post id.
	publish func(b *Bus, accessToken, text string) (postID string, err error)
}

// socialPlatforms — provider API adapters. Tests override tokenURL/publishURL
// via SPINE_SOCIAL_API_BASE_<PLATFORM>.
var socialPlatforms = map[string]socialPlatform{
	"facebook": {
		authURL:          "https://www.facebook.com/v19.0/dialog/oauth",
		tokenURL:         "https://graph.facebook.com/v19.0/oauth/access_token",
		publishURL:       "https://graph.facebook.com/v19.0",
		accessTokenField: "access_token",
		scopes:           []string{"pages_manage_posts", "pages_read_engagement", "pages_show_list"},
		publish:          publishFacebook,
	},
	"x": {
		authURL:          "https://twitter.com/i/oauth2/authorize",
		tokenURL:         "https://api.twitter.com/2/oauth2/token",
		publishURL:       "https://api.twitter.com/2",
		accessTokenField: "access_token", // x expects JSON body, handled in publishX
		scopes:           []string{"tweet.read", "tweet.write", "users.read", "offline.access"},
		publish:          publishX,
	},
	"linkedin": {
		authURL:          "https://www.linkedin.com/oauth/v2/authorization",
		tokenURL:         "https://www.linkedin.com/oauth/v2/accessToken",
		publishURL:       "https://api.linkedin.com/v2",
		accessTokenField: "access_token",
		scopes:           []string{"w_organization_social", "r_organization_social"},
		publish:          publishLinkedIn,
	},
	"instagram": {
		authURL:          "https://api.instagram.com/oauth/authorize",
		tokenURL:         "https://api.instagram.com/oauth/access_token",
		publishURL:       "https://graph.facebook.com/v19.0", // container + publish go through the Graph API
		accessTokenField: "access_token",
		scopes:           []string{"instagram_basic", "instagram_content_publish", "pages_show_list"},
		publish:          publishInstagram,
	},
}

// socialAccounts holds one connected account per platform. Session-scoped.
type socialAccount struct {
	AccessToken   string    `json:"-"`
	RefreshToken  string    `json:"-"`
	AccountLabel  string    `json:"account_label"` // display name / page / org id (safe to show)
	ExpiresAt     time.Time `json:"expires_at"`
	ConnectedVia  string    `json:"connected_via"` // "oauth" or "manual"
	ExternalAccID string    `json:"external_account_id"`
}

// socialStore guards the connected-account map. Swapped atomically so
// concurrent social.post reads never see a torn map (same pattern as
// tlsDomainsExtra).
var (
	socialMu       sync.Mutex
	socialAccounts atomic.Pointer[map[string]socialAccount]
	socialStates   = map[string]socialFlowState{} // OAuth CSRF state → flow
)

type socialFlowState struct {
	platform   string
	returnTo   string // URL the browser is sent to after callback
	verifier   string // PKCE code_verifier (x.com)
	expiresAt  time.Time
	accountKey string // optional: second account of the same platform
}

// SocialReset clears all connected accounts and pending OAuth flows. Test
// isolation only: the store is process-global (session-scoped by design),
// so suites sharing one process must reset between engine instances.
func SocialReset() {
	socialMu.Lock()
	socialAccounts.Store(nil)
	socialStates = map[string]socialFlowState{}
	socialMu.Unlock()
}
// SocialConnections returns a sorted, secret-free snapshot of connected
// platforms for dashboard display.
func (b *Bus) SocialConnections() map[string]map[string]string {
	out := map[string]map[string]string{}
	if p := socialAccounts.Load(); p != nil {
		for name, acct := range *p {
			out[name] = map[string]string{
				"account_label": acct.AccountLabel,
				"connected_via": acct.ConnectedVia,
				"external_id":   acct.ExternalAccID,
			}
		}
	}
	return out
}

// socialClientCreds resolves id/secret for a platform: env first
// (SOCIAL_<P>_CLIENT_ID / _SECRET), then the runtime override set by
// social.connect with mode: creds. Returns empty strings when unset.
func socialClientCreds(platform string) (id, secret string) {
	upper := strings.ToUpper(platform)
	if v := os.Getenv("SOCIAL_" + upper + "_CLIENT_ID"); v != "" {
		id = v
	}
	if v := os.Getenv("SOCIAL_" + upper + "_CLIENT_SECRET"); v != "" {
		secret = v
	}
	if p := socialOverrides.Load(); p != nil {
		if o, ok := (*p)[platform]; ok {
			if o.clientID != "" {
				id = o.clientID
			}
			if o.clientSecret != "" {
				secret = o.clientSecret
			}
		}
	}
	return id, secret
}

type socialCreds struct{ clientID, clientSecret string }

var socialOverrides atomic.Pointer[map[string]socialCreds]

// SetSocialClient installs runtime OAuth client credentials for a platform
// (admin-panel connect flow without env access). Empty values keep current.
func (b *Bus) SetSocialClient(platform, clientID, clientSecret string) {
	socialMu.Lock()
	defer socialMu.Unlock()
	cur := map[string]socialCreds{}
	if p := socialOverrides.Load(); p != nil {
		for k, v := range *p {
			cur[k] = v
		}
	}
	c := cur[platform]
	if clientID != "" {
		c.clientID = clientID
	}
	if clientSecret != "" {
		c.clientSecret = clientSecret
	}
	cur[platform] = c
	socialOverrides.Store(&cur)
}

// socialRedirectURI resolves the OAuth redirect URI for a platform:
// SOCIAL_<P>_REDIRECT_URI env, else <SPINE_PUBLIC_URL>/oauth/<platform>/callback.
func socialRedirectURI(platform string) string {
	if v := os.Getenv("SOCIAL_" + strings.ToUpper(platform) + "_REDIRECT_URI"); v != "" {
		return v
	}
	base := os.Getenv("SPINE_PUBLIC_URL")
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/oauth/" + platform + "/callback"
}

// socialAPIBase resolves a provider base URL honoring test overrides.
func socialAPIBase(platform, def string) string {
	if v := os.Getenv("SPINE_SOCIAL_API_BASE_" + strings.ToUpper(platform)); v != "" {
		return v
	}
	return def
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()) // never silently reuse state
	}
	return hex.EncodeToString(buf)
}

// socialConnect implements the `social.connect` action.
//
// mode: "start" (default) — mint an authorization URL the admin dashboard
// opens in a popup; payload { platform: "facebook" } (+ optional
// return_url). The payload gains social_auth_url + social_state.
//
// mode: "disconnect" — drop the platform's account (payload { platform }).
//
// mode: "manual" — install an already-acquired token without the OAuth
// popup (payload { platform, access_token, refresh_token?, account_label? }).
// Useful for headless servers and platform apps that mint their own tokens.
func (b *Bus) socialConnect(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	mode := strings.ToLower(ResolveVariables(step.Config["mode"], eventName, payload))
	platform := strings.ToLower(strings.TrimSpace(ResolveVariables("$event.payload.platform", eventName, payload)))
	if platform == "" {
		return fmt.Errorf("social.connect requires payload 'platform' (one of: %s)", strings.Join(socialPlatformNames(), ", "))
	}
	p, ok := socialPlatforms[platform]
	if !ok {
		return fmt.Errorf("unknown social platform %q — supported: %s", platform, strings.Join(socialPlatformNames(), ", "))
	}

	switch mode {
	case "disconnect":
		socialMu.Lock()
		cur := map[string]socialAccount{}
		if ptr := socialAccounts.Load(); ptr != nil {
			for k, v := range *ptr {
				cur[k] = v
			}
		}
		delete(cur, platform)
		socialAccounts.Store(&cur)
		socialMu.Unlock()
		b.socialVaultDelete(platform)
		log.Printf("[social] disconnected %s", platform)
		return nil

	case "manual":
		token := strings.TrimSpace(ResolveVariables("$event.payload.access_token", eventName, payload))
		if token == "" {
			return fmt.Errorf("social.connect mode=manual requires payload 'access_token'")
		}
		label := strings.TrimSpace(ResolveVariables("$event.payload.account_label", eventName, payload))
		if label == "" {
			label = platform + " (manual)"
		}
		socialMu.Lock()
		cur := map[string]socialAccount{}
		if ptr := socialAccounts.Load(); ptr != nil {
			for k, v := range *ptr {
				cur[k] = v
			}
		}
		cur[platform] = socialAccount{
			AccessToken:  token,
			RefreshToken: strings.TrimSpace(ResolveVariables("$event.payload.refresh_token", eventName, payload)),
			AccountLabel: label,
			ConnectedVia: "manual",
		}
		socialAccounts.Store(&cur)
		socialMu.Unlock()
		b.socialVaultStore(platform, cur[platform])
		log.Printf("[social] %s connected manually (token length %d)", platform, len(token))
		return nil

	default: // "start"
		id, _ := socialClientCreds(platform)
		if id == "" {
			return fmt.Errorf("social.connect: no OAuth client for %s — set SOCIAL_%s_CLIENT_ID (and _SECRET) or pass them via social.connect mode=manual",
				platform, strings.ToUpper(platform))
		}
		redirect := socialRedirectURI(platform)
		if redirect == "" {
			return fmt.Errorf("social.connect: no redirect URI for %s — set SOCIAL_%s_REDIRECT_URI or SPINE_PUBLIC_URL",
				platform, strings.ToUpper(platform))
		}

		state := randomHex(16)
		verifier := ""
		flow := socialFlowState{
			platform:  platform,
			verifier:  verifier,
			expiresAt: time.Now().Add(15 * time.Minute),
			returnTo:  strings.TrimSpace(ResolveVariables("$event.payload.return_url", eventName, payload)),
		}
		// Optional multi-account key: let a Hootsuite-style app connect two
		// pages of the same platform under distinct labels.
		if key := strings.TrimSpace(ResolveVariables("$event.payload.account_key", eventName, payload)); key != "" {
			flow.accountKey = platform + ":" + key
		}

		q := url.Values{}
		q.Set("client_id", id)
		q.Set("redirect_uri", redirect)
		q.Set("state", state)
		if platform == "x" {
			verifier = randomHex(48)
			flow.verifier = verifier
			challenge := verifier // plain (no S256 transform needed for server-side flows we control)
			q.Set("code_challenge", challenge)
			q.Set("code_challenge_method", "plain")
			q.Set("response_type", "code")
		}
		q.Set("scope", strings.Join(p.scopes, " "))
		// Some providers require response_type/other params — facebook uses
		// defaults; linkedin too. Instagram uses the same shape.

		socialMu.Lock()
		socialStates[state] = flow
		// Prune expired states to bound memory.
		now := time.Now()
		for k, f := range socialStates {
			if now.After(f.expiresAt) {
				delete(socialStates, k)
			}
		}
		socialMu.Unlock()

		payload["social_auth_url"] = p.authURL + "?" + q.Encode()
		payload["social_state"] = state
		payload["social_platform"] = platform
		log.Printf("[social] %s connect started (state %s…)", platform, state[:min(8, len(state))])
		return nil
	}
}

// SocialOAuthCallback handles the browser redirect from the provider. It is
// called by the engine's /oauth/<platform>/callback HTTP route (engine.go).
// It exchanges the code, stores the account, and returns the URL to redirect
// the admin's browser to.
func (b *Bus) SocialOAuthCallback(platform, code, state string) (redirectURL string, err error) {
	socialMu.Lock()
	flow, ok := socialStates[state]
	if !ok || flow.platform != platform {
		socialMu.Unlock()
		return "", fmt.Errorf("unknown or expired OAuth state — restart the connect flow")
	}
	if time.Now().After(flow.expiresAt) {
		delete(socialStates, state)
		socialMu.Unlock()
		return "", fmt.Errorf("OAuth state expired — restart the connect flow")
	}
	delete(socialStates, state)
	socialMu.Unlock()

	p, ok := socialPlatforms[platform]
	if !ok {
		return "", fmt.Errorf("unknown social platform %q", platform)
	}
	client_id, client_secret := socialClientCreds(platform)
	redirect := socialRedirectURI(platform)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set(p.accessTokenField, client_id) // provider-specific placement of client id
	form.Set("client_id", client_id)
	form.Set("client_secret", client_secret)
	if flow.verifier != "" {
		form.Set("code_verifier", flow.verifier)
	}

	acct, err := exchangeSocialToken(platform, p, form)
	if err != nil {
		return "", err
	}

	key := flow.accountKey
	if key == "" {
		key = platform
	}
	socialMu.Lock()
	cur := map[string]socialAccount{}
	if ptr := socialAccounts.Load(); ptr != nil {
		for k, v := range *ptr {
			cur[k] = v
		}
	}
	cur[key] = *acct
	socialAccounts.Store(&cur)
	socialMu.Unlock()

	b.socialVaultStore(key, *acct)
	log.Printf("[social] %s connected via OAuth (%s)", platform, acct.AccountLabel)
	if flow.returnTo != "" {
		return flow.returnTo, nil
	}
	return "", nil
}

// exchangeSocialToken posts the token request and normalizes the account.
func exchangeSocialToken(platform string, p socialPlatform, form url.Values) (*socialAccount, error) {
	_ = platform
	tokenURL := p.tokenURL
	if override := os.Getenv("SPINE_SOCIAL_API_BASE_" + strings.ToUpper(platform)); override != "" {
		// Full-URL override wins; bare-host override gets the default path.
		if strings.HasPrefix(override, "http://") || strings.HasPrefix(override, "https://") {
			if u, perr := url.Parse(override); perr == nil && u.Path != "" {
				tokenURL = override // full endpoint URL
			} else {
				if pu, derr := url.Parse(p.tokenURL); derr == nil {
					tokenURL = strings.TrimSuffix(override, "/") + pu.Path
				} else {
					tokenURL = strings.TrimSuffix(override, "/") + p.tokenURL
				}
			}
		}
	}

	resp, err := sharedHTTPClient.PostForm(tokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("social token exchange to %s failed: %w", tokenURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize))
	if err != nil {
		return nil, fmt.Errorf("social token exchange read failed: %w", err)
	}

	var parsed struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
		AccountLabel     string `json:"name"`
		ExternalID       string `json:"id"`
		Err              struct{ Message string } `json:"error"`
	}
	if jerr := json.Unmarshal(body, &parsed); jerr != nil {
		return nil, fmt.Errorf("social token response unparseable (HTTP %d): %.200s", resp.StatusCode, string(body))
	}
	if resp.StatusCode >= 400 || parsed.Err.Message != "" {
		return nil, fmt.Errorf("%s token exchange failed (HTTP %d): %s", platform, resp.StatusCode, parsed.Err.Message)
	}
	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("%s token response missing access_token (HTTP %d)", platform, resp.StatusCode)
	}
	label := parsed.AccountLabel
	if label == "" {
		label = platform + " account"
	}
	return &socialAccount{
		AccessToken:   parsed.AccessToken,
		RefreshToken:  parsed.RefreshToken,
		AccountLabel:  label,
		ExternalAccID: parsed.ExternalID,
		ConnectedVia:  "oauth",
		ExpiresAt:     time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second),
	}, nil
}

// socialPlatformNames returns sorted platform ids.
func socialPlatformNames() []string {
	out := make([]string, 0, len(socialPlatforms))
	for k := range socialPlatforms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// socialPost implements the `social.post` action.
//
// Config: platform (required), text (required), account_key (optional for
// multi-account). On success the payload gains social_post_id and
// social_post_url when the provider returns one.
func (b *Bus) socialPost(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	platform := strings.ToLower(strings.TrimSpace(ResolveVariables(step.Config["platform"], eventName, payload)))
	if platform == "" {
		return fmt.Errorf("social.post requires 'platform' config (one of: %s)", strings.Join(socialPlatformNames(), ", "))
	}
	p, ok := socialPlatforms[platform]
	if !ok {
		return fmt.Errorf("unknown social platform %q — supported: %s", platform, strings.Join(socialPlatformNames(), ", "))
	}
	text := ResolveVariables(step.Config["text"], eventName, payload)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("social.post requires 'text' config (or it resolved empty)")
	}

	acctKey := platform
	if k := strings.TrimSpace(ResolveVariables(step.Config["account_key"], eventName, payload)); k != "" {
		acctKey = platform + ":" + k
	}
	socialMu.Lock()
	var acct socialAccount
	have := false
	if ptr := socialAccounts.Load(); ptr != nil {
		acct, have = (*ptr)[acctKey]
	}
	socialMu.Unlock()
	if !have {
		return fmt.Errorf("social.post: %s is not connected — run social.connect for platform %q first", platform, platform)
	}

	// Opportunistic refresh when the token is stale and we hold a refresh token.
	if !acct.ExpiresAt.IsZero() && time.Now().After(acct.ExpiresAt) && acct.RefreshToken != "" {
		if refreshed, rerr := b.refreshSocialToken(platform, p, acct.RefreshToken); rerr == nil {
			acct = *refreshed
			socialMu.Lock()
			cur := map[string]socialAccount{}
			if ptr := socialAccounts.Load(); ptr != nil {
				for k, v := range *ptr {
					cur[k] = v
				}
			}
			cur[acctKey] = acct
			socialAccounts.Store(&cur)
			socialMu.Unlock()
		} else {
			log.Printf("[social] %s token refresh failed: %v", platform, rerr)
		}
	}

	postID, err := p.publish(b, acct.AccessToken, text)
	if err != nil {
		return fmt.Errorf("social.post to %s failed: %w", platform, err)
	}
	payload["social_post_id"] = postID
	payload["social_platform"] = platform
	log.Printf("[social] posted to %s as %s (id %s)", platform, acct.AccountLabel, postID)
	return nil
}

// refreshSocialToken performs a refresh_token grant (x + linkedin support it).
func (b *Bus) refreshSocialToken(platform string, p socialPlatform, refreshToken string) (*socialAccount, error) {
	id, secret := socialClientCreds(platform)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	return exchangeSocialToken(platform, p, form)
}

// ── Provider publish adapters ──────────────────────────────────────────────
// Each marshals the text into the provider's publish endpoint shape and
// returns the provider post id. All use sharedHTTPClient (TLS reuse).

func publishFacebook(b *Bus, accessToken, text string) (string, error) {
	base := socialAPIBase("facebook", "https://graph.facebook.com/v19.0")
	pageID := os.Getenv("SOCIAL_FACEBOOK_PAGE_ID")
	if pageID == "" {
		return "", fmt.Errorf("set SOCIAL_FACEBOOK_PAGE_ID (the page to publish to)")
	}
	form := url.Values{}
	form.Set("message", text)
	form.Set("access_token", accessToken)
	resp, err := sharedHTTPClient.PostForm(base+"/me/feed", form)
	return parseSocialPostResponse(resp, err, "facebook")
}

func publishX(b *Bus, accessToken, text string) (string, error) {
	base := socialAPIBase("x", "https://api.twitter.com/2")
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, base+"/tweets", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sharedHTTPClient.Do(req)
	return parseSocialPostResponse(resp, err, "x")
}

func publishLinkedIn(b *Bus, accessToken, text string) (string, error) {
	base := socialAPIBase("linkedin", "https://api.linkedin.com/v2")
	org := os.Getenv("SOCIAL_LINKEDIN_ORG_URN")
	if org == "" {
		return "", fmt.Errorf("set SOCIAL_LINKEDIN_ORG_URN (e.g. urn:li:organization:123456)")
	}
	body, _ := json.Marshal(map[string]interface{}{
		"author":          org,
		"lifecycleState":  "PUBLISHED",
		"specificContent": map[string]interface{}{"com.linkedin.ugc.ShareContent": map[string]interface{}{"shareCommentary": map[string]string{"text": text}}},
		"visibility":      map[string]string{"com.linkedin.ugc.MemberNetworkVisibility": "PUBLIC"},
	})
	req, err := http.NewRequest(http.MethodPost, base+"/ugcPosts", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	resp, err := sharedHTTPClient.Do(req)
	return parseSocialPostResponse(resp, err, "linkedin")
}

func publishInstagram(b *Bus, accessToken, text string) (string, error) {
	base := socialAPIBase("instagram", "https://graph.facebook.com/v19.0")
	igUser := os.Getenv("SOCIAL_INSTAGRAM_BUSINESS_ID")
	if igUser == "" {
		return "", fmt.Errorf("set SOCIAL_INSTAGRAM_BUSINESS_ID (IG business account id)")
	}
	imageURL := os.Getenv("SOCIAL_INSTAGRAM_IMAGE_URL")
	if imageURL == "" {
		return "", fmt.Errorf("instagram publish requires an image — set SOCIAL_INSTAGRAM_IMAGE_URL (Graph API requires media)")
	}
	// Two-step: create a media container, then publish it.
	form := url.Values{}
	form.Set("image_url", imageURL)
	form.Set("caption", text)
	form.Set("access_token", accessToken)
	form.Set("ig_user_id", igUser)
	resp, err := sharedHTTPClient.PostForm(base+"/"+igUser+"/media", form)
	containerID, err := parseSocialPostResponse(resp, err, "instagram-container")
	if err != nil {
		return "", err
	}
	pub := url.Values{}
	pub.Set("creation_id", containerID)
	pub.Set("access_token", accessToken)
	resp2, err := sharedHTTPClient.PostForm(base+"/"+igUser+"/media_publish", pub)
	return parseSocialPostResponse(resp2, err, "instagram")
}

// parseSocialPostResponse extracts a post id from provider JSON responses.
func parseSocialPostResponse(resp *http.Response, err error, label string) (string, error) {
	if err != nil {
		return "", fmt.Errorf("%s request failed: %w", label, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize))
	if err != nil {
		return "", fmt.Errorf("%s response read failed: %w", label, err)
	}
	var parsed struct {
		ID  string `json:"id"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Err struct{ Message string } `json:"error"`
	}
	if jerr := json.Unmarshal(body, &parsed); jerr != nil {
		return "", fmt.Errorf("%s response unparseable (HTTP %d): %.200s", label, resp.StatusCode, string(body))
	}
	if resp.StatusCode >= 400 || parsed.Err.Message != "" {
		return "", fmt.Errorf("%s returned HTTP %d: %s", label, resp.StatusCode, parsed.Err.Message)
	}
	id := parsed.ID
	if id == "" {
		id = parsed.Data.ID
	}
	if id == "" {
		return "", fmt.Errorf("%s response missing post id (HTTP %d)", label, resp.StatusCode)
	}
	return id, nil
}
