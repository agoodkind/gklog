package correlation

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestNewGeneratesValidTraceAndSpan(t *testing.T) {
	corr := New("req-1")
	if !corr.Valid() {
		t.Fatalf("expected valid correlation, got %#v", corr)
	}
	if corr.RequestID != "req-1" {
		t.Fatalf("request id = %q, want req-1", corr.RequestID)
	}
	if corr.ParentSpanID != "" {
		t.Fatalf("parent span id should be empty for fresh trace, got %q", corr.ParentSpanID)
	}
}

func TestChildPreservesTraceMovesSpan(t *testing.T) {
	parent := New("req-1")
	child := parent.Child()

	if child.TraceID != parent.TraceID {
		t.Fatalf("child trace id %q != parent %q", child.TraceID, parent.TraceID)
	}
	if child.SpanID == parent.SpanID {
		t.Fatalf("child span id should be fresh, got same as parent: %q", child.SpanID)
	}
	if child.ParentSpanID != parent.SpanID {
		t.Fatalf("child parent_span_id %q != parent span_id %q", child.ParentSpanID, parent.SpanID)
	}
	if child.RequestID != parent.RequestID {
		t.Fatalf("child request id %q != parent %q", child.RequestID, parent.RequestID)
	}
}

func TestChildOnEmptyContextInitializesTrace(t *testing.T) {
	var empty Context
	child := empty.Child()
	if !child.Valid() {
		t.Fatalf("expected child of empty to be valid, got %#v", child)
	}
}

func TestChildCopiesIdentityAttributes(t *testing.T) {
	parent := New("req-1").WithIdentityAttributes(IdentityAttribute{Key: "job_id", Value: "job-1"})
	child := parent.Child()
	if child.IdentityAttributeValue("job_id") != "job-1" {
		t.Fatalf("child missing inherited identity attribute: %#v", child.IdentityAttributes)
	}
	child = child.WithIdentityAttributes(IdentityAttribute{Key: "stage", Value: "embed"})
	if parent.IdentityAttributeValue("stage") != "" {
		t.Fatalf("child mutation leaked back into parent: %#v", parent.IdentityAttributes)
	}
}

func TestEnsureReturnsExistingContext(t *testing.T) {
	parent := WithContext(context.Background(), New("req-1"))
	ctx, corr := Ensure(parent, "req-2")
	if corr.RequestID != "req-1" {
		t.Fatalf("Ensure should preserve existing request id, got %q", corr.RequestID)
	}
	if FromContext(ctx).TraceID != corr.TraceID {
		t.Fatalf("Ensure returned mismatched trace id")
	}
}

func TestEnsureBuildsContextWhenAbsent(t *testing.T) {
	ctx, corr := Ensure(context.Background(), "req-3")
	if corr.RequestID != "req-3" {
		t.Fatalf("Ensure should adopt provided request id, got %q", corr.RequestID)
	}
	if !corr.Valid() {
		t.Fatalf("Ensure should produce valid correlation, got %#v", corr)
	}
	if FromContext(ctx).TraceID != corr.TraceID {
		t.Fatalf("Ensure context mismatch")
	}
}

func TestFromContextZeroWhenAbsent(t *testing.T) {
	corr := FromContext(context.Background())
	if corr.TraceID != "" || corr.SpanID != "" {
		t.Fatalf("expected zero correlation from bare context, got %#v", corr)
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	corr := New("req-1")
	tp := corr.Traceparent()
	if tp == "" {
		t.Fatalf("expected non-empty traceparent")
	}
	traceID, spanID, ok := ParseTraceparent(tp)
	if !ok {
		t.Fatalf("traceparent round-trip failed: %q", tp)
	}
	if traceID != corr.TraceID || spanID != corr.SpanID {
		t.Fatalf("round-trip ids mismatch: got %q/%q, want %q/%q",
			traceID, spanID, corr.TraceID, corr.SpanID)
	}
}

func TestParseTraceparentRejectsBadInput(t *testing.T) {
	cases := []string{
		"",
		"00-bad",
		"01-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01",
		"00-" + strings.Repeat("z", 32) + "-" + strings.Repeat("b", 16) + "-01",
		"00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("b", 16) + "-01",
	}
	for _, raw := range cases {
		if _, _, ok := ParseTraceparent(raw); ok {
			t.Fatalf("ParseTraceparent should reject %q", raw)
		}
	}
}

func TestAttrsIncludesAllFields(t *testing.T) {
	parent := New("req-1")
	corr := parent.Child().WithIdentityAttributes(IdentityAttribute{Key: "job_id", Value: "job-1"})
	attrs := corr.Attrs()
	gotKeys := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		gotKeys[attr.Key] = attr.Value.String()
	}
	wantKeys := []string{"trace_id", "span_id", "parent_span_id", "request_id", "job_id"}
	for _, key := range wantKeys {
		if _, ok := gotKeys[key]; !ok {
			t.Fatalf("Attrs missing %q; got %v", key, gotKeys)
		}
	}
}

func TestAppendAttrsSkipsExistingKeys(t *testing.T) {
	corr := New("req-1")
	base := corr.Attrs()
	combined := AppendAttrs(base, corr)
	if len(combined) != len(base) {
		t.Fatalf("AppendAttrs should skip duplicates, got %d attrs from %d", len(combined), len(base))
	}
}

func TestHTTPHeaderRoundTrip(t *testing.T) {
	corr := New("req-1")
	header := corr.HTTPHeaders()
	if header.Get(HeaderTraceID) != string(corr.TraceID) {
		t.Fatalf("header trace_id mismatch: %q vs %q", header.Get(HeaderTraceID), corr.TraceID)
	}
	parsed := FromHTTPHeader(header, "")
	if parsed.TraceID != corr.TraceID {
		t.Fatalf("FromHTTPHeader trace id mismatch: %q vs %q", parsed.TraceID, corr.TraceID)
	}
	if parsed.ParentSpanID != corr.SpanID {
		t.Fatalf("FromHTTPHeader should treat inbound span as parent: %q vs %q",
			parsed.ParentSpanID, corr.SpanID)
	}
	if parsed.SpanID == corr.SpanID {
		t.Fatalf("FromHTTPHeader should mint fresh span, got same as parent: %q", parsed.SpanID)
	}
	if parsed.RequestID != corr.RequestID {
		t.Fatalf("FromHTTPHeader request id mismatch: %q vs %q", parsed.RequestID, corr.RequestID)
	}
}

func TestFromHTTPHeaderExplicitParentOverridesInbound(t *testing.T) {
	corr := New("req-1")
	header := corr.HTTPHeaders()
	header.Set(HeaderParentSpanID, strings.Repeat("f", 16))

	parsed := FromHTTPHeader(header, "")
	if string(parsed.ParentSpanID) != strings.Repeat("f", 16) {
		t.Fatalf("explicit x-parent-span-id should override, got %q", parsed.ParentSpanID)
	}
}

func TestFromHTTPHeaderFallsBackToRequestID(t *testing.T) {
	parsed := FromHTTPHeader(http.Header{}, "req-fallback")
	if parsed.RequestID != "req-fallback" {
		t.Fatalf("request id fallback mismatch: %q", parsed.RequestID)
	}
	if !parsed.Valid() {
		t.Fatalf("expected fallback correlation to be valid: %#v", parsed)
	}
}

func TestFromHTTPHeaderTraceparentResetsSpan(t *testing.T) {
	header := http.Header{}
	header.Set(HeaderTraceparent, "00-"+strings.Repeat("a", 32)+"-"+strings.Repeat("b", 16)+"-01")
	parsed := FromHTTPHeader(header, "req")
	if string(parsed.TraceID) != strings.Repeat("a", 32) {
		t.Fatalf("traceparent trace id not adopted, got %q", parsed.TraceID)
	}
	if string(parsed.ParentSpanID) != strings.Repeat("b", 16) {
		t.Fatalf("traceparent span id should be parent, got %q", parsed.ParentSpanID)
	}
	if !parsed.Valid() {
		t.Fatalf("expected valid correlation, got %#v", parsed)
	}
}

func TestGRPCMetadataRoundTrip(t *testing.T) {
	corr := New("req-1")
	md := corr.Metadata()
	ctx := metadata.NewIncomingContext(context.Background(), md)
	parsed := FromIncomingMetadata(ctx)
	if parsed.TraceID != corr.TraceID {
		t.Fatalf("metadata trace id mismatch: %q vs %q", parsed.TraceID, corr.TraceID)
	}
	if parsed.ParentSpanID != corr.SpanID {
		t.Fatalf("metadata should treat inbound span as parent: %q vs %q",
			parsed.ParentSpanID, corr.SpanID)
	}
}

func TestFromIncomingMetadataWithoutMD(t *testing.T) {
	corr := FromIncomingMetadata(context.Background())
	if !corr.Valid() {
		t.Fatalf("expected fresh correlation when no metadata present, got %#v", corr)
	}
}

func TestNewOutgoingContextEnsuresCorrelation(t *testing.T) {
	ctx := NewOutgoingContext(context.Background())
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatalf("expected outgoing metadata")
	}
	if got := firstMetadata(md, HeaderTraceID); !validTraceID(got) {
		t.Fatalf("outgoing metadata trace id invalid: %q", got)
	}
}

func TestWithIdentityAttributesReplacesByKey(t *testing.T) {
	corr := New("req").WithIdentityAttributes(
		IdentityAttribute{Key: "job_id", Value: "job-1"},
		IdentityAttribute{Key: "job_id", Value: "job-2"},
	)
	if got := corr.IdentityAttributeValue("job_id"); got != "job-2" {
		t.Fatalf("identity attribute not replaced: %q", got)
	}
}

func TestWithIdentityAttributesIgnoresEmpty(t *testing.T) {
	corr := New("req").WithIdentityAttributes(
		IdentityAttribute{Key: "", Value: "v"},
		IdentityAttribute{Key: "k", Value: "  "},
	)
	if len(corr.IdentityAttributes) != 0 {
		t.Fatalf("expected no identity attributes from empty inputs, got %#v", corr.IdentityAttributes)
	}
}

func TestValidRejectsAllZero(t *testing.T) {
	if validTraceID(strings.Repeat("0", 32)) {
		t.Fatalf("validTraceID should reject all-zero trace id")
	}
	if validSpanID(strings.Repeat("0", 16)) {
		t.Fatalf("validSpanID should reject all-zero span id")
	}
}
