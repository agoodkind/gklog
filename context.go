package gklog

import (
	"context"
	"log/slog"
)

type contextLoggerKey struct{}

// WithLogger returns a child of ctx that carries log for use by
// [LoggerFromContext]. Attach stable fields with log.With before
// calling this, typically at a request or RPC boundary, then pass the
// returned context through the stack. Downstream code should log with
// LoggerFromContext(ctx).InfoContext(ctx, msg, ...) so the record
// carries the same context for handlers.
//
// If log is nil, ctx is returned unchanged. If ctx is nil, it is
// treated as [context.Background].
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextLoggerKey{}, log)
}

// LoggerFromContext returns the [*slog.Logger] previously stored with
// [WithLogger], or [slog.Default] when ctx is nil, no value was stored,
// or the stored value is not a non-nil [*slog.Logger].
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	v, ok := ctx.Value(contextLoggerKey{}).(*slog.Logger)
	if !ok || v == nil {
		return slog.Default()
	}
	return v
}

// L is a short alias for [LoggerFromContext].
func L(ctx context.Context) *slog.Logger {
	return LoggerFromContext(ctx)
}
