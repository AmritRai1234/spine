package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AllowUnsignedWebhooks reports whether webhooks may pass through unsigned
// when no secret is configured (SPINE_ALLOW_UNSIGNED_WEBHOOKS=1). Read at
// request time — not cached in init() — so tests and runtime reconfiguration
// observe the current environment.
func allowUnsignedWebhooks() bool {
	return os.Getenv("SPINE_ALLOW_UNSIGNED_WEBHOOKS") == "1"
}

// SecretFunc is a function that returns the webhook secret for a provider.
type SecretFunc func(provider string) string

// WebhookVerifyMiddleware returns an HTTP middleware that verifies HMAC-SHA256
// signatures on incoming webhook requests. It reads the raw body once, verifies
// the signature, then restores r.Body for downstream handlers.
//
// Stripe format: header "Stripe-Signature" with value "t=<unix_ts>,v1=<hex>[,v1=<hex2>]"
// Generic format: header "X-Spine-Signature" with value "<ts>.<hex>"
//
// Fail-closed: if a secret is configured, unsigned or tampered requests get 401.
// If no secret is configured, returns 503 unless SPINE_ALLOW_UNSIGNED_WEBHOOKS=1.
func WebhookVerifyMiddleware(getSecret SecretFunc, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only validate POST requests that look like webhooks
		provider := extractWebhookProvider(r.URL.Path)
		if provider == "" {
			next(w, r)
			return
		}

		secret := getSecret(provider)

		// No secret configured for this provider
		if secret == "" {
			if allowUnsignedWebhooks() {
				log.Printf("[webhook] warning: unsigned webhook from provider '%s' (no secret configured, SPINE_ALLOW_UNSIGNED_WEBHOOKS=1)", provider)
				next(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "webhook_provider_not_configured",
			})
			return
		}

		// Read the raw body
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "cannot_read_body",
			})
			return
		}
		// Restore body for downstream handlers
		r.Body = io.NopCloser(bytes.NewReader(rawBody))

		// Determine signature scheme from header
		stripeSig := r.Header.Get("Stripe-Signature")
		genericSig := r.Header.Get("X-Spine-Signature")

		var valid bool

		if stripeSig != "" {
			valid = verifyStripeSignature(stripeSig, rawBody, secret)
		} else if genericSig != "" {
			valid = verifyGenericSignature(genericSig, rawBody, secret)
		} else {
			// No signature present but secret is configured
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "invalid_webhook_signature",
			})
			return
		}

		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  "invalid_webhook_signature",
			})
			return
		}

		next(w, r)
	}
}

// verifyStripeSignature parses a Stripe-Signature header and validates
// the HMAC-SHA256 signature using constant-time comparison.
func verifyStripeSignature(header string, rawBody []byte, secret string) bool {
	var timestamp int64
	var expectedSigs []string

	// Parse t=<ts>,v1=<hex>[,v1=<hex2>]
	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "t=") {
			ts, err := strconv.ParseInt(strings.TrimPrefix(part, "t="), 10, 64)
			if err != nil {
				return false
			}
			timestamp = ts
		} else if strings.HasPrefix(part, "v1=") {
			expectedSigs = append(expectedSigs, strings.TrimPrefix(part, "v1="))
		}
	}

	if timestamp == 0 || len(expectedSigs) == 0 {
		return false
	}

	// Replay guard: reject if |now - ts| > 300s
	now := time.Now().Unix()
	diff := now - timestamp
	if diff < 0 {
		diff = -diff
	}
	if diff > 300 {
		return false
	}

	// Compute HMAC-SHA256 over "<ts>.<rawBody>"
	signedPayload := strconv.FormatInt(timestamp, 10) + "." + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	computedSig := hex.EncodeToString(mac.Sum(nil))

	// Constant-time compare against every v1 entry
	for _, expected := range expectedSigs {
		if subtle.ConstantTimeCompare([]byte(computedSig), []byte(expected)) == 1 {
			return true
		}
	}

	return false
}

// verifyGenericSignature validates a "X-Spine-Signature" header of the form
// "<ts>.<hex>" using HMAC-SHA256 with constant-time comparison.
func verifyGenericSignature(header string, rawBody []byte, secret string) bool {
	// Format: <timestamp>.<hex_signature>
	dotIdx := strings.Index(header, ".")
	if dotIdx < 0 {
		return false
	}

	tsStr := header[:dotIdx]
	expectedSig := header[dotIdx+1:]

	if tsStr == "" || expectedSig == "" {
		return false
	}

	timestamp, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}

	// Replay guard: reject if |now - ts| > 300s
	now := time.Now().Unix()
	diff := now - timestamp
	if diff < 0 {
		diff = -diff
	}
	if diff > 300 {
		return false
	}

	// Compute HMAC-SHA256 over "<ts>.<rawBody>"
	signedPayload := tsStr + "." + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	computedSig := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(computedSig), []byte(expectedSig)) == 1
}

// extractWebhookProvider extracts the provider name from a webhook URL path.
// Returns "" if the path doesn't match /webhook/<provider>.
func extractWebhookProvider(path string) string {
	if !strings.HasPrefix(path, "/webhook/") {
		return ""
	}
	provider := strings.TrimPrefix(path, "/webhook/")
	if provider == "" || strings.Contains(provider, "/") {
		return ""
	}
	return provider
}
