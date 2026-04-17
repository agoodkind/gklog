package gklog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"goodkind.io/gklog/emaillog"
	"goodkind.io/gklog/version"
)

// EmailSenderFunc is a function type for sending email notifications.
type EmailSenderFunc func(ctx context.Context, to, subject, body string) error

// Config holds all parameters for creating a logger.
type Config struct {
	TextLogFile        string
	JSONLogFile        string
	EmailSend          EmailSenderFunc // nil = no email handler
	EmailTo            string
	EmailMinLevel      string
	EmailCooldown      string
	TextLabel          string
	EmailSubjectPrefix string
	// Rotation controls log rotation for TextLogFile and JSONLogFile.
	// Zero values use defaults (5MB, keep forever, compressed).
	Rotation RotationConfig
	// DisableStdout skips the JSON stdout handler (for programs where stdout is reserved).
	DisableStdout bool
	// JSONMinLevel is the minimum level for JSON stdout and JSONLogFile handlers.
	// Empty or unknown values default to debug.
	JSONMinLevel string
}

// New creates a unified logger supporting text files, JSON files,
// JSON stdout (journald), and optional email handler. All log records
// are annotated with build metadata from goodkind.io/gklog/version.
// The returned io.Closer must be closed to release rotating log files; it may be a no-op.
func New(lc Config) (*slog.Logger, io.Closer, error) {
	jsonOpts := &slog.HandlerOptions{Level: parseJSONMinLevel(lc.JSONMinLevel)}

	var closers []io.Closer
	var children []slog.Handler

	// JSON handler to stdout for journald (optional).
	if !lc.DisableStdout {
		stdoutH := slog.NewJSONHandler(os.Stdout, jsonOpts)
		children = append(children, stdoutH)
	}

	// Add text file handler if configured
	if strings.TrimSpace(lc.TextLogFile) != "" {
		textLJ := NewLumberjackWriterWithConfig(lc.TextLogFile, lc.Rotation)
		closers = append(closers, textLJ)
		txtH := NewTextHandler(textLJ, lc.TextLabel)
		children = append(children, txtH)
	}

	// Add JSON file handler if configured
	if strings.TrimSpace(lc.JSONLogFile) != "" {
		jsonLJ := NewLumberjackWriterWithConfig(lc.JSONLogFile, lc.Rotation)
		closers = append(closers, jsonLJ)
		jsonH := slog.NewJSONHandler(jsonLJ, jsonOpts)
		children = append(children, jsonH)
	}

	// Add email handler if sender and recipient configured
	if lc.EmailSend != nil && strings.TrimSpace(lc.EmailTo) != "" {
		threshold := ParseEmailMinLevel(lc.EmailMinLevel)
		cd := 5 * time.Minute // default
		if lc.EmailCooldown != "" {
			if parsed, err := time.ParseDuration(lc.EmailCooldown); err == nil {
				cd = parsed
			}
		}

		senderAdapter := &senderFuncAdapter{fn: lc.EmailSend}
		emailH := emaillog.New(threshold, cd, senderAdapter, lc.EmailTo, lc.EmailSubjectPrefix)
		children = append(children, emailH)
	}

	if len(children) == 0 {
		return nil, nil, fmt.Errorf("gklog: no log outputs enabled (enable stdout or a log file)")
	}

	logger := slog.New(NewTeeHandler(children...)).
		With("build", version.String())
	return logger, multiCloser(closers), nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var first error
	for _, c := range m {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func parseJSONMinLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "debug", "":
		return slog.LevelDebug
	default:
		return slog.LevelDebug
	}
}

// senderFuncAdapter adapts EmailSenderFunc to the emaillog.Sender interface.
type senderFuncAdapter struct {
	fn EmailSenderFunc
}

// Send implements emaillog.Sender interface.
func (a *senderFuncAdapter) Send(ctx context.Context, to, subject, body string) error {
	return a.fn(ctx, to, subject, body)
}

// ParseEmailMinLevel converts a string to slog.Level for email alerts.
func ParseEmailMinLevel(s string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
