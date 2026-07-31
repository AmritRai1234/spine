package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

// setFields merges key-value pairs from step.Config into the event payload.
// Values are resolved through ResolveVariables, so $uuid, $now, $event.payload.X work.
func (b *Bus) setFields(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	for key, val := range step.Config {
		resolved := ResolveVariables(val, eventName, payload)
		payload[key] = resolved
	}
	return nil
}
