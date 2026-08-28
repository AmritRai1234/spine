package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// tracking.register — GENERIC package-tracking registration for any provider
// with an HTTP register-style API (17TRACK, Shippo, EasyPost, AfterShip, or a
// self-hosted tracker). Spine has no provider-specific logic: the manifest
// supplies the endpoint, headers, and body template, so plugging in a new
// tracking provider is pure manifest work.
//
// Step config (all value expressions support $event.payload.* / $env.*):
//
//	url      (or step URL)  Provider register endpoint.
//	                        e.g. https://api.17track.net/track/v2.4/register
//	numbers                 Space-separated list of tracking-number value
//	                        expressions (e.g. "$event.payload.tracking_number").
//	                        Exactly the numbers registered in this call.
//	headers                 Space-separated "Name: value" pairs; both sides are
//	                        resolved expressions. Auth headers for any provider:
//	                        17TRACK → "17token: $env.TRACK17_TOKEN"
//	                        EasyPost → "Authorization: Bearer $env.EASYPOST_KEY"
//	                        AfterShip → "as-api-key: $env.AFTERSHIP_KEY"
//	body                    JSON body TEMPLATE. {{numbers}} is replaced by a
//	                        JSON array of the resolved numbers; everything else
//	                        is resolved verbatim. Providers with different
//	                        shapes write their own template, e.g.
//	                        17TRACK: '[{"number":"{{numbers}}"}]' (→ [{number:"X"}])
//	                        Shippo:   '{"tracking_number":"{{numbers}}","carrier":"{{...}}"}'
//	                        When body is omitted a 17TRACK-compatible array
//	                        [{"number":"X"}] is sent.
//	optional                "true" → missing token/numbers is a silent no-op
//	                        (dev-friendly, like email.send without SMTP_HOST).
//
// The provider's JSON response lands in the payload under the `as` key
// (default "tracking_response") so later steps can branch on it.
//
// Timing-safety note: this action is a NETWORK step like http.post — 5s
// timeout, failures surface synchronously so on_failure can fire.

func (b *Bus) trackingRegister(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	targetURL := ResolveVariables(step.Config["url"], eventName, payload)
	if targetURL == "" {
		targetURL = ResolveVariables(step.URL, eventName, payload)
	}
	optional := step.Config["optional"] == "true"

	if targetURL == "" {
		if optional {
			log.Printf("[tracking] tracking.register skipped — no url configured")
			return nil
		}
		return fmt.Errorf("tracking.register requires 'url' config")
	}

	// Resolve the tracking numbers (space-separated value expressions).
	var numbers []string
	for _, expr := range strings.Fields(step.Config["numbers"]) {
		v := ResolveValue(expr, eventName, payload)
		if s, ok := v.(string); ok && s != "" {
			numbers = append(numbers, s)
		}
	}
	if len(numbers) == 0 {
		if optional {
			log.Printf("[tracking] tracking.register skipped — no tracking numbers on %s", eventName)
			return nil
		}
		return fmt.Errorf("tracking.register requires at least one resolvable 'numbers' entry")
	}

	// Build headers from "Name: value" pairs (pairs separated by whitespace,
	// name/value by the first colon — so a value may contain spaces). The
	// whole config value resolves FIRST so it can come entirely from an env
	// var: headers: "$env.TRACKING_API_HEADERS".
	headersCfg := ResolveVariables(step.Config["headers"], eventName, payload)
	headers := map[string]string{"Content-Type": "application/json"}
	for _, pair := range splitHeaderPairs(headersCfg) {
		idx := strings.IndexByte(pair, ':')
		if idx <= 0 {
			return fmt.Errorf("tracking.register: malformed header %q (want 'Name: value')", pair)
		}
		name := strings.TrimSpace(pair[:idx])
		value := ResolveVariables(strings.TrimSpace(pair[idx+1:]), eventName, payload)
		if name == "" || value == "" {
			continue
		}
		headers[name] = value
	}

	// Build the body: template with {{numbers}} → JSON array, or the
	// 17TRACK-compatible default.
	arrayJSON, err := json.Marshal(numbers)
	if err != nil {
		return fmt.Errorf("tracking.register: encode numbers: %w", err)
	}
	bodyTemplate := step.Config["body"]
	var body string
	if bodyTemplate == "" {
		// Default: 17TRACK-compatible array of objects, one per number.
		objs := make([]map[string]string, len(numbers))
		for i, n := range numbers {
			objs[i] = map[string]string{"number": n}
		}
		b, err := json.Marshal(objs)
		if err != nil {
			return fmt.Errorf("tracking.register: encode body: %w", err)
		}
		body = string(b)
	} else {
		// `"{{numbers}}"` (inside quotes) collapses to a bare JSON array;
		// bare `{{numbers}}` expands to comma-separated quoted items.
		body = strings.ReplaceAll(bodyTemplate, `"{{numbers}}"`, string(arrayJSON))
		body = ResolveVariables(strings.ReplaceAll(body, "{{numbers}}", strings.Trim(string(arrayJSON), "[]")), eventName, payload)
	}

	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return fmt.Errorf("tracking.register: bad request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("tracking.register to '%s' failed: %w", targetURL, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("tracking.register to '%s' returned status %d: %s", targetURL, resp.StatusCode, truncateForLog(respBody))
	}

	as := step.Config["as"]
	if as == "" {
		as = "tracking_response"
	}
	var parsed interface{}
	if json.Unmarshal(respBody, &parsed) == nil {
		payload[as] = parsed
	} else {
		payload[as] = string(respBody)
	}
	return nil
}

// splitHeaderPairs splits "Name: value  Name2: value2" on whitespace that
// precedes a header-name-looking token (word chars followed by a colon), so a
// value may itself contain spaces ("Authorization: Bearer abc def").
func splitHeaderPairs(s string) []string {
	var pairs []string
	start := 0
	for i := 1; i < len(s); i++ {
		// A boundary: whitespace followed by name chars and a colon, where
		// the previous char is not the colon of this same pair.
		if s[i-1] == ' ' || s[i-1] == '\t' {
			j := i
			for j < len(s) && (isHeaderNameByte(s[j])) {
				j++
			}
			if j > i && j < len(s) && s[j] == ':' {
				if trim := strings.TrimSpace(s[start:i]); trim != "" {
					pairs = append(pairs, trim)
				}
				start = i
			}
		}
	}
	if trim := strings.TrimSpace(s[start:]); trim != "" {
		pairs = append(pairs, trim)
	}
	return pairs
}

func isHeaderNameByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func truncateForLog(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
