package correlation

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"goodkind.io/gklog/internal/clock"
)

type capturedRecord struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Missing   string `json:"missing_field,omitempty"`
	SourceMsg string `json:"source_msg,omitempty"`
}

func captureRecords(t *testing.T, opts HandlerOptions, write func(log *slog.Logger)) []capturedRecord {
	t.Helper()
	var buf bytes.Buffer
	json := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(SlogHandler(json, opts))
	write(logger)
	return parseRecords(t, &buf)
}

func parseRecords(t *testing.T, buf *bytes.Buffer) []capturedRecord {
	t.Helper()
	var out []capturedRecord
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec capturedRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestSlogHandlerInjectsContextAttrs(t *testing.T) {
	corr := New("req-1")
	ctx := WithContext(context.Background(), corr)

	records := captureRecords(t, HandlerOptions{}, func(log *slog.Logger) {
		log.InfoContext(ctx, "hello")
	})

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].TraceID != string(corr.TraceID) {
		t.Fatalf("trace_id missing/wrong: %q vs %q", records[0].TraceID, corr.TraceID)
	}
	if records[0].SpanID != string(corr.SpanID) {
		t.Fatalf("span_id missing/wrong: %q vs %q", records[0].SpanID, corr.SpanID)
	}
}

func TestSlogHandlerSkipsWhenContextEmpty(t *testing.T) {
	records := captureRecords(t, HandlerOptions{}, func(log *slog.Logger) {
		log.Info("no-context")
	})
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].TraceID != "" {
		t.Fatalf("expected no trace_id, got %q", records[0].TraceID)
	}
}

func TestSlogHandlerPreservesExistingAttrs(t *testing.T) {
	corr := New("req-1")
	ctx := WithContext(context.Background(), corr)

	records := captureRecords(t, HandlerOptions{}, func(log *slog.Logger) {
		log.InfoContext(ctx, "explicit", "trace_id", "user-trace")
	})

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].TraceID != "user-trace" {
		t.Fatalf("user-supplied trace_id should win, got %q", records[0].TraceID)
	}
}

func TestSlogHandlerInjectsIdentityAttributes(t *testing.T) {
	corr := New("req-1").WithIdentityAttributes(
		IdentityAttribute{Key: "job_id", Value: "job-1"},
	)
	ctx := WithContext(context.Background(), corr)

	records := captureRecords(t, HandlerOptions{}, func(log *slog.Logger) {
		log.InfoContext(ctx, "with-job")
	})
	if records[0].JobID != "job-1" {
		t.Fatalf("expected job_id=job-1, got %q", records[0].JobID)
	}
}

func TestSlogHandlerStrictEmitsMissingField(t *testing.T) {
	corr := New("req-1")
	ctx := WithContext(context.Background(), corr)

	records := captureRecords(t, HandlerOptions{
		Strict:   true,
		Required: []string{"trace_id", "span_id", "job_id"},
	}, func(log *slog.Logger) {
		log.InfoContext(ctx, "no-job")
	})

	if len(records) != 2 {
		t.Fatalf("expected 2 records (diag + original), got %d: %#v", len(records), records)
	}
	diag := records[0]
	if diag.Msg != "correlation.missing_field" {
		t.Fatalf("diag message wrong: %q", diag.Msg)
	}
	if diag.Missing != "job_id" {
		t.Fatalf("diag missing_field wrong: %q", diag.Missing)
	}
	if diag.SourceMsg != "no-job" {
		t.Fatalf("diag source_msg wrong: %q", diag.SourceMsg)
	}
	if diag.TraceID != string(corr.TraceID) {
		t.Fatalf("diag should carry trace_id, got %q", diag.TraceID)
	}
}

func TestSlogHandlerStrictSilentOnEmptyContext(t *testing.T) {
	records := captureRecords(t, HandlerOptions{
		Strict:   true,
		Required: []string{"trace_id"},
	}, func(log *slog.Logger) {
		log.Info("no-context")
	})
	if len(records) != 1 {
		t.Fatalf("expected 1 record when no context active, got %d: %#v", len(records), records)
	}
}

func TestSlogHandlerStrictSilentWhenRequiredPresent(t *testing.T) {
	corr := New("req-1").WithIdentityAttributes(
		IdentityAttribute{Key: "job_id", Value: "job-1"},
	)
	ctx := WithContext(context.Background(), corr)

	records := captureRecords(t, HandlerOptions{
		Strict:   true,
		Required: []string{"trace_id", "job_id"},
	}, func(log *slog.Logger) {
		log.InfoContext(ctx, "ok")
	})
	if len(records) != 1 {
		t.Fatalf("expected 1 record when all required present, got %d", len(records))
	}
}

func TestSlogHandlerNilNextFallsBackToDiscard(t *testing.T) {
	handler := SlogHandler(nil, HandlerOptions{})
	logger := slog.New(handler)
	logger.Info("ignored")
}

func TestSlogHandlerStrictUsesInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	handler := SlogHandler(jsonHandler, HandlerOptions{
		Strict:   true,
		Required: []string{"job_id"},
	}).(*slogHandler)
	handler.clock = clock.Func(func() time.Time { return fixed })

	logger := slog.New(handler)
	ctx := WithContext(context.Background(), New("req"))
	logger.InfoContext(ctx, "no-job")

	if !strings.Contains(buf.String(), `"time":"2026-05-25T12:00:00Z"`) {
		t.Fatalf("expected injected clock to set diag time, got:\n%s", buf.String())
	}
}
