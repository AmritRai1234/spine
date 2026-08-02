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
			return b.dbInsert(step.Table, eventName, payload)
		}
	case "db.update":
		if step.Table != "" {
			return b.dbUpdate(step.Table, eventName, payload)
		}
	case "db.upsert":
		if step.Table != "" {
			key := step.Config["key"]
			if key == "" {
				key = "id"
			}
			return b.dbUpsert(step.Table, key, eventName, payload)
		}
	case "db.delete":
		if step.Table != "" {
			return b.dbDelete(step.Table, step.Where, eventName, payload)
		}
	case "set":
		return b.setFields(step, eventName, payload)
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

	// Sanitize table identifiers to prevent SQL injection from manifest input
	safeTable := sanitizeIdent(tableName)
	ftsTable := safeTable
	if !strings.HasSuffix(safeTable, "_fts") {
		ftsTable = safeTable + "_fts"
	}

	// Attempt FTS5 virtual table query with fallback to standard table search
	rows, err := b.db.Query(fmt.Sprintf(`SELECT rowid, content FROM "%s" WHERE content MATCH ?`, ftsTable), resolvedQuery)
	if err != nil {
		// Fallback: standard table column query
		rows, err = b.db.Query(fmt.Sprintf(`SELECT rowid, created_at FROM "%s" WHERE created_at LIKE ?`, safeTable), "%"+resolvedQuery+"%")
		if err != nil {
			return nil // Soft fallback: return empty results if table not ready
		}
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var rowid int64
		var content string
		if err := rows.Scan(&rowid, &content); err == nil {
			results = append(results, map[string]interface{}{"rowid": rowid, "content": content})
		}
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
