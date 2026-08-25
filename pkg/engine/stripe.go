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

// stripeCheckout implements the `stripe.checkout` action.
func (b *Bus) stripeCheckout(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	secret := os.Getenv("STRIPE_SECRET_KEY")
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
	cents := int64(math.Round(amountDollars * 100))

	successURL := ResolveVariables(step.Config["success_url"], eventName, payload)
	cancelURL := ResolveVariables(step.Config["cancel_url"], eventName, payload)
	for name, u := range map[string]string{"success_url": successURL, "cancel_url": cancelURL} {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			// Common cause: $env.STORE_PUBLIC_URL unset → relative path.
			return fmt.Errorf("stripe.checkout '%s' must be an absolute http(s) URL (got %q) — is its environment variable set?", name, u)
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
