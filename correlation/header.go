package correlation

import "strings"

// HeaderMarker prefixes the one-line correlation header so a reader can spot it
// at a glance and a downstream stage can detect a body that already carries one.
// It is the magnifying-glass glyph followed by a single space.
const HeaderMarker = "🔎 "

// MarkerLine renders pairs as one correlation header line: [HeaderMarker]
// followed by space-joined key=value fields. pairs is a flat key, value, key,
// value sequence. A pair whose key or value is empty is skipped, and a trailing
// key with no value is ignored. The result is "" when no pair contributes a
// field, so a caller can print it unconditionally and emit nothing when there is
// nothing to show.
func MarkerLine(pairs ...string) string {
	fields := make([]string, 0, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		key := strings.TrimSpace(pairs[index])
		value := strings.TrimSpace(pairs[index+1])
		if key == "" || value == "" {
			continue
		}
		fields = append(fields, key+"="+value)
	}
	if len(fields) == 0 {
		return ""
	}
	return HeaderMarker + strings.Join(fields, " ")
}

// HeaderLine renders c's present core identifiers, in the canonical order
// trace_id, span_id, parent_span_id, request_id, followed by the extra key,
// value pairs, as one [MarkerLine]. Empty fields are skipped, and the result is
// "" when nothing is present.
func HeaderLine(c Context, extra ...string) string {
	pairs := make([]string, 0, 8+len(extra))
	pairs = append(pairs,
		"trace_id", string(c.TraceID),
		"span_id", string(c.SpanID),
		"parent_span_id", string(c.ParentSpanID),
		"request_id", c.RequestID,
	)
	pairs = append(pairs, extra...)
	return MarkerLine(pairs...)
}

// Meta is the JSON projection of a [Context]'s core identifiers. The field set
// matches the text header so the two forms stay in lockstep; empty fields are
// omitted on marshal.
type Meta struct {
	RequestID    string `json:"request_id,omitempty"`
	TraceID      string `json:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
}

// Meta returns the JSON projection of c's core identifiers.
func (c Context) Meta() Meta {
	return Meta{
		RequestID:    strings.TrimSpace(c.RequestID),
		TraceID:      string(c.TraceID),
		SpanID:       string(c.SpanID),
		ParentSpanID: string(c.ParentSpanID),
	}
}

// Empty reports whether m carries no field.
func (m Meta) Empty() bool {
	return m.RequestID == "" && m.TraceID == "" && m.SpanID == "" && m.ParentSpanID == ""
}
