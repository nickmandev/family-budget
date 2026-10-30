package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"
)

// HeaderRequestID is the correlation header. A valid inbound value is
// propagated so a reverse proxy or client can correlate logs; invalid values
// are replaced rather than trusted verbatim.
const HeaderRequestID = "X-Request-ID"

// Middleware is the explicit, composable middleware shape used by Server.
type Middleware func(http.Handler) http.Handler

// Chain composes middleware so the first argument is the outermost wrapper.
//
// Server order (see NewServer):
//
//	RequestID -> Recovery -> CORS -> Logging -> MaxBodyBytes -> Timeout -> routes
//
// RequestID is outermost so every log line and error envelope carries the same
// id. Recovery wraps CORS and the remaining middleware so panics become a clean
// 500. CORS runs before logging, body limits, timeout and routing so preflights
// are answered cheaply without entering those layers. MaxBodyBytes sits before
// Timeout so an oversized body is rejected without consuming the request
// timeout budget. Per-route authentication adds one more wrapper inside this
// chain for protected routes only.
func Chain(handler http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

type contextKey int

const requestIDContextKey contextKey = iota

// RequestIDFromContext returns the correlation id attached by RequestID, or ""
// when the middleware is absent.
func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

// WithRequestID attaches an explicit correlation id. It exists for tests and
// for callers that invoke handlers without the full middleware chain.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// RequestID propagates a well-formed inbound X-Request-ID or generates a new
// cryptographically random one, stores it in the request context and echoes it
// on the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if !requestIDPattern.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
		})
	}
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure is unrecoverable for session tokens, but a
		// correlation id can safely fall back to a timestamp-based value.
		return fmt.Sprintf("req-%d", time.Now().UTC().UnixNano())
	}
	return encodeHex(raw[:])
}

// statusRecorder captures the status and byte count for structured logging
// without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(body)
	r.bytes += n
	return n, err
}

// Unwrap supports http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) HeaderWritten() bool { return r.wroteHeader }

// Logging emits one structured line per request with request metadata only.
//
// Redaction policy (SECURITY_AND_OPERATIONS "Operational telemetry"): request
// bodies, query strings and headers are never logged, so passwords, session
// tokens, reset tokens and financial payloads cannot appear here. The path is
// logged without its query because auth/recovery links could carry a token in
// the query string.
func Logging(logger *slog.Logger, trustedProxies ...clientIPResolver) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	resolver := defaultClientIPResolver
	if len(trustedProxies) > 0 {
		resolver = trustedProxies[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			if !recorder.wroteHeader {
				recorder.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http_request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"bytes", recorder.bytes,
				"duration_ms", float64(time.Since(start).Microseconds())/1000,
				"remote_addr", r.RemoteAddr,
				"client_ip", resolver.resolve(r),
			)
		})
	}
}

// Recovery converts a panic into a generic 500 envelope. The stack trace and
// panic value stay server-side in the structured log; they never reach the
// client.
func Recovery(logger *slog.Logger) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				// Let net/http handle its own abort signal.
				if errors.Is(recoveredAsError(recovered), http.ErrAbortHandler) {
					panic(recovered)
				}
				logger.ErrorContext(r.Context(), "panic_recovered",
					"request_id", RequestIDFromContext(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				if written, ok := w.(interface{ HeaderWritten() bool }); !ok || !written.HeaderWritten() {
					writeError(w, r, errInternal)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func recoveredAsError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return nil
}

// MaxBodyBytes enforces the request-size limit. A declared Content-Length over
// the limit is rejected before the handler runs; a chunked/streamed body is
// capped by http.MaxBytesReader and surfaces as PAYLOAD_TOO_LARGE when the
// handler decodes it (see decodeJSON).
func MaxBodyBytes(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit > 0 {
				if r.ContentLength > limit {
					writeError(w, r, errPayloadTooLarge)
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds handler execution. It buffers the handler's response so a
// late write cannot race with the timeout response. Panics are re-raised in
// the outer goroutine so Recovery can see them.
func Timeout(duration time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if duration <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), duration)
			defer cancel()

			buffered := newBufferedResponseWriter()
			done := make(chan any, 1)
			go func() {
				var recovered any
				defer func() {
					recovered = recover()
					done <- recovered
				}()
				next.ServeHTTP(buffered, r.WithContext(ctx))
			}()

			select {
			case recovered := <-done:
				if recovered != nil {
					panic(recovered)
				}
				buffered.flushTo(w)
			case <-ctx.Done():
				writeError(w, r, errRequestTimeout)
			}
		})
	}
}

// bufferedResponseWriter is the Timeout middleware's private response target.
// It is only ever written by the handler goroutine.
type bufferedResponseWriter struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header), status: http.StatusOK}
}

func (b *bufferedResponseWriter) Header() http.Header { return b.header }

func (b *bufferedResponseWriter) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.status = status
	b.wroteHeader = true
}

func (b *bufferedResponseWriter) Write(body []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(body)
}

func (b *bufferedResponseWriter) flushTo(w http.ResponseWriter) {
	for key, values := range b.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if !b.wroteHeader {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
