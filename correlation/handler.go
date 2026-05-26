package correlation

import (
	"context"
	"io"
	"log/slog"

	"goodkind.io/gklog/internal/clock"
)

// HandlerOptions configures [SlogHandler].
type HandlerOptions struct {
	// Strict turns on a missing-field check. When a record's context
	// carries a [Context] with a non-empty TraceID and the merged
	// record is missing any field listed in Required, the handler
	// emits an extra record named "correlation.missing_field" naming
	// the missing field and the source message. The check is silent
	// when the context carries no [Context] (no active span).
	Strict bool

	// Required lists the attribute keys that must be present on every
	// record under an active span. Only consulted when Strict is true.
	// Typical: []string{"trace_id", "span_id", "job_id", "codebase_id"}.
	Required []string
}

// SlogHandler wraps next so every record carries the trace, span, and
// request identifiers on its context. Missing attrs from the Context
// are added to the record; attrs already on the record are preserved.
// Pair the wrapper with [WithContext] at request boundaries.
func SlogHandler(next slog.Handler, opts HandlerOptions) slog.Handler {
	if next == nil {
		next = slog.DiscardHandler
	}
	return &slogHandler{
		next:     next,
		strict:   opts.Strict,
		required: append([]string(nil), opts.Required...),
		clock:    clock.System,
	}
}

type slogHandler struct {
	next     slog.Handler
	attrs    []slog.Attr
	strict   bool
	required []string
	clock    clock.Clock
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	corr := FromContext(ctx)
	corrAttrs := corr.Attrs()
	if len(corrAttrs) == 0 {
		if err := h.next.Handle(ctx, record); err != nil {
			return &handleError{err: err}
		}
		return nil
	}
	existing := h.attrKeySet(record)
	var missing []slog.Attr
	for _, attr := range corrAttrs {
		if !existing[attr.Key] {
			missing = append(missing, attr)
		}
	}
	enhanced := record
	if len(missing) > 0 {
		enhanced = record.Clone()
		enhanced.AddAttrs(missing...)
		for _, attr := range missing {
			existing[attr.Key] = true
		}
	}
	if h.strict && corr.TraceID != "" {
		for _, field := range h.required {
			if existing[field] {
				continue
			}
			h.emitMissingField(ctx, corr, field, record)
		}
	}
	if err := h.next.Handle(ctx, enhanced); err != nil {
		return &handleError{err: err}
	}
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &slogHandler{
		next:     h.next.WithAttrs(attrs),
		attrs:    append(append([]slog.Attr(nil), h.attrs...), attrs...),
		strict:   h.strict,
		required: h.required,
		clock:    h.clock,
	}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	return &slogHandler{
		next:     h.next.WithGroup(name),
		attrs:    append([]slog.Attr(nil), h.attrs...),
		strict:   h.strict,
		required: h.required,
		clock:    h.clock,
	}
}

// Close forwards to the wrapped handler when it implements [io.Closer].
func (h *slogHandler) Close() error {
	closer, ok := h.next.(io.Closer)
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		return &closeError{err: err}
	}
	return nil
}

// handleError carries a wrapped-handler failure from [slogHandler.Handle].
type handleError struct{ err error }

func (e *handleError) Error() string {
	return "gklog/correlation: handle: " + e.err.Error()
}

func (e *handleError) Unwrap() error { return e.err }

// closeError carries a wrapped-handler failure from [slogHandler.Close].
type closeError struct{ err error }

func (e *closeError) Error() string {
	return "gklog/correlation: close: " + e.err.Error()
}

func (e *closeError) Unwrap() error { return e.err }

func (h *slogHandler) attrKeySet(record slog.Record) map[string]bool {
	keys := make(map[string]bool, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		keys[attr.Key] = true
	}
	record.Attrs(func(attr slog.Attr) bool {
		keys[attr.Key] = true
		return true
	})
	return keys
}

func (h *slogHandler) emitMissingField(ctx context.Context, corr Context, field string, source slog.Record) {
	diag := slog.NewRecord(h.clock.Now(), slog.LevelWarn, "correlation.missing_field", source.PC)
	diag.AddAttrs(
		slog.String("missing_field", field),
		slog.String("source_msg", source.Message),
	)
	for _, attr := range corr.Attrs() {
		diag.AddAttrs(attr)
	}
	_ = h.next.Handle(ctx, diag)
}
