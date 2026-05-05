package gklog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"goodkind.io/gklog/emaillog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// StdoutJSON returns a [log/slog.Handler] that writes JSON records to
// stdout at level or above. Intended for journald capture; the
// systemd-journald daemon classifies records by their level field.
func StdoutJSON(level slog.Level) slog.Handler {
	return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
}

// FileText returns a [log/slog.Handler] that writes a human-friendly
// format (matching the project's bracketed-label text format) to a
// rotating, multi-process-locked file at path. The returned handler
// also implements [io.Closer] so the caller's New result will close
// the underlying file handle when its Closer fires.
//
// label is the bracketed prefix on every line, e.g. "[mwan-watchdog]".
// rot controls rotation; pass RotationConfig{} for the defaults
// (5MB, keep forever, compressed, local time).
func FileText(path, label string, rot RotationConfig) slog.Handler {
	w := NewLockedWriteCloser(path, NewLumberjackWriterWithConfig(path, rot))
	return &closableHandler{Handler: NewTextHandler(w, label), closer: w}
}

// FileJSON returns a [log/slog.Handler] that writes JSON records at
// level or above to a rotating, multi-process-locked file at path.
// The returned handler also implements [io.Closer] so the caller's
// New result will close the underlying file handle when its Closer
// fires.
//
// rot controls rotation; pass RotationConfig{} for the defaults
// (5MB, keep forever, compressed, local time).
func FileJSON(path string, level slog.Level, rot RotationConfig) slog.Handler {
	w := NewLockedWriteCloser(path, NewLumberjackWriterWithConfig(path, rot))
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return &closableHandler{Handler: inner, closer: w}
}

// EmailHandler returns a [log/slog.Handler] that emails records at
// threshold or above via sender, with a per-message-string cooldown
// to prevent floods during sustained outages. subjectPrefix is
// prepended to every outgoing subject (e.g. "[mwan]"); empty for
// none.
func EmailHandler(threshold slog.Level, cooldown time.Duration, sender emaillog.Sender, to, subjectPrefix string) slog.Handler {
	return emaillog.New(threshold, cooldown, sender, to, subjectPrefix)
}

// closableHandler wraps a [log/slog.Handler] with an [io.Closer] so
// the underlying writer is released when the gklog factory's Closer
// runs. Forwards Enabled/Handle/WithAttrs/WithGroup to the inner
// handler.
type closableHandler struct {
	slog.Handler

	closer io.Closer
}

func (h *closableHandler) Close() error {
	if h.closer == nil {
		return nil
	}
	err := h.closer.Close()
	if err == nil {
		return nil
	}
	return &handlerCloseError{err: err}
}

// handlerCloseError reports a writer-close failure from a
// [closableHandler]. The original error is recoverable via [errors.As]
// or [errors.Unwrap].
type handlerCloseError struct {
	err error
}

func (e *handlerCloseError) Error() string {
	return "gklog: close handler writer: " + e.err.Error()
}

func (e *handlerCloseError) Unwrap() error { return e.err }

func (h *closableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &closableHandler{Handler: h.Handler.WithAttrs(attrs), closer: h.closer}
}

func (h *closableHandler) WithGroup(name string) slog.Handler {
	return &closableHandler{Handler: h.Handler.WithGroup(name), closer: h.closer}
}

// --- TeeHandler ----------------------------------------------------

// TeeHandler fans out a [log/slog.Record] to multiple child handlers.
type TeeHandler struct {
	children []slog.Handler
}

// NewTeeHandler returns a TeeHandler over the provided children.
func NewTeeHandler(children ...slog.Handler) *TeeHandler {
	return &TeeHandler{children: children}
}

// Enabled reports whether any child handler is enabled at level for ctx.
func (t *TeeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t.children {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches r to each enabled child. Per-child errors are
// aggregated into a [TeeChildError]; one child failure does not stop
// dispatch to the rest. Returns nil when every enabled child succeeded.
func (t *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range t.children {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &TeeChildError{Errs: errs}
}

// TeeChildError aggregates per-child errors from [TeeHandler.Handle].
// Use [errors.As] to recover the slice.
type TeeChildError struct {
	// Errs holds the non-nil per-child errors in dispatch order.
	Errs []error
}

// Error reports the per-child failures joined into a single string.
func (e *TeeChildError) Error() string {
	parts := make([]string, 0, len(e.Errs))
	for _, err := range e.Errs {
		parts = append(parts, err.Error())
	}
	return "tee handler: " + strings.Join(parts, "; ")
}

// Unwrap exposes the per-child errors for [errors.Is] / [errors.As].
func (e *TeeChildError) Unwrap() []error { return e.Errs }

// WithAttrs returns a TeeHandler with attrs applied to every child.
func (t *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, len(t.children))
	for i, h := range t.children {
		children[i] = h.WithAttrs(attrs)
	}
	return &TeeHandler{children: children}
}

// WithGroup returns a TeeHandler with the named group applied to every
// child.
func (t *TeeHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, len(t.children))
	for i, h := range t.children {
		children[i] = h.WithGroup(name)
	}
	return &TeeHandler{children: children}
}

// --- TextHandler ---------------------------------------------------

// TextHandler writes human-readable lines to a writer. Format:
//
//	2006-01-02 15:04:05 <label> LEVEL msg k1=v1 k2=v2
//
// Attrs added via [log/slog.Logger.With] and groups added via
// WithGroup are preserved across handler clones. Group prefixes are
// dotted into the rendered key (rpc.code=OK).
type TextHandler struct {
	mu     *sync.Mutex
	w      io.Writer
	label  string
	attrs  []slog.Attr
	groups []string
}

// NewTextHandler returns a TextHandler that writes to w with the given
// label prefix. Empty label is allowed (just dropped from the output).
// All clones produced by WithAttrs / WithGroup share the underlying
// writer mutex so output stays line-atomic across loggers.
func NewTextHandler(w io.Writer, label string) *TextHandler {
	return &TextHandler{w: w, label: label, mu: &sync.Mutex{}}
}

// Enabled reports whether the handler accepts level for ctx; the text
// handler accepts every record regardless of level.
func (*TextHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle renders r as a single human-readable line and writes it
// atomically under the handler's shared mutex.
func (h *TextHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("2006-01-02 15:04:05"))
	b.WriteByte(' ')
	if h.label != "" {
		b.WriteString(h.label)
		b.WriteByte(' ')
	}
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	textWriteAttrs(&b, "", h.attrs)
	prefix := textGroupPrefix(h.groups)
	r.Attrs(func(a slog.Attr) bool {
		textWriteAttr(&b, prefix, a)
		return true
	})
	b.WriteByte('\n')
	line := b.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.w.Write([]byte(line))
	return nil
}

// WithAttrs returns a clone whose subsequent records render the given
// attrs (wrapped in any active groups) before the per-record attrs.
func (h *TextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	out := &TextHandler{
		mu:     h.mu,
		w:      h.w,
		label:  h.label,
		attrs:  append(append([]slog.Attr(nil), h.attrs...), textWrapGroups(h.groups, attrs)...),
		groups: append([]string(nil), h.groups...),
	}
	return out
}

// WithGroup returns a clone that nests subsequent attrs under name.
// Empty name is a no-op (returns the receiver unchanged).
func (h *TextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := &TextHandler{
		mu:     h.mu,
		w:      h.w,
		label:  h.label,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append(append([]string(nil), h.groups...), name),
	}
	return out
}

func textWriteAttrs(b *strings.Builder, prefix string, attrs []slog.Attr) {
	for _, a := range attrs {
		textWriteAttr(b, prefix, a)
	}
}

func textWriteAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		nested := a.Key
		if prefix != "" {
			nested = prefix + "." + a.Key
		}
		textWriteAttrs(b, nested, a.Value.Group())
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	fmt.Fprintf(b, "%v", a.Value.Any())
}

func textWrapGroups(groups []string, attrs []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return attrs
	}
	wrapped := append([]slog.Attr(nil), attrs...)
	for i := len(groups) - 1; i >= 0; i-- {
		wrapped = []slog.Attr{{
			Key:   groups[i],
			Value: slog.GroupValue(wrapped...),
		}}
	}
	return wrapped
}

func textGroupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".")
}

// --- Rotation + locked writer ---------------------------------------

// RotationConfig controls log rotation. Zero values fall back to
// sensible defaults (5MB, keep forever, compressed, local time).
type RotationConfig struct {
	MaxSizeMB  int   `toml:"max_size_mb"`  // rotate when file exceeds this size; default 5
	MaxBackups int   `toml:"max_backups"`  // number of rotated files to retain; 0 = unlimited
	MaxAgeDays int   `toml:"max_age_days"` // days to retain rotated files; 0 = unlimited
	Compress   *bool `toml:"compress"`     // nil = compress (default true); explicit false disables
	LocalTime  *bool `toml:"local_time"`   // nil = local time (default true); explicit false uses UTC
}

// NewLumberjackWriter returns a rotating log writer for the given path
// using default rotation settings (5MB, keep forever, compressed).
func NewLumberjackWriter(path string) *lumberjack.Logger {
	return NewLumberjackWriterWithConfig(path, RotationConfig{})
}

// NewLumberjackWriterWithConfig returns a rotating log writer for the
// given path with caller-supplied rotation settings. Zero values use
// defaults.
func NewLumberjackWriterWithConfig(path string, rc RotationConfig) *lumberjack.Logger {
	maxSize := rc.MaxSizeMB
	if maxSize <= 0 {
		maxSize = 5
	}
	compress := true
	if rc.Compress != nil {
		compress = *rc.Compress
	}
	localTime := true
	if rc.LocalTime != nil {
		localTime = *rc.LocalTime
	}
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSize,
		MaxBackups: rc.MaxBackups,
		MaxAge:     rc.MaxAgeDays,
		Compress:   compress,
		LocalTime:  localTime,
	}
}

// NewLockedWriteCloser wraps a write closer with a sidecar file lock
// so independent processes sharing the same log path serialize each
// Write call. This matters for JSONL durability: overlapping daemon
// instances or a restart during rotation should not interleave partial
// records into the same file.
func NewLockedWriteCloser(path string, wc io.WriteCloser) io.WriteCloser {
	if wc == nil {
		return nil
	}
	lockPath := path + ".lock"
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		lockPath = filepath.Join(dir, filepath.Base(path)+".lock")
	}
	return &lockedWriteCloser{
		lock: flock.New(lockPath),
		wc:   wc,
	}
}

type lockedWriteCloser struct {
	mu   sync.Mutex
	lock *flock.Flock
	wc   io.WriteCloser
}

func (w *lockedWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.lock.Lock(); err != nil {
		return 0, &lockedWriteError{op: "lock", err: err}
	}
	defer func() { _ = w.lock.Unlock() }()
	n, err := w.wc.Write(p)
	if err != nil {
		return n, &lockedWriteError{op: "write", err: err}
	}
	return n, nil
}

func (w *lockedWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wc == nil {
		return nil
	}
	if err := w.wc.Close(); err != nil {
		return &lockedWriteError{op: "close", err: err}
	}
	return nil
}

// lockedWriteError carries the underlying I/O failure from a
// [lockedWriteCloser] operation. The original error is recoverable via
// [errors.Unwrap] / [errors.As].
type lockedWriteError struct {
	op  string
	err error
}

func (e *lockedWriteError) Error() string {
	return "gklog: locked writer " + e.op + ": " + e.err.Error()
}

func (e *lockedWriteError) Unwrap() error { return e.err }

// --- private helpers ------------------------------------------------

func trimUpper(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
