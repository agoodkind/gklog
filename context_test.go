package gklog

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestWithLoggerLoggerFromContextRoundTrip(t *testing.T) {
	t.Parallel()
	custom := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	ctx = WithLogger(ctx, custom)
	if got := LoggerFromContext(ctx); got != custom {
		t.Fatalf("LoggerFromContext: want same pointer as stored, got %p want %p", got, custom)
	}
	if got := L(ctx); got != custom {
		t.Fatalf("L: want same pointer as stored")
	}
}

func TestLoggerFromContextMissingUsesDefault(t *testing.T) {
	t.Parallel()
	def := slog.Default()
	ctx := context.Background()
	if got := LoggerFromContext(ctx); got != def {
		t.Fatalf("missing value: got %p want default %p", got, def)
	}
}

func TestWithLoggerNilLoggerReturnsCtxUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	out := WithLogger(ctx, nil)
	if out != ctx {
		t.Fatal("nil log: expected same context")
	}
	if LoggerFromContext(out) != slog.Default() {
		t.Fatal("expected LoggerFromContext to fall back to default")
	}
}

func TestWithLoggerNilCtxUsesBackground(t *testing.T) {
	t.Parallel()
	custom := slog.New(slog.NewTextHandler(io.Discard, nil))
	//nolint:staticcheck // SA1012: deliberately exercising the documented nil-ctx fallback path.
	ctx := WithLogger(nil, custom)
	if got := LoggerFromContext(ctx); got != custom {
		t.Fatalf("nil input ctx: expected stored logger")
	}
}

func TestLoggerFromContextNilCtx(t *testing.T) {
	t.Parallel()
	def := slog.Default()
	//nolint:staticcheck // SA1012: deliberately exercising the documented nil-ctx fallback path.
	if got := LoggerFromContext(nil); got != def {
		t.Fatalf("nil ctx: got %p want default %p", got, def)
	}
}

func TestLoggerFromContextWrongTypeFallsBack(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), contextLoggerKey{}, "not-a-logger")
	if got := LoggerFromContext(ctx); got != slog.Default() {
		t.Fatalf("wrong type: expected slog.Default")
	}
}
