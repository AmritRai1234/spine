package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Shared HTTP client for webhook steps — enables TCP/TLS connection reuse
var sharedHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// ActionFunc represents a custom Go plugin action handler function.
type ActionFunc func(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error

// RegisterAction registers a custom Go action handler plugin.
func (b *Bus) RegisterAction(name string, fn ActionFunc) {
	b.customActions.Store(name, fn)
}

func (b *Bus) dispatchAction(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	switch step.Action {
	case "db.insert":
		if step.Table != "" {
			return b.dbInsert(step, eventName, payload)
		}
	case "db.update":
		if step.Table != "" {
			return b.dbUpdate(step.Table, step.Where, eventName, payload)
		}
	case "db.sum":
		if step.Table != "" {
			as := step.Config["as"]
			if as == "" {
				as = "sum_result"
			}
			return b.dbSum(step.Table, step.Config["column"], step.Where, as, eventName, payload)
		}
	case "db.upsert":
		if step.Table != "" {
			key := step.Config["key"]
			if key == "" {
				// Accept the more descriptive alias too
				key = step.Config["conflict_key"]
			}
			if key == "" {
				key = "id"
			}
			// Multi-column key (comma-joined by the parser from list syntax,
			// or written literally as "a,b") → composite constraint semantics.
			// A LIST-form key (even a single element) is ALSO constraint
			// semantics: list syntax is the author's explicit claim marker;
			// scalar key: stays identity/merge. key_list is set by the parser.
			isConstraint := strings.Contains(key, ",") || step.Config["key_list"] == "true"
			if isConstraint {
				cols := strings.Split(key, ",")
				for i := range cols {
					cols[i] = strings.TrimSpace(cols[i])
				}
				return b.dbUpsertComposite(step.Table, cols, eventName, payload)
			}
			return b.dbUpsert(step.Table, key, eventName, payload)
		}
	case "db.delete":
		if step.Table != "" {
			return b.dbDelete(step.Table, step.Where, eventName, payload)
		}
	case "db.lookup":
		return b.dbLookup(step, eventName, payload)
	case "db.adjust":
		return b.dbAdjust(step, eventName, payload)
	case "assert":
		return b.assertCondition(step, eventName, payload)
	case "unset":
		return b.unsetFields(step, eventName, payload)
	case "notify.webhook":
		return b.notifyWebhook(step, eventName, payload)
	case "set":
		return b.setFields(step, eventName, payload)
	case "math.calc":
		return b.mathCalc(step, eventName, payload)
	case "http.post":
		return b.httpPost(step, eventName, payload)
	case "log.write":
		return b.logWrite(step, eventName, payload)
	case "fts.search":
		return b.ftsSearch(step, eventName, payload)
	case "emit_to":
		return b.emitToBridge(step, eventName, payload)
	case "queue.publish":
		return b.queuePublish(step, eventName, payload)
	case "email.send":
		return b.emailSend(step, eventName, payload)
	case "email.broadcast":
		return b.emailBroadcast(step, eventName, payload)
	case "subscriptions.sweep":
		return b.subscriptionsSweep(step, eventName, payload)
	case "slots.generate":
		return b.slotsGenerate(step, eventName, payload)
	case "db.fanout":
		return b.dbFanout(step, eventName, payload)
	case "stripe.checkout":
		return b.stripeCheckout(step, eventName, payload)
	case "stripe.connect":
		return b.stripeConnect(step, eventName, payload)
	case "domain.connect":
		return b.domainConnect(step, eventName, payload)
	case "auth.hash":
		return b.authHash(step, eventName, payload)
	case "auth.verify":
		return b.authVerify(step, eventName, payload)
	case "tracking.register":
		return b.trackingRegister(step, eventName, payload)
	default:
		if val, ok := b.customActions.Load(step.Action); ok {
			if fn, isFn := val.(ActionFunc); isFn {
				return fn(step, eventName, payload)
			}
		}
	}
	return nil
}

func (b *Bus) queuePublish(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	topic := step.Table
	if topic == "" {
		topic = eventName
	}
	b.hub.BroadcastState(topic, eventName, payload)
	return nil
}

func (b *Bus) httpPost(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	targetURL := ResolveVariables(step.URL, eventName, payload)
	if targetURL == "" {
		return fmt.Errorf("http.post step missing 'url'")
	}

	var bodyBytes []byte
	if step.Input != "" && step.Input != "$event.payload" {
		resolvedInput := ResolveVariables(step.Input, eventName, payload)
		bodyBytes = []byte(resolvedInput)
	} else {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("http.post failed to marshal payload: %w", err)
		}
	}

	resp, err := sharedHTTPClient.Post(targetURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("http.post request to '%s' failed: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http.post to '%s' returned status %d", targetURL, resp.StatusCode)
	}

	return nil
}

func (b *Bus) logWrite(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	msg := step.Message
	if msg == "" {
		msg = "event: $event.name payload: $event.payload"
	}
	resolvedMsg := ResolveVariables(msg, eventName, payload)
	log.Printf("[SPINE LOG] %s", resolvedMsg)
	return nil
}

// ftsSearch runs a full-text search over a table's FTS5 index (SQLite/Turso).
// The index is provisioned on first use (ensureFTS) and maintained by
// triggers, so results are always current. Matching rows land in the payload
// as `fts_results`: [{rowid, content}] where content is the row's indexed
// text columns joined with spaces. Failures are loud — the route errors —
// instead of silently returning empty results.
func (b *Bus) ftsSearch(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	tableName := step.Table
	query := step.Config["query"]
	if query == "" {
		query = step.Where
	}
	if query == "" {
		query = step.Input
	}
	if tableName == "" || query == "" {
		return fmt.Errorf("fts.search requires 'table' and query")
	}
	resolvedQuery := ResolveVariables(query, eventName, payload)
	if strings.TrimSpace(resolvedQuery) == "" {
		return fmt.Errorf("fts.search query resolved empty")
	}

	// Sanitize table identifiers to prevent SQL injection from manifest input.
	table := sanitizeIdent(tableName)
	if err := b.ensureFTS(table); err != nil {
		return err
	}
	ftsTable := ftsTableName(table)

	rows, err := b.db.Query(fmt.Sprintf(`SELECT %s, * FROM "%s" WHERE "%s" MATCH %s`,
		b.dialect.rowIDCol, ftsTable, ftsTable, b.ph(1)), resolvedQuery)
	if err != nil {
		// A malformed FTS query (e.g. "a AND") is a user error — surface it.
		return fmt.Errorf("fts.search on table %q failed: %w", table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("fts.search: reading result columns failed: %w", err)
	}

	var results []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("fts.search: scanning result failed: %w", err)
		}
		row := make(map[string]interface{}, len(cols))
		var contentParts []string
		for i, c := range cols {
			switch v := vals[i].(type) {
			case nil:
				row[c] = nil
			case []byte:
				row[c] = string(v)
				if c != "rowid" {
					contentParts = append(contentParts, string(v))
				}
			default:
				row[c] = v
				if c != "rowid" {
					contentParts = append(contentParts, fmt.Sprintf("%v", v))
				}
			}
		}
		if len(contentParts) > 0 {
			row["content"] = strings.Join(contentParts, " ")
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("fts.search: iterating results failed: %w", err)
	}

	payload["fts_results"] = results
	return nil
}

func (b *Bus) emitToBridge(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	targetStream := step.Config["stream"]
	if targetStream == "" {
		targetStream = step.Table
	}
	if targetStream == "" {
		targetStream = "external_stream"
	}
	log.Printf("[STREAM BRIDGE] Emitted event '%s' to external stream '%s'", eventName, targetStream)
	b.hub.BroadcastState(targetStream, eventName, payload)
	return nil
}

// setFields merges key-value pairs from step.Config into the event payload.
// Values are resolved through ResolveVariables, so $uuid, $now, $event.payload.X work.
func (b *Bus) setFields(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	for key, val := range step.Config {
		resolved := ResolveVariables(val, eventName, payload)
		payload[key] = resolved
	}
	return nil
}

// unsetFields removes keys from the event payload. Config "fields" holds a
// space-separated list of key names. Used after db.lookup to prune merged
// helper columns (product_*, coupon_*) before a db.insert persists the payload.
func (b *Bus) unsetFields(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	for _, field := range strings.Fields(step.Config["fields"]) {
		delete(payload, field)
	}
	return nil
}

// assertCondition turns a guard expression into a hard failure: when the
// condition does not hold the step errors (triggering on_failure), unlike an
// `if:` which merely skips. This is what makes server-side guards — price
// mismatches, oversold stock — reject a route instead of silently passing.
func (b *Bus) assertCondition(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	cond := step.Config["condition"]
	if cond == "" {
		return fmt.Errorf("assert step requires 'condition' config")
	}
	if EvaluateCondition(cond, eventName, payload) {
		return nil
	}
	// `message` is a parser-known step key (step.Message); Config fallback
	// keeps hand-constructed steps working too.
	msg := ResolveVariables(step.Message, eventName, payload)
	if msg == "" {
		msg = ResolveVariables(step.Config["message"], eventName, payload)
	}
	if msg == "" {
		msg = cond
	}
	return fmt.Errorf("assert failed: %s", msg)
}

// notifyWebhook posts the event payload to a webhook URL, silently no-opping
// when the URL resolves empty (e.g. $env.ALERT_WEBHOOK_URL not configured).
// Failures flow through the durable outbox for retries, like http.post.
func (b *Bus) notifyWebhook(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	targetURL := ResolveVariables(step.URL, eventName, payload)
	if targetURL == "" {
		return nil // Not configured — notifications disabled, never fail the route
	}
	post := *step
	post.URL = targetURL
	if err := b.httpPost(&post, eventName, payload); err != nil {
		return err
	}
	return nil
}
