package trace

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"goodkind.io/gklog"
)

const (
	testRequestID   = "req-test-123"
	testTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID      = "00f067aa0ba902b7"
	testTraceparent = "00-" + testTraceID + "-" + testSpanID + "-01"
)

func TestRequestLoggerPreservesInboundRequestIDAndTraceContext(t *testing.T) {
	closer, err := Setup(Options{ServiceName: "gklog-trace-test"})
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("close tracing: %v", err)
		}
	})

	capture := newCaptureHandler()
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestID(r.Context()); got != testRequestID {
			t.Fatalf("request id = %q, want %q", got, testRequestID)
		}
		if got := TraceID(r.Context()); got != testTraceID {
			t.Fatalf("trace id = %q, want %q", got, testTraceID)
		}
		if got := SpanID(r.Context()); got == "" {
			t.Fatal("span id is empty")
		}

		gklog.L(r.Context()).InfoContext(r.Context(), "handler.log")
		w.WriteHeader(http.StatusAccepted)
	}))

	request := httptest.NewRequest(http.MethodPost, "/telemetry", nil)
	request.Header.Set(RequestIDHeader, testRequestID)
	request.Header.Set("traceparent", testTraceparent)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get(RequestIDHeader); got != testRequestID {
		t.Fatalf("response request id = %q, want %q", got, testRequestID)
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusAccepted)
	}

	record, ok := capture.find("handler.log")
	if !ok {
		t.Fatal("handler log was not captured")
	}
	if got := record.attrs["request_id"].String(); got != testRequestID {
		t.Fatalf("logged request_id = %q, want %q", got, testRequestID)
	}
	if got := record.attrs["trace_id"].String(); got != testTraceID {
		t.Fatalf("logged trace_id = %q, want %q", got, testTraceID)
	}
	if got := record.attrs["span_id"].String(); got == "" {
		t.Fatal("logged span_id is empty")
	}
}

func TestRequestLoggerGeneratesRequestIDWhenAbsent(t *testing.T) {
	closer, err := Setup(Options{ServiceName: "gklog-trace-test"})
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	t.Cleanup(func() {
		_ = closer.Close()
	})

	var captured string
	handler := RequestLogger(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = RequestID(r.Context())
	}))

	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if captured == "" {
		t.Fatal("expected generated request id, got empty")
	}
	if got := response.Header().Get(RequestIDHeader); got != captured {
		t.Fatalf("response request id = %q, want %q", got, captured)
	}
}
