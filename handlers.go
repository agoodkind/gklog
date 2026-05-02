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

// StdoutJSON returns a slog.Handler that writes JSON records to stdout
// at level or above. Intended for journald capture; the systemd-journald
// daemon classifies records by their level field.
func StdoutJSON(level slog.Level) slog.Handler {
	return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
}

// FileText returns a slog.Handler that writes a human-friendly format
// (matching the project's [<label>]-prefixed text format) to a
// rotating, multi-process-locked file at path. The returned handler
// also implements io.Closer so the caller's New result will close the
// underlying file handle when its Closer fires.
//
// label is the bracketed prefix on every line, e.g. "[mwan-watchdog]".
// rot controls rotation; pass RotationConfig{} for the defaults
// (5MB, keep forever, compressed, local time).
func FileText(path, label string, rot RotationConfig) slog.Handler {
	w := NewLockedWriteCloser(path, NewLumberjackWriterWithConfig(path, rot))
	return &closableHandler{Handler: NewTextHandler(w, label), closer: w}
}

// FileJSON returns a slog.Handler that writes JSON records at level or
// above to a rotating, multi-process-locked file at path. The returned
// handler also implements io.Closer so the caller's New result will
// close the underlying file handle when its Closer fires.
//
// rot controls rotation; pass RotationConfig{} for the defaults
// (5MB, keep forever, compressed, local time).
func FileJSON(path string, level slog.Level, rot RotationConfig) slog.Handler {
	w := NewLockedWriteCloser(path, NewLumberjackWriterWithConfig(path, rot))
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return &closableHandler{Handler: inner, closer: w}
}

// EmailHandler returns a slog.Handler that emails records at threshold
// or above via sender, with a per-message-string cooldown to prevent
// floods during sustained outages. subjectPrefix is prepended to every
// outgoing subject (e.g. "[mwan]"); empty for none.
func EmailHandler(threshold slog.Level, cooldown time.Duration, sender emaillog.Sender, to, subjectPrefix string) slog.Handler {
	return emaillog.New(threshold, cooldown, sender, to, subjectPrefix)
}

// closableHandler wraps a slog.Handler with an io.Closer so the
// underlying writer is released when the gklog factory's Closer runs.
// Forwards Enabled/Handle/WithAttrs/WithGroup to the inner handler.
type closableHandler struct {
	slog.Handler
	closer io.Closer
}

func (h *closableHandler) Close() error {
	if h.closer == nil {
		return nil
	}
	return h.closer.Close()
}

func (h *closableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &closableHandler{Handler: h.Handler.WithAttrs(attrs), closer: h.closer}
}

func (h *closableHandler) WithGroup(name string) slog.Handler {
	return &closableHandler{Handler: h.Handler.WithGroup(name), closer: h.closer}
}

// --- TeeHandler ----------------------------------------------------

// TeeHandler fans out a slog.Record to multiple child handlers.
type TeeHandler struct {
	children []slog.Handler
}

// NewTeeHandler returns a TeeHandler over the provided children.
func NewTeeHandler(children ...slog.Handler) *TeeHandler {
	return &TeeHandler{children: children}
}

func (t *TeeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t.children {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (t *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range t.children {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, len(t.children))
	for i, h := range t.children {
		children[i] = h.WithAttrs(attrs)
	}
	return &TeeHandler{children: children}
}

func (t *TeeHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, len(t.children))
	for i, h := range t.children {
		children[i] = h.WithGroup(name)
	}
	return &TeeHandler{children: children}
}

// --- TextHandler ---------------------------------------------------

// TextHandler writes human-readable lines to a writer.
// Format: 2006-01-02 15:04:05 <label> LEVEL msg key=val key=val
type TextHandler struct {
	mu    sync.Mutex
	w     io.Writer
	label string
}

// NewTextHandler returns a TextHandler that writes to w with the given
// label prefix. Empty label is allowed (just dropped from the output).
func NewTextHandler(w io.Writer, label string) *TextHandler {
	return &TextHandler{w: w, label: label}
}

func (h *TextHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

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
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		fmt.Fprintf(&b, "%v", a.Value.Any())
		return true
	})
	b.WriteByte('\n')
	line := b.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.w.Write([]byte(line))
	return nil
}

func (h *TextHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *TextHandler) WithGroup(string) slog.Handler      { return h }

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
		return 0, err
	}
	defer func() { _ = w.lock.Unlock() }()
	return w.wc.Write(p)
}

func (w *lockedWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wc == nil {
		return nil
	}
	return w.wc.Close()
}

// --- private helpers ------------------------------------------------

func trimUpper(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
