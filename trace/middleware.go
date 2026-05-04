package trace

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/gklog"
)

// RequestLogger is HTTP middleware that:
//   - Generates a request id (or reads X-Request-ID from the inbound headers).
//   - Extracts an upstream W3C traceparent and starts a server span.
//   - Stores a logger decorated with request_id, trace_id, span_id, method,
//     path, and remote_addr in the request context.
//   - Logs one "request" summary record per call with status, bytes, latency.
//   - Echoes X-Request-ID on the response.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := r.Header.Get(RequestIDHeader)
		if reqID == "" {
			reqID = uuid.New().String()
		}

		ctx := WithRequestMetadata(r.Context(), reqID)
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer().Start(
			ctx,
			fmt.Sprintf("%s %s", r.Method, r.URL.Path),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
				attribute.String("client.address", r.RemoteAddr),
				attribute.String("http.request.header.x_request_id", reqID),
			),
		)
		defer span.End()

		l := LoggerWithContext(ctx, slog.Default(),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
		)
		ctx = gklog.WithLogger(ctx, l)
		r = r.WithContext(ctx)

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set(RequestIDHeader, reqID)

		next.ServeHTTP(rw, r)

		latency := time.Since(start)
		span.SetAttributes(
			attribute.Int("http.response.status_code", rw.status),
			attribute.Int64("http.response.body.size", rw.bytes),
			attribute.Int64("http.server.duration_ms", latency.Milliseconds()),
		)
		if rw.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(rw.status))
		}

		l.InfoContext(ctx, "request",
			slog.Int("status", rw.status),
			slog.Int64("bytes", rw.bytes),
			slog.Duration("latency", latency),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}
