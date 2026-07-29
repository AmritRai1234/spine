package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"time"
)

type contextKey string

const RequestIDKey contextKey = "requestID"

// generateRequestID returns a 16-byte random hex string for request tracing.
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// responseWriterInterceptor captures response HTTP status code and bytes written.
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
	bytesWritten int64
}

func (rw *responseWriterInterceptor) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriterInterceptor) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// LoggingMiddleware logs request path, method, status code, latency, and injects X-Request-ID.
func LoggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}

		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), RequestIDKey, reqID)
		r = r.WithContext(ctx)

		interceptor := &responseWriterInterceptor{ResponseWriter: w, statusCode: http.StatusOK}

		next(interceptor, r)

		duration := time.Since(start)
		log.Printf("[HTTP] req_id=%s method=%s path=%s status=%d duration=%s bytes=%d",
			reqID, r.Method, r.URL.Path, interceptor.statusCode, duration, interceptor.bytesWritten)
	}
}

// GetRequestID retrieves the request ID from context.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}
