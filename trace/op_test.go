package trace

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"goodkind.io/gklog"
)

func TestOpEmitsDebugOnFastSuccess(t *testing.T) {
	capture, ctx := captureCtx(t)

	_ = func() (err error) {
		defer Op(ctx, "fast.op")(&err)
		return nil
	}()

	record, ok := capture.find("fast.op")
	if !ok {
		t.Fatal("expected fast.op record")
	}
	if record.level != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", record.level)
	}
	if got := record.attrs["status"].String(); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	if record.attrs["op"].String() != "fast.op" {
		t.Fatalf("op = %q", record.attrs["op"].String())
	}
}

func TestOpEmitsWarnOnFailure(t *testing.T) {
	capture, ctx := captureCtx(t)

	want := errors.New("boom")
	_ = func() (err error) {
		defer Op(ctx, "failing.op")(&err)
		return want
	}()

	record, ok := capture.find("failing.op")
	if !ok {
		t.Fatal("expected failing.op record")
	}
	if record.level != slog.LevelWarn {
		t.Fatalf("level = %v, want warn", record.level)
	}
	if got := record.attrs["status"].String(); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := record.attrs["err"].String(); got != "boom" {
		t.Fatalf("err = %q, want boom", got)
	}
}

func TestOpEmitsWarnOnSlow(t *testing.T) {
	capture, ctx := captureCtx(t)

	original := SlowOpThreshold
	SlowOpThreshold = time.Microsecond
	t.Cleanup(func() { SlowOpThreshold = original })

	_ = func() (err error) {
		defer Op(ctx, "slow.op")(&err)
		time.Sleep(2 * time.Millisecond)
		return nil
	}()

	record, ok := capture.find("slow.op")
	if !ok {
		t.Fatal("expected slow.op record")
	}
	if record.level != slog.LevelWarn {
		t.Fatalf("level = %v, want warn", record.level)
	}
	if got := record.attrs["status"].String(); got != "slow" {
		t.Fatalf("status = %q, want slow", got)
	}
}

func captureCtx(t *testing.T) (*captureHandler, context.Context) {
	t.Helper()
	capture := newCaptureHandler()
	logger := slog.New(capture)
	ctx := gklog.WithLogger(context.Background(), logger)
	return capture, ctx
}
