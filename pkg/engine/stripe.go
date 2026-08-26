package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// stripe.checkout — create a Stripe Checkout Session from a route step.
//
// This is a tier-3 action (manifest spine_version: 3) because it moves
// money: it calls out to Stripe with a live secret key.
//
// Environment configuration:
//
//	STRIPE_SECRET_KEY  sk_test_…/sk_live_…   (unset ⇒ action is a silent no-op,
//	                                          matching email/webhook dev semantics)
//	STRIPE_API_BASE    override for tests & proxies (default https://api.stripe.com)
//
// Step config:
//
//	order_id     required — becomes client_reference_id, echoed back by the
//	             webhook so the capture can be reconciled to the order
//	amount       required — order total in DOLLARS; converted to integer cents
//	             here (Stripe's unit). The client never supplies cents.
//	currency     optional — default "usd"
//	description  optional — line-item name shown on the hosted page
//	customer_email optional — pre-fills the checkout email field
//	success_url / cancel_url required — Stripe redirects after payment
//
// On success the payload gains `checkout_url` (hosted page to redirect the
// shopper to) and `checkout_session_id` (cs_…) for ledger correlation.

const stripeDefaultAPIBase = "https://api.stripe.com"

// Runtime-configurable Stripe credentials, set via the stripe.connect action
// (admin panel "Connect Stripe"). Environment wins: STRIPE_SECRET_KEY /
// SPINE_WEBHOOK_SECRET_STRIPE keep precedence so ops-level configuration can
// never be silently overridden from the UI. Values are ephemeral — they live
// until process restart, which keeps them out of the database entirely.
var (
	stripeSecretOverride     atomic.Pointer[string]
	stripeWebhookOverride    atomic.Pointer[string]
	stripeConnectedLabel     atomic.Pointer[string] // masked hint for dashboards
)

// SetStripeSecret installs a runtime secret key for stripe.checkout and a
// matching webhook signing secret. Empty webhook secret leaves current value.
func (b *Bus) SetStripeSecret(secret, webhookSecret string) {
	if secret != "" {
		s := secret
		stripeSecretOverride.Store(&s)
		label := maskStripeKey(secret)
		stripeConnectedLabel.Store(&label)
	}
	if webhookSecret != "" {
		w := webhookSecret
		stripeWebhookOverride.Store(&w)
	}
}

// StripeConnection reports whether checkout is configured and returns a
// masked label (mode + last 4 chars) safe for dashboard display.
func (b *Bus) StripeConnection() (connected bool, label string) {
	if l := stripeConnectedLabel.Load(); l != nil {
		return true, *l
	}
	if s := os.Getenv("STRIPE_SECRET_KEY"); strings.HasPrefix(s, "sk_") {
		return true, maskStripeKey(s)
	}
	return false, ""
}

// stripeActiveSecret resolves the effective secret key: env first, then the
// runtime override installed by stripe.connect.
func stripeActiveSecret() string {
	if s := os.Getenv("STRIPE_SECRET_KEY"); s != "" {
		return s
	}
	if p := stripeSecretOverride.Load(); p != nil {
		return *p
	}
	return ""
}

// stripeActiveWebhookSecret mirrors stripeActiveSecret for signing secrets.
func stripeActiveWebhookSecret() string {
	for _, env := range []string{"SPINE_WEBHOOK_SECRET_STRIPE", "STRIPE_WEBHOOK_SECRET"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	if p := stripeWebhookOverride.Load(); p != nil {
		return *p
	}
	return ""
}

func maskStripeKey(key string) string {
	mode := "live"
	if strings.Contains(key[:min(8, len(key))], "test") || !strings.HasPrefix(key, "sk_live") {
		mode = "test"
	}
	tail := key
	if len(key) > 4 {
		tail = key[len(key)-4:]
	}
	return mode + " ••••" + tail
}

// stripeConnect implements the `stripe.connect` action: install runtime
// credentials carried by an admin event.
//
//	payload: { stripe_secret: "sk_test_…", webhook_secret?: "whsec_…" }
//
// Never logs the values. Emits nothing itself; the manifest route broadcasts
// a masked-only connected state afterwards.
func (b *Bus) stripeConnect(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	connecting := strings.ToLower(ResolveVariables(step.Config["mode"], eventName, payload)) != "disconnect"

	var secret, hook string
	if connecting {
		secret = strings.TrimSpace(ResolveVariables("$event.payload.stripe_secret", eventName, payload))
		hook = strings.TrimSpace(ResolveVariables("$event.payload.webhook_secret", eventName, payload))
		if !strings.HasPrefix(secret, "sk_test_") && !strings.HasPrefix(secret, "sk_live_") &&
			!strings.HasPrefix(secret, "rk_") {
			return fmt.Errorf("stripe.connect requires payload 'stripe_secret' starting with sk_test_/sk_live_/rk_")
		}
	}

	b.SetStripeSecret(secret, hook)

	if connecting {
		log.Printf("[stripe] runtime credentials connected (%s)", maskStripeKey(secret))
	} else {
		stripeSecretOverride.Store(nil)
		stripeConnectedLabel.Store(nil)
		log.Printf("[stripe] runtime credentials disconnected")
	}
	return nil
}

// stripeCheckout implements the `stripe.checkout` action.
func (b *Bus) stripeCheckout(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	secret := stripeActiveSecret()
	if secret == "" {
		log.Printf("[stripe] STRIPE_SECRET_KEY not set — stripe.checkout skipped (payments disabled)")
		return nil
	}

	orderID := ResolveVariables(step.Config["order_id"], eventName, payload)
	if orderID == "" {
		return fmt.Errorf("stripe.checkout requires 'order_id' config")
	}
	amountDollars, err := resolveFloat(step.Config["amount"], eventName, payload)
	if err != nil {
		return fmt.Errorf("stripe.checkout invalid 'amount': %w", err)
	}
	if amountDollars <= 0 || math.IsNaN(amountDollars) || math.IsInf(amountDollars, 0) {
		return fmt.Errorf("stripe.checkout 'amount' must be positive, got %v", amountDollars)
	}
	// Exact integer dollars → exact cents (no float rounding). Fractional
	// amounts still go through float conversion — Stripe's unit is cents and
	// a rounding error on a float dollar amount is at most a cent.
	var cents int64
	rawAmount := strings.TrimSpace(step.Config["amount"])
	if isPlainNumber(rawAmount) && !strings.Contains(rawAmount, ".") {
		if dollarsInt, perr := strconv.ParseInt(rawAmount, 10, 64); perr == nil {
			cents = dollarsInt * 100
		}
	}
	if cents == 0 {
		cents = int64(math.Round(amountDollars * 100))
	}

	successURL := ResolveVariables(step.Config["success_url"], eventName, payload)
	cancelURL := ResolveVariables(step.Config["cancel_url"], eventName, payload)
	for name, u := range map[string]string{"success_url": successURL, "cancel_url": cancelURL} {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			// Common cause: $env.STORE_PUBLIC_URL unset → relative path.
			return fmt.Errorf("stripe.checkout '%s' must be an absolute http(s) URL (got %q) — is its environment variable set?", name, u)
		}
		// Open-redirect guard: post-payment redirect targets must not be
		// client-controlled. Payload-derived URLs are only accepted when the
		// resolved host is in STRIPE_ALLOWED_REDIRECT_HOSTS (comma-separated).
		cfgName := map[string]string{"success_url": "success_url", "cancel_url": "cancel_url"}[name]
		if strings.Contains(step.Config[cfgName], "$event.payload") {
			parsed, perr := url.Parse(u)
			if perr != nil || !redirectHostAllowed(parsed.Hostname()) {
				return fmt.Errorf("stripe.checkout '%s' is derived from the client payload — refusing to redirect there (open-redirect risk); configure it in the manifest or allowlist the host via STRIPE_ALLOWED_REDIRECT_HOSTS", name)
			}
		}
	}

	currency := strings.ToLower(ResolveVariables(step.Config["currency"], eventName, payload))
	if currency == "" {
		currency = "usd"
	}
	description := ResolveVariables(step.Config["description"], eventName, payload)
	if description == "" {
		description = "Order " + orderID
	}

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", orderID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", currency)
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(cents, 10))
	form.Set("line_items[0][price_data][product_data][name]", description)
	if email := ResolveVariables(step.Config["customer_email"], eventName, payload); email != "" {
		form.Set("customer_email", email)
	}

	sessionID, checkoutURL, err := stripeCreateSession(secret, form)
	if err != nil {
		return fmt.Errorf("stripe.checkout failed: %w", err)
	}

	payload["checkout_session_id"] = sessionID
	payload["checkout_url"] = checkoutURL
	payload["checkout_amount_cents"] = cents
	log.Printf("[stripe] checkout session %s created for order %s (%.2f %s)", sessionID, orderID, amountDollars, currency)
	return nil
}

// redirectHostAllowed reports whether host is in the STRIPE_ALLOWED_REDIRECT_HOSTS
// allowlist (comma-separated, hostnames only). An unset allowlist allows nothing.
func redirectHostAllowed(host string) bool {
	if host == "" {
		return false
	}
	for _, h := range strings.Split(os.Getenv("STRIPE_ALLOWED_REDIRECT_HOSTS"), ",") {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	return false
}

// stripeCreateSession posts the form-encoded Sessions request and extracts
// id + url from the response.
func stripeCreateSession(secret string, form url.Values) (sessionID, checkoutURL string, err error) {
	apiBase := os.Getenv("STRIPE_API_BASE")
	if apiBase == "" {
		apiBase = stripeDefaultAPIBase
	}

	req, err := http.NewRequest(http.MethodPost, apiBase+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Idempotency: keyed by client_reference_id (order_id) so execStep
	// retries or client re-emits for the same order never create duplicate
	// Checkout Sessions.
	req.Header.Set("Idempotency-Key", "spine-order-"+form.Get("client_reference_id"))

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize))
	if err != nil {
		return "", "", err
	}

	var parsed struct {
		ID  string `json:"id"`
		URL string `json:"url"`
		Err struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("unexpected response (HTTP %d): %.200s", resp.StatusCode, string(body))
	}
	if resp.StatusCode >= 400 || parsed.Err.Message != "" {
		return "", "", fmt.Errorf("Stripe returned %d: %s", resp.StatusCode, parsed.Err.Message)
	}
	if parsed.ID == "" || parsed.URL == "" {
		return "", "", fmt.Errorf("Stripe response missing id/url (HTTP %d)", resp.StatusCode)
	}
	return parsed.ID, parsed.URL, nil
}
