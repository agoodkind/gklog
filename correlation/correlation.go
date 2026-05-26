// Package correlation carries request, trace, and span identifiers
// across process boundaries via [context.Context], HTTP headers, and
// gRPC metadata. Pair it with [SlogHandler] to auto-inject the
// identifiers onto every log record that flows through a context
// carrying a [Context].
package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// HTTP header names in canonical form for use with [http.Header.Get]
// and [http.Header.Set]. gRPC metadata keys are lowercased on use
// (see [Context.Metadata]).
const (
	HeaderRequestID    = "X-Request-Id"
	HeaderTraceID      = "X-Trace-Id"
	HeaderSpanID       = "X-Span-Id"
	HeaderParentSpanID = "X-Parent-Span-Id"
	HeaderTraceparent  = "Traceparent"
)

// TraceID is the trace identifier shared by every record from one
// logical operation. It is a 32-character lowercase hex string (16
// bytes) matching the W3C Traceparent trace-id field.
type TraceID string

// SpanID is the identifier of one stage within a trace. It is a
// 16-character lowercase hex string (8 bytes) matching the W3C
// Traceparent span-id field.
type SpanID string

// IdentityAttribute is a key-value pair callers attach to a [Context]
// for fields that are not part of the core trace/span surface. The
// package treats keys and values as opaque strings and never inspects
// them.
type IdentityAttribute struct {
	Key   string
	Value string
}

// Context carries the trace, span, and request identifiers for one
// operation. It is a value type; pass it by value, store it on
// [context.Context] with [WithContext], and recover it with
// [FromContext].
type Context struct {
	TraceID            TraceID
	SpanID             SpanID
	ParentSpanID       SpanID
	RequestID          string
	IdentityAttributes []IdentityAttribute
}

type contextKey struct{}

// New returns a fresh [Context] with a new trace and span and the
// given request identifier. requestID is trimmed; pass "" to start
// without one.
func New(requestID string) Context {
	return Context{
		TraceID:   NewTraceID(),
		SpanID:    NewSpanID(),
		RequestID: strings.TrimSpace(requestID),
	}
}

// FromContext returns the [Context] stored on ctx with [WithContext],
// or the zero value when ctx is nil or carries no [Context].
func FromContext(ctx context.Context) Context {
	if ctx == nil {
		return Context{}
	}
	corr, ok := ctx.Value(contextKey{}).(Context)
	if !ok {
		return Context{}
	}
	return corr
}

// WithContext returns a child of ctx carrying corr. A nil ctx is
// treated as [context.Background].
func WithContext(ctx context.Context, corr Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, corr)
}

// Ensure returns ctx unchanged when it already carries a [Context]
// with a non-empty TraceID, and otherwise returns a child of ctx
// carrying a fresh [Context] built with [New]. requestID is used only
// when a new [Context] is built, or to back-fill RequestID on an
// existing [Context] that lacked one.
func Ensure(ctx context.Context, requestID string) (context.Context, Context) {
	corr := FromContext(ctx)
	if corr.TraceID == "" {
		corr = New(requestID)
	}
	if corr.RequestID == "" {
		corr.RequestID = strings.TrimSpace(requestID)
	}
	return WithContext(ctx, corr), corr
}

// WithRequestID returns a copy of c with RequestID set to the trimmed
// value.
func (c Context) WithRequestID(requestID string) Context {
	c.RequestID = strings.TrimSpace(requestID)
	return c
}

// Child returns a child span: a copy of c with a fresh SpanID, with
// the current SpanID moved to ParentSpanID, and with a fresh TraceID
// when c had none. IdentityAttributes are copied so child mutations
// do not leak back into the parent.
func (c Context) Child() Context {
	if c.TraceID == "" {
		c.TraceID = NewTraceID()
	}
	if c.SpanID != "" {
		c.ParentSpanID = c.SpanID
	}
	c.SpanID = NewSpanID()
	if len(c.IdentityAttributes) > 0 {
		copied := make([]IdentityAttribute, len(c.IdentityAttributes))
		copy(copied, c.IdentityAttributes)
		c.IdentityAttributes = copied
	}
	return c
}

// Valid reports whether c carries a well-formed TraceID and SpanID.
func (c Context) Valid() bool {
	return validTraceID(string(c.TraceID)) && validSpanID(string(c.SpanID))
}

// Traceparent renders c as a W3C traceparent value, or "" when c
// fails [Context.Valid].
func (c Context) Traceparent() string {
	if !c.Valid() {
		return ""
	}
	return "00-" + string(c.TraceID) + "-" + string(c.SpanID) + "-01"
}

// Attrs returns the [slog.Attr] form of c suitable for forwarding to a
// [slog.Logger]. Empty fields are omitted.
func (c Context) Attrs() []slog.Attr {
	attrs := make([]slog.Attr, 0, 4+len(c.IdentityAttributes))
	if c.TraceID != "" {
		attrs = append(attrs, slog.String("trace_id", string(c.TraceID)))
	}
	if c.SpanID != "" {
		attrs = append(attrs, slog.String("span_id", string(c.SpanID)))
	}
	if c.ParentSpanID != "" {
		attrs = append(attrs, slog.String("parent_span_id", string(c.ParentSpanID)))
	}
	if c.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", c.RequestID))
	}
	return appendIdentityAttributes(attrs, c.IdentityAttributes)
}

// AttrsFromContext is shorthand for FromContext(ctx).Attrs().
func AttrsFromContext(ctx context.Context) []slog.Attr {
	return FromContext(ctx).Attrs()
}

// AppendAttrs appends corr's attrs to attrs, skipping any keys already
// present in attrs. Returns the combined slice.
func AppendAttrs(attrs []slog.Attr, corr Context) []slog.Attr {
	corrAttrs := corr.Attrs()
	if len(attrs) == 0 {
		return corrAttrs
	}
	seen := make(map[string]bool, len(attrs))
	for _, attr := range attrs {
		seen[attr.Key] = true
	}
	for _, attr := range corrAttrs {
		if seen[attr.Key] {
			continue
		}
		attrs = append(attrs, attr)
		seen[attr.Key] = true
	}
	return attrs
}

// WithIdentityAttributes returns a copy of c with the given attributes
// merged in. A later attribute with the same key replaces an earlier
// one so a caller can back-fill from a more reliable source. Empty
// keys or values are dropped.
func (c Context) WithIdentityAttributes(attrs ...IdentityAttribute) Context {
	if len(attrs) == 0 {
		return c
	}
	merged := append([]IdentityAttribute(nil), c.IdentityAttributes...)
	for _, attr := range attrs {
		normalized, ok := attr.normalize()
		if !ok {
			continue
		}
		replaced := false
		for i := range merged {
			if merged[i].Key == normalized.Key {
				merged[i] = normalized
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, normalized)
		}
	}
	c.IdentityAttributes = merged
	return c
}

// IdentityAttributeValue returns the value attached for key, or "" when
// the key is absent.
func (c Context) IdentityAttributeValue(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, attr := range c.IdentityAttributes {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func (a IdentityAttribute) normalize() (IdentityAttribute, bool) {
	key := strings.TrimSpace(a.Key)
	value := strings.TrimSpace(a.Value)
	if key == "" || value == "" {
		return IdentityAttribute{}, false
	}
	return IdentityAttribute{Key: key, Value: value}, true
}

func appendIdentityAttributes(attrs []slog.Attr, identityAttrs []IdentityAttribute) []slog.Attr {
	if len(identityAttrs) == 0 {
		return attrs
	}
	seen := make(map[string]bool, len(attrs)+len(identityAttrs))
	for _, attr := range attrs {
		seen[attr.Key] = true
	}
	for _, identityAttr := range identityAttrs {
		normalized, ok := identityAttr.normalize()
		if !ok {
			continue
		}
		if seen[normalized.Key] {
			continue
		}
		attrs = append(attrs, slog.String(normalized.Key, normalized.Value))
		seen[normalized.Key] = true
	}
	return attrs
}

// FromHTTPHeader builds a [Context] from header, falling back to
// requestID when the inbound headers do not carry one. A valid
// traceparent header takes precedence over the discrete x-trace-id /
// x-span-id headers; explicit x-trace-id and x-span-id headers
// override the traceparent values when both are present.
func FromHTTPHeader(header http.Header, requestID string) Context {
	corr := New(requestID)
	if id := strings.TrimSpace(header.Get(HeaderRequestID)); id != "" {
		corr.RequestID = id
	}
	if traceID, spanID, ok := ParseTraceparent(header.Get(HeaderTraceparent)); ok {
		corr.TraceID = traceID
		corr.ParentSpanID = spanID
		corr.SpanID = NewSpanID()
	}
	if traceID := strings.TrimSpace(header.Get(HeaderTraceID)); validTraceID(traceID) {
		corr.TraceID = TraceID(traceID)
	}
	if spanID := strings.TrimSpace(header.Get(HeaderSpanID)); validSpanID(spanID) {
		corr.ParentSpanID = SpanID(spanID)
		corr.SpanID = NewSpanID()
	}
	if parentSpanID := strings.TrimSpace(header.Get(HeaderParentSpanID)); validSpanID(parentSpanID) {
		corr.ParentSpanID = SpanID(parentSpanID)
	}
	return corr
}

// SetHTTPHeaders writes c's trace, span, and request identifiers onto
// header. Empty fields are skipped. A nil header is a no-op.
func (c Context) SetHTTPHeaders(header http.Header) {
	if header == nil {
		return
	}
	if c.RequestID != "" {
		header.Set(HeaderRequestID, c.RequestID)
	}
	if c.TraceID != "" {
		header.Set(HeaderTraceID, string(c.TraceID))
	}
	if c.SpanID != "" {
		header.Set(HeaderSpanID, string(c.SpanID))
	}
	if c.ParentSpanID != "" {
		header.Set(HeaderParentSpanID, string(c.ParentSpanID))
	}
	if traceparent := c.Traceparent(); traceparent != "" {
		header.Set(HeaderTraceparent, traceparent)
	}
}

// HTTPHeaders returns a fresh [http.Header] populated by
// [Context.SetHTTPHeaders].
func (c Context) HTTPHeaders() http.Header {
	header := http.Header{}
	c.SetHTTPHeaders(header)
	return header
}

// FromIncomingMetadata builds a [Context] from gRPC incoming metadata,
// falling back to a fresh [Context] when no metadata is attached.
func FromIncomingMetadata(ctx context.Context) Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return New("")
	}
	return fromMetadata(md)
}

// NewOutgoingContext returns a child of ctx carrying outgoing gRPC
// metadata populated from the [Context] on ctx. If ctx has no
// [Context], one is built with [Ensure].
func NewOutgoingContext(ctx context.Context) context.Context {
	corr := FromContext(ctx)
	if corr.TraceID == "" {
		ctx, corr = Ensure(ctx, "")
	}
	return metadata.NewOutgoingContext(ctx, corr.Metadata())
}

// Metadata returns gRPC metadata populated from c. Empty fields are
// skipped.
func (c Context) Metadata() metadata.MD {
	md := metadata.MD{}
	setMetadata(md, HeaderRequestID, c.RequestID)
	setMetadata(md, HeaderTraceID, string(c.TraceID))
	setMetadata(md, HeaderSpanID, string(c.SpanID))
	setMetadata(md, HeaderParentSpanID, string(c.ParentSpanID))
	if traceparent := c.Traceparent(); traceparent != "" {
		setMetadata(md, HeaderTraceparent, traceparent)
	}
	return md
}

// ParseTraceparent parses a W3C traceparent value. Returns the trace
// id, parent span id, and true on success; zero values and false on
// any malformed input.
func ParseTraceparent(raw string) (TraceID, SpanID, bool) {
	parts := strings.Split(strings.TrimSpace(raw), "-")
	if len(parts) != 4 {
		return "", "", false
	}
	if parts[0] != "00" {
		return "", "", false
	}
	if !validTraceID(parts[1]) || !validSpanID(parts[2]) {
		return "", "", false
	}
	return TraceID(parts[1]), SpanID(parts[2]), true
}

// NewTraceID returns a fresh random 32-hex-character [TraceID].
func NewTraceID() TraceID {
	return TraceID(randomHex(16))
}

// NewSpanID returns a fresh random 16-hex-character [SpanID].
func NewSpanID() SpanID {
	return SpanID(randomHex(8))
}

func randomHex(byteCount int) string {
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", byteCount*2)
	}
	return hex.EncodeToString(buf)
}

func fromMetadata(md metadata.MD) Context {
	corr := New(firstMetadata(md, HeaderRequestID))
	if traceID, spanID, ok := ParseTraceparent(firstMetadata(md, HeaderTraceparent)); ok {
		corr.TraceID = traceID
		corr.ParentSpanID = spanID
		corr.SpanID = NewSpanID()
	}
	if traceID := firstMetadata(md, HeaderTraceID); validTraceID(traceID) {
		corr.TraceID = TraceID(traceID)
	}
	if spanID := firstMetadata(md, HeaderSpanID); validSpanID(spanID) {
		corr.ParentSpanID = SpanID(spanID)
		corr.SpanID = NewSpanID()
	}
	if parentSpanID := firstMetadata(md, HeaderParentSpanID); validSpanID(parentSpanID) {
		corr.ParentSpanID = SpanID(parentSpanID)
	}
	return corr
}

func firstMetadata(md metadata.MD, key string) string {
	values := md.Get(strings.ToLower(key))
	if len(values) == 0 {
		values = md.Get(key)
	}
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func setMetadata(md metadata.MD, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	md.Set(strings.ToLower(key), value)
}

func validTraceID(value string) bool {
	return validHex(value, 32) && value != strings.Repeat("0", 32)
}

func validSpanID(value string) bool {
	return validHex(value, 16) && value != strings.Repeat("0", 16)
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}
