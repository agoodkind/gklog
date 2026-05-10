// Package gklog provides structured logging primitives layered on
// [log/slog]: a fan-out tee handler, a human-friendly text handler,
// optional rotating file output via lumberjack, an emaillog handler,
// and a context-scoped logger helper.
//
// API shape: callers compose a list of [log/slog.Handler] instances
// via the constructor functions in handlers.go (StdoutJSON, FileText,
// FileJSON, EmailHandler) and pass them through Config to New. Adding
// a new sink is a new constructor; New itself stays unchanged.
//
// Example:
//
//	handlers := []slog.Handler{gklog.StdoutJSON(slog.LevelDebug)}
//	if path != "" {
//	    handlers = append(handlers, gklog.FileJSON(path, slog.LevelDebug, gklog.RotationConfig{}))
//	}
//	log, closer := gklog.New(gklog.Config{
//	    BuildVersion: buildVersion,
//	    Handlers:     handlers,
//	})
//	defer closer.Close()
package gklog

import (
	"io"
	"log/slog"
)

// Config is what callers compose for New: the handler list and a
// build-version annotation that should accompany every record.
type Config struct {
	// BuildVersion is appended as a "build" attribute to every record
	// via slog.Logger.With. Caller chooses the format.
	BuildVersion string

	// Handlers is the ordered list of slog.Handler children that the
	// returned logger fans out to via a TeeHandler. Empty Handlers
	// returns a logger that drops every record (intended for tests).
	Handlers []slog.Handler
}

// New composes cfg.Handlers via TeeHandler and returns a logger plus
// a Closer that releases any handlers implementing [io.Closer]
// (typically those wrapping rotating file writers). The Closer never
// errors when called multiple times; calling it on a logger built from
// non-Closer handlers is a no-op.
func New(cfg Config) (*slog.Logger, io.Closer) {
	logger := slog.New(NewTeeHandler(cfg.Handlers...)).With("build", cfg.BuildVersion)
	return logger, &handlerListCloser{handlers: cfg.Handlers}
}

type handlerListCloser struct {
	handlers []slog.Handler
	closed   bool
}

type parsedLevelName string

const (
	parsedLevelDebug   parsedLevelName = "DEBUG"
	parsedLevelInfo    parsedLevelName = "INFO"
	parsedLevelWarn    parsedLevelName = "WARN"
	parsedLevelWarning parsedLevelName = "WARNING"
	parsedLevelError   parsedLevelName = "ERROR"
)

func (c *handlerListCloser) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	var first error
	for _, h := range c.handlers {
		cl, ok := h.(io.Closer)
		if !ok {
			continue
		}
		closeErr := cl.Close()
		if closeErr != nil && first == nil {
			first = closeErr
		}
	}
	return first
}

// ParseLevel converts "DEBUG", "INFO", "WARN"/"WARNING", "ERROR" (case
// insensitive, surrounding whitespace ignored) to [log/slog.Level].
// Empty or unrecognised input returns [log/slog.LevelWarn]. Used by
// the email handler constructor and any caller that ingests level
// strings from config.
func ParseLevel(s string) slog.Level {
	switch parsedLevelName(trimUpper(s)) {
	case parsedLevelDebug:
		return slog.LevelDebug
	case parsedLevelInfo:
		return slog.LevelInfo
	case parsedLevelWarn, parsedLevelWarning:
		return slog.LevelWarn
	case parsedLevelError:
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// ParseEmailMinLevel is the legacy name of ParseLevel preserved for
// callers that imported the v0.1 API.
//
// Deprecated: use ParseLevel.
func ParseEmailMinLevel(s string) slog.Level { return ParseLevel(s) }
