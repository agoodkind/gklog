package gklog

import (
	"context"
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
}

// New creates a unified logger supporting text files, JSON files,
// JSON stdout (journald), and optional email handler. All log records
// are annotated with build metadata from goodkind.io/gklog/version.
func New(lc Config) (*slog.Logger, error) {
	var children []slog.Handler

	// Always add JSON handler to stdout for journald
	jsonOpts := &slog.HandlerOptions{Level: slog.LevelDebug}
	stdoutH := slog.NewJSONHandler(os.Stdout, jsonOpts)
	children = append(children, stdoutH)

	// Add text file handler if configured
	if strings.TrimSpace(lc.TextLogFile) != "" {
		textLJ := NewLumberjackWriter(lc.TextLogFile)
		txtH := NewTextHandler(textLJ, lc.TextLabel)
		children = append(children, txtH)
	}

	// Add JSON file handler if configured
	if strings.TrimSpace(lc.JSONLogFile) != "" {
		jsonLJ := NewLumberjackWriter(lc.JSONLogFile)
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

	logger := slog.New(NewTeeHandler(children...)).
		With("build", version.String())
	return logger, nil
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
