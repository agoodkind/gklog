// Package telemetry provides independent runtime controls for diagnostics.
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// LogSettings configures diagnostic logging.
type LogSettings struct {
	Enabled bool
	Level   slog.Level
}

func (s LogSettings) validate() error {
	switch s.Level {
	case slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError:
		return nil
	default:
		return &settingsError{field: "log level", value: s.Level.String()}
	}
}

// LogStatus reports the current settings and whether the control has closed.
type LogStatus struct {
	LogSettings

	Closed bool
}

// LogControl applies runtime settings to selected diagnostic handlers.
// Callers compose audit handlers separately from Handler wrappers.
type LogControl struct {
	mu     sync.RWMutex
	status LogStatus
}

// NewLogControl validates settings and creates a logging control.
//
// Settings accept the standard DEBUG, INFO, WARN, and ERROR levels.
//
// Example:
//
//	control, err := NewLogControl(LogSettings{Enabled: true, Level: slog.LevelInfo})
func NewLogControl(settings LogSettings) (*LogControl, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}

	return &LogControl{status: LogStatus{LogSettings: settings}}, nil
}

// Update validates and applies settings unless Close has closed the control.
//
// Invalid settings leave the current state unchanged.
//
// Example:
//
//	err := control.Update(LogSettings{Enabled: true, Level: slog.LevelWarn})
func (c *LogControl) Update(settings LogSettings) error {
	if err := settings.validate(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.status.Closed {
		return ErrClosed
	}

	c.status.LogSettings = settings

	return nil
}

// Status returns a consistent copy of the current control state.
//
// Later updates do not change a previously returned value.
//
// Example:
//
//	status := control.Status()
func (c *LogControl) Status() LogStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.status
}

// Close disables diagnostic output and rejects later updates.
//
// Normal handler cleanup retains ownership of the wrapped handlers.
//
// Example:
//
//	err := control.Close()
func (c *LogControl) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Enabled = false
	c.status.Closed = true

	return nil
}

// Handler wraps a diagnostic handler without changing its existing filters.
//
// Derivatives share this control. The next handler must be non-nil.
//
// Example:
//
//	logger := slog.New(control.Handler(diagnosticHandler))
func (c *LogControl) Handler(next slog.Handler) slog.Handler {
	return &logHandler{control: c, next: next}
}

type logHandler struct {
	control *LogControl
	next    slog.Handler
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	status := h.control.Status()

	return status.Enabled && !status.Closed && level >= status.Level && h.next.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, record slog.Record) error {
	status := h.control.Status()
	if !status.Enabled || status.Closed || record.Level < status.Level {
		return nil
	}

	if !h.next.Enabled(ctx, record.Level) {
		return nil
	}

	if err := h.next.Handle(ctx, record); err != nil {
		return &operationError{operation: "handle diagnostic record", err: err}
	}

	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logHandler{control: h.control, next: h.next.WithAttrs(attrs)}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{control: h.control, next: h.next.WithGroup(name)}
}

func (h *logHandler) Close() error {
	closer, ok := h.next.(io.Closer)
	if !ok {
		return nil
	}

	if err := closer.Close(); err != nil {
		return &operationError{operation: "close diagnostic handler", err: err}
	}

	return nil
}
