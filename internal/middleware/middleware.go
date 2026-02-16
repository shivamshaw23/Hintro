// Package middleware contains HTTP middleware for the ride pooling system.
//
// RequestLogger provides structured logging for all API requests,
// including method, path, status code, and latency.
package middleware

import (
	"log"
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// RequestLogger logs every HTTP request with method, path, status, and latency.
//
// Example output:
//
//	[http] POST /api/v1/book/2 → 200 (4.2ms)
//	[http] POST /api/v1/book/3 → 422 (2.1ms)
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		latency := time.Since(start)
		log.Printf("[http] %s %s → %d (%s)",
			r.Method, r.URL.Path, rw.statusCode, latency.Round(100*time.Microsecond))
	})
}

// Recoverer catches panics in handlers and returns a 500 response
// instead of crashing the entire server.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[http] PANIC: %s %s → %v", r.Method, r.URL.Path, err)
				http.Error(w, `{"error":"internal_server_error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS adds headers so browser-based clients (e.g. Swagger UI) can call the API.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
