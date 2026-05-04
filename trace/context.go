// Package trace adds request-scoped correlation, OTel tracing, HTTP
// middleware, and a pgx query tracer on top of the gklog logging stack.
//
// Importing this package brings in OpenTelemetry, net/http, and pgx/v5;
// callers that only want gklog's slog handlers should stay on the root
// gklog package.
package trace

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
	"goodkind.io/gklog"
)

// RequestIDHeader is the caller-visible correlation header preserved on
// inbound requests and echoed on responses by RequestLogger.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// WithRequestMetadata stores the request-scoped correlation id in ctx.
func WithRequestMetadata(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID returns the caller-visible request correlation identifier.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

// TraceID returns the active trace identifier from the current span context,
// or "" when no valid span is attached to ctx.
func TraceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// SpanID returns the active span identifier from the current span context,
// or "" when no valid span is attached to ctx.
func SpanID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

// WithTraceLogger replaces the logger stored in ctx (via gklog.WithLogger)
// with one decorated with request_id, trace_id, span_id, and any extra
// caller-supplied attrs.
func WithTraceLogger(ctx context.Context, attrs ...slog.Attr) context.Context {
	return gklog.WithLogger(ctx, LoggerWithContext(ctx, gklog.L(ctx), attrs...))
}

// LoggerWithContext returns base augmented with the request_id, trace_id,
// and span_id present on ctx, plus any extra attrs. Missing values are
// omitted rather than logged as empty strings.
func LoggerWithContext(ctx context.Context, base *slog.Logger, attrs ...slog.Attr) *slog.Logger {
	loggerAttrs := make([]slog.Attr, 0, len(attrs)+3)
	if requestID := RequestID(ctx); requestID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("request_id", requestID))
	}
	if traceID := TraceID(ctx); traceID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("trace_id", traceID))
	}
	if spanID := SpanID(ctx); spanID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("span_id", spanID))
	}
	loggerAttrs = append(loggerAttrs, attrs...)
	return base.With(attrsToArgs(loggerAttrs)...)
}

func attrsToArgs(attrs []slog.Attr) []any {
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	return args
}
