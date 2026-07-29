package middleware

import (
	"context"
	"net/http"
	"sync"
)

type customContextKeyType struct{}

var customContextKey = customContextKeyType{}

// CustomExtractorFunc defines a user function that inspects an HTTP request
// and returns dynamic custom key-value pairs (e.g. location, temperature, tenant_id, device_info).
type CustomExtractorFunc func(r *http.Request) map[string]interface{}

// CustomContextManager handles registration and execution of user-defined
// context extractors across HTTP requests.
type CustomContextManager struct {
	mu         sync.RWMutex
	extractors []CustomExtractorFunc
	staticData map[string]interface{}
}

// NewCustomContextManager creates a new CustomContextManager instance.
func NewCustomContextManager() *CustomContextManager {
	return &CustomContextManager{
		extractors: make([]CustomExtractorFunc, 0),
		staticData: make(map[string]interface{}),
	}
}

// AddExtractor registers a custom dynamic extraction function (e.g., for location, temperature, device info).
func (m *CustomContextManager) AddExtractor(fn CustomExtractorFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn != nil {
		m.extractors = append(m.extractors, fn)
	}
}

// SetStaticAttribute sets a static key-value attribute (e.g. region="us-east-1", environment="prod").
func (m *CustomContextManager) SetStaticAttribute(key string, val interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.staticData[key] = val
}

// Extract gathers all custom context data from registered extractors and static attributes.
func (m *CustomContextManager) Extract(r *http.Request) map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]interface{})

	// 1. Copy static attributes
	for k, v := range m.staticData {
		result[k] = v
	}

	// 2. Execute dynamic extractors (e.g. geolocation, headers, temperature sensors)
	if r != nil {
		for _, fn := range m.extractors {
			data := fn(r)
			for k, v := range data {
				result[k] = v
			}
		}
	}

	return result
}

// Middleware returns an HTTP middleware that extracts custom attributes and injects them into r.Context().
func (m *CustomContextManager) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extracted := m.Extract(r)
		ctx := context.WithValue(r.Context(), customContextKey, extracted)
		next(w, r.WithContext(ctx))
	}
}

// GetCustomContext retrieves the custom attributes map from an HTTP request context.
func GetCustomContext(ctx context.Context) map[string]interface{} {
	if data, ok := ctx.Value(customContextKey).(map[string]interface{}); ok {
		return data
	}
	return make(map[string]interface{})
}

// MergeCustomContextIntoPayload merges custom context attributes (like location, temperature) into
// an event payload without overwriting explicit client-provided payload keys.
func MergeCustomContextIntoPayload(ctx context.Context, payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		payload = make(map[string]interface{})
	}

	customData := GetCustomContext(ctx)
	if len(customData) == 0 {
		return payload
	}

	// Inject under '_context' dictionary
	payload["_context"] = customData

	// Also merge missing top-level keys for easy $event.payload.<key> resolution
	for k, v := range customData {
		if _, exists := payload[k]; !exists {
			payload[k] = v
		}
	}

	return payload
}
