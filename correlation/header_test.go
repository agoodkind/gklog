package correlation

import (
	"encoding/json"
	"testing"
)

func TestMarkerLineJoinsNonEmptyPairs(t *testing.T) {
	t.Parallel()
	got := MarkerLine("logs", ".make/logs", "trace_id", "abc", "span_id", "def")
	want := "🔎 logs=.make/logs trace_id=abc span_id=def"
	if got != want {
		t.Fatalf("MarkerLine = %q, want %q", got, want)
	}
}

func TestMarkerLineSkipsEmptyValues(t *testing.T) {
	t.Parallel()
	got := MarkerLine("trace_id", "abc", "span_id", "", "job_id", "  ")
	want := "🔎 trace_id=abc"
	if got != want {
		t.Fatalf("MarkerLine = %q, want %q", got, want)
	}
}

func TestMarkerLineEmptyWhenNothing(t *testing.T) {
	t.Parallel()
	if got := MarkerLine("span_id", "", "job_id", ""); got != "" {
		t.Fatalf("MarkerLine = %q, want empty", got)
	}
	if got := MarkerLine(); got != "" {
		t.Fatalf("MarkerLine() = %q, want empty", got)
	}
}

func TestMarkerLineIgnoresTrailingKey(t *testing.T) {
	t.Parallel()
	got := MarkerLine("trace_id", "abc", "dangling")
	want := "🔎 trace_id=abc"
	if got != want {
		t.Fatalf("MarkerLine = %q, want %q", got, want)
	}
}

func TestHeaderLineCanonicalOrderWithExtra(t *testing.T) {
	t.Parallel()
	corr := Context{
		TraceID:      "11111111111111111111111111111111",
		SpanID:       "2222222222222222",
		ParentSpanID: "3333333333333333",
		RequestID:    "req-1",
	}
	got := HeaderLine(corr, "logs", ".make/logs")
	want := "🔎 trace_id=11111111111111111111111111111111 span_id=2222222222222222 " +
		"parent_span_id=3333333333333333 request_id=req-1 logs=.make/logs"
	if got != want {
		t.Fatalf("HeaderLine = %q, want %q", got, want)
	}
}

func TestHeaderLineOmitsEmptyCoreFields(t *testing.T) {
	t.Parallel()
	corr := Context{
		TraceID: "11111111111111111111111111111111",
		SpanID:  "2222222222222222",
	}
	got := HeaderLine(corr)
	want := "🔎 trace_id=11111111111111111111111111111111 span_id=2222222222222222"
	if got != want {
		t.Fatalf("HeaderLine = %q, want %q", got, want)
	}
}

func TestMetaJSONOrderAndOmitEmpty(t *testing.T) {
	t.Parallel()
	corr := Context{
		RequestID: "req-456",
		TraceID:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpanID:    "bbbbbbbbbbbbbbbb",
	}
	encoded, err := json.Marshal(corr.Meta())
	if err != nil {
		t.Fatalf("marshal Meta: %v", err)
	}
	want := `{"request_id":"req-456","trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","span_id":"bbbbbbbbbbbbbbbb"}`
	if string(encoded) != want {
		t.Fatalf("Meta JSON = %s, want %s", encoded, want)
	}
}

func TestMetaEmpty(t *testing.T) {
	t.Parallel()
	if !(Context{}).Meta().Empty() {
		t.Fatal("zero Context Meta should be Empty")
	}
	if (Context{TraceID: "x"}).Meta().Empty() {
		t.Fatal("Meta with a trace id should not be Empty")
	}
}
