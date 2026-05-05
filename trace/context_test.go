package trace

import (
	"context"
	"log/slog"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestMetadata(context.Background(), "abc-123")
	if got := RequestID(ctx); got != "abc-123" {
		t.Fatalf("RequestID = %q, want abc-123", got)
	}
}

func TestRequestIDEmptyByDefault(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("RequestID = %q, want empty", got)
	}
}

func TestTraceIDAndSpanIDEmptyWithoutSpan(t *testing.T) {
	ctx := context.Background()
	if got := IDFromContext(ctx); got != "" {
		t.Fatalf("IDFromContext = %q, want empty", got)
	}
	if got := SpanIDFromContext(ctx); got != "" {
		t.Fatalf("SpanIDFromContext = %q, want empty", got)
	}
}

func TestLoggerWithContextOmitsMissingFields(t *testing.T) {
	capture := newCaptureHandler()
	base := slog.New(capture)
	logger := LoggerWithContext(context.Background(), base, slog.String("extra", "yes"))
	logger.Info("evt")

	record, ok := capture.find("evt")
	if !ok {
		t.Fatal("expected evt record")
	}
	if _, present := record.attrs["request_id"]; present {
		t.Fatal("request_id should be absent when ctx has none")
	}
	if _, present := record.attrs["trace_id"]; present {
		t.Fatal("trace_id should be absent when ctx has no span")
	}
	if got := record.attrs["extra"].String(); got != "yes" {
		t.Fatalf("extra = %q, want yes", got)
	}
}

func TestLoggerWithContextIncludesRequestID(t *testing.T) {
	capture := newCaptureHandler()
	base := slog.New(capture)
	ctx := WithRequestMetadata(context.Background(), "req-9")
	logger := LoggerWithContext(ctx, base)
	logger.Info("evt")

	record, ok := capture.find("evt")
	if !ok {
		t.Fatal("expected evt record")
	}
	if got := record.attrs["request_id"].String(); got != "req-9" {
		t.Fatalf("request_id = %q, want req-9", got)
	}
}
