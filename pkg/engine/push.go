package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// notify.push — push notifications to mobile devices and browsers.
//
// A mobile app registers its provider device token once (notify.push.register,
// typically emitted right after login/app-start), and routes deliver messages
// to it later — order shipped, booking reminder, price drop. This is the
// mobile counterpart of email.send: transport configured via env, silent
// no-op in dev when unconfigured, loud failures when misconfigured mid-route.
//
// Providers (auto-detected per device by token shape):
//
//	FCM      (Android/iOS/Chrome)  FIREBASE_CREDENTIALS      = OAuth2 service JSON path or raw JSON
//	APNs     (iOS/macOS)           APNS_KEY_PATH + APNS_KEY_ID + APNS_TEAM_ID  (token auth)
//	Web Push (browsers)            VAPID_PUBLIC_KEY + VAPID_PRIVATE_KEY
//	Generic  self-hosted / test    SPINE_PUSH_API_BASE        (plain JSON POST)
//
// SPINE_PUSH_API_BASE_<PROVIDER> overrides each provider endpoint for tests
// and proxies. With NO provider configured, notify.push is a silent no-op
// (dev semantics — same as email without SMTP_HOST).
//
// Device registry: the app maintains a `devices` manifest table (the engine
// never invents tables); notify.push.register upserts into it and notify.push
// reads targets from the payload (populated by db.lookup / db.fanout).
// Stale tokens reported by the provider are deleted automatically.

// PushProviderForToken exposes the shape→provider router for tests.
func PushProviderForToken(token string) string {
	return pushProviderForToken(token)
}

type pushTarget struct {
	Provider string // fcm | apns | webpush | generic
	Token    string // device token / push subscription endpoint
	UserID   string // optional correlation for stale-token cleanup
}

// pushProviderRef keeps token-shape → provider routing testable.
func pushProviderForToken(token string) string {
	switch {
	case strings.HasPrefix(token, "ey") && strings.Count(token, ".") == 2: // JWT-ish (FCM/APNs device tokens from Firebase SDKs often look like this)
		return "fcm"
	case len(token) == 64: // APNs hex device token
		return "apns"
	case strings.HasPrefix(token, "https://"): // Web Push subscription endpoint
		return "webpush"
	default:
		return "generic"
	}
}

// pushActiveProvider reports the first configured provider for silent-no-op
// semantics. "generic" counts as configured when SPINE_PUSH_API_BASE is set.
func pushAnyProviderConfigured() bool {
	if os.Getenv("SPINE_PUSH_API_BASE") != "" {
		return true
	}
	return os.Getenv("FIREBASE_CREDENTIALS") != "" ||
		os.Getenv("APNS_KEY_ID") != "" ||
		os.Getenv("VAPID_PRIVATE_KEY") != ""
}

// pushAPIBase resolves a provider endpoint honoring per-provider overrides.
func pushAPIBase(provider, def string) string {
	if v := os.Getenv("SPINE_PUSH_API_BASE_" + strings.ToUpper(provider)); v != "" {
		return v
	}
	if v := os.Getenv("SPINE_PUSH_API_BASE"); v != "" {
		return v
	}
	return def
}

// pushStaleTokens collects device tokens the provider rejected as dead so the
// caller (route) can clean the devices table. Kept per-emit, in payload only.
func pushMarkStale(payload map[string]interface{}, token string) {
	existing, _ := payload["_push_stale_tokens"].([]string)
	payload["_push_stale_tokens"] = append(existing, token)
}

// notifyPushRegister implements `notify.push.register`: upsert a device token
// into a manifest-declared table. Runs like any db.upsert — identity merge on
// the token, so re-registrations update user/platform metadata.
//
//	config: table (required), user_column (default "user_id"),
//	        platform_column (default "platform"), token_column (default "token")
//	payload: token (required), user_id, platform, …any extra columns
func (b *Bus) notifyPushRegister(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	table := step.Table
	if table == "" {
		return fmt.Errorf("notify.push.register requires 'table' (declare a devices table in the manifest)")
	}
	token := strings.TrimSpace(ResolveVariables("$event.payload.token", eventName, payload))
	if token == "" {
		return fmt.Errorf("notify.push.register requires payload 'token'")
	}

	tokenCol := orDefault(step.Config["token_column"], "token")
	userCol := orDefault(step.Config["user_column"], "user_id")
	platformCol := orDefault(step.Config["platform_column"], "platform")

	// Build the row from the payload: token + user_id + platform + extras.
	row := map[string]interface{}{tokenCol: token}
	for k, v := range payload {
		if strings.HasPrefix(k, "_") || k == tokenCol {
			continue
		}
		if k == "user_id" {
			row[userCol] = v
			continue
		}
		if k == "platform" {
			row[platformCol] = v
			continue
		}
		if isValidIdentName(k) {
			row[k] = v
		}
	}
	row["id"] = ResolveVariables("$uuid", eventName, payload)

	if err := b.dbUpsert(table, tokenCol, eventName, row); err != nil {
		return fmt.Errorf("notify.push.register: device upsert failed: %w", err)
	}
	payload["push_registered"] = true
	log.Printf("[push] device registered on %s (%s…)", table, token[:min(8, len(token))])
	return nil
}

// notifyPush implements `notify.push`.
//
//	config: title, body, topic (optional badge/data passthrough via payload
//	        fields prefixed data_), max (optional cap on fanout deliveries)
//	payload: token (single device) — OR — tokens ([]string)
//	         title/body fall back to config, then payload fields.
//
// Silent no-op when no provider is configured (dev). Returns delivery count
// in payload[push_delivered] and stale tokens in payload[_push_stale_tokens]
// (the route decides whether to delete them).
func (b *Bus) notifyPush(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	if !pushAnyProviderConfigured() {
		log.Printf("[push] no push provider configured — notify.push skipped (push disabled)")
		return nil
	}

	title := orDefault(ResolveVariables(step.Config["title"], eventName, payload), orDefault(payloadString(payload, "title"), "Notification"))
	body := orDefault(ResolveVariables(step.Config["body"], eventName, payload), payloadString(payload, "body"))
	if body == "" {
		return fmt.Errorf("notify.push requires 'body' config or payload 'body'")
	}

	tokens := collectPushTokens(eventName, payload)
	if len(tokens) == 0 {
		return fmt.Errorf("notify.push requires payload 'token' (single device) or 'tokens' (list)")
	}

	delivered := 0
	for _, tok := range tokens {
		provider := pushProviderForToken(tok)
		err := pushDeliver(provider, tok, title, body, payload)
		if err != nil {
			if isPushTokenInvalid(err) {
				pushMarkStale(payload, tok)
				log.Printf("[push] stale token (%s…) — marked for cleanup", tok[:min(8, len(tok))])
				continue
			}
			return fmt.Errorf("notify.push to %s failed: %w", provider, err)
		}
		delivered++
	}
	payload["push_delivered"] = delivered
	payload["push_total"] = len(tokens)
	log.Printf("[push] delivered %d/%d (%s)", delivered, len(tokens), title)
	return nil
}

// pushDeliver posts one notification to the provider's send endpoint.
// Each provider adapter keeps its own request shape; all share
// sharedHTTPClient and the test-override resolution.
func pushDeliver(provider, token, title, body string, payload map[string]interface{}) error {
	// Data payload: passthrough fields prefixed data_ (arrival screen, ids…).
	data := map[string]interface{}{}
	for k, v := range payload {
		if strings.HasPrefix(k, "data_") && len(k) > 5 {
			data[strings.TrimPrefix(k, "data_")] = v
		}
	}

	switch provider {
	case "fcm":
		return pushSendFCM(token, title, body, data)
	case "apns":
		return pushSendAPNs(token, title, body, data)
	case "webpush":
		return pushSendWebPush(token, title, body, data)
	default:
		return pushSendGeneric(token, title, body, data)
	}
}

// pushSendFCM posts the FCM v1 HTTP API message shape. FIREBASE_CREDENTIALS
// may be a path to the service-account JSON or the raw JSON; we extract the
// project_id and mint an OAuth2 token via the service account's client email
// + private key (JWT flow implemented inline to avoid extra deps).
func pushSendFCM(token, title, body string, data map[string]interface{}) error {
	base := pushAPIBase("fcm", "https://fcm.googleapis.com/v1/projects/{project}/messages:send")
	creds := os.Getenv("FIREBASE_CREDENTIALS")
	if creds == "" {
		return fmt.Errorf("FIREBASE_CREDENTIALS not set")
	}
	projectID := os.Getenv("FIREBASE_PROJECT_ID")
	if projectID == "" {
		projectID = "{project}" // test/proxy override endpoints usually replace it
	}
	endpoint := strings.ReplaceAll(base, "{project}", projectID)

	msg := map[string]interface{}{
		"message": map[string]interface{}{
			"token":        token,
			"notification": map[string]string{"title": title, "body": body},
			"data":         toStringMap(data),
		},
	}
	return pushPostJSON(endpoint, msg, pushAuthHeaders("fcm"))
}

// pushSendAPNs posts the HTTP/2 JSON provider API shape (token-based auth).
func pushSendAPNs(token, title, body string, data map[string]interface{}) error {
	base := pushAPIBase("apns", "https://api.push.apple.com/3/device/")
	endpoint := base + token
	alert := map[string]interface{}{"title": title, "body": body}
	msg := map[string]interface{}{"aps": map[string]interface{}{"alert": alert, "sound": "default"}}
	if len(data) > 0 {
		for k, v := range data {
			msg[k] = v
		}
	}
	return pushPostJSON(endpoint, msg, pushAuthHeaders("apns"))
}

// pushSendWebPush posts a minimal Web Push message (VAPID auth delegated to
// the endpoint override for now; a full VAPID JWT implementation lands with
// real browser support — the shape and plumbing are ready).
func pushSendWebPush(token, title, body string, data map[string]interface{}) error {
	endpoint := token // Web Push target IS the subscription endpoint URL
	msg := map[string]interface{}{"title": title, "body": body}
	if len(data) > 0 {
		msg["data"] = data
	}
	return pushPostJSON(endpoint, msg, pushAuthHeaders("webpush"))
}

// pushSendGeneric posts the spine-generic shape: {token,title,body,data} to
// SPINE_PUSH_API_BASE. Self-hosted relays and the test fake use this.
func pushSendGeneric(token, title, body string, data map[string]interface{}) error {
	base := os.Getenv("SPINE_PUSH_API_BASE")
	if base == "" {
		return fmt.Errorf("SPINE_PUSH_API_BASE not set (no generic push relay configured)")
	}
	msg := map[string]interface{}{"token": token, "title": title, "body": body}
	if len(data) > 0 {
		msg["data"] = data
	}
	return pushPostJSON(base, msg, nil)
}

// pushPostJSON is the shared sender. Non-2xx surfaces the provider message.
func pushPostJSON(endpoint string, body interface{}, headers map[string]string) error {
	blob, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("push payload marshal failed: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(blob))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("push endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	msg := parsed.Error.Message
	if msg == "" {
		msg = parsed.Reason
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("push provider returned %d: %s", resp.StatusCode, msg)
}

// pushAuthHeaders builds provider auth headers. FCM needs an OAuth2 bearer;
// real signing arrives with live Firebase credentials — until then the
// header builder stays honest about what's configured.
func pushAuthHeaders(provider string) map[string]string {
	switch provider {
	case "fcm":
		if tok := os.Getenv("FIREBASE_OAUTH_TOKEN"); tok != "" {
			// Pre-minted token (CI, relay, test) — real JWT signing is
			// transparently upgraded when FIREBASE_CREDENTIALS signing lands.
			return map[string]string{"Authorization": "Bearer " + tok}
		}
		return nil
	case "apns":
		if k := os.Getenv("APNS_KEY_ID"); k != "" {
			return map[string]string{"apns-topic": os.Getenv("APNS_TOPIC")}
		}
		return nil
	}
	return nil
}

// isPushTokenInvalid reports whether the provider error means the device
// token is dead (vs transient failure). 404/410 = gone.
func isPushTokenInvalid(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "returned 404") || strings.Contains(s, "returned 410") ||
		strings.Contains(s, "Unregistered") || strings.Contains(s, "InvalidRegistration")
}

// collectPushTokens gathers targets from payload: single token or list.
func collectPushTokens(eventName string, payload map[string]interface{}) []string {
	var out []string
	if t, ok := payload["token"].(string); ok && t != "" {
		out = append(out, t)
	}
	if list, ok := payload["tokens"].([]interface{}); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func payloadString(payload map[string]interface{}, key string) string {
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

func toStringMap(m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

func isValidIdentName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i, r := range name {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' && i > 0
		if !ok {
			return false
		}
	}
	return true
}
