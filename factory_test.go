package gklog

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestNewWithEmptyHandlersReturnsLogger(t *testing.T) {
	t.Parallel()
	log, closer := New(Config{BuildVersion: "test"})
	if log == nil {
		t.Fatal("nil logger")
	}
	if closer == nil {
		t.Fatal("nil closer")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Calling Close twice must be a no-op.
	if err := closer.Close(); err != nil {
		t.Fatalf("close x2: %v", err)
	}
}

func TestNewWithFileJSONClosesUnderlyingFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "out.log")
	log, closer := New(Config{
		BuildVersion: "v",
		Handlers:     []slog.Handler{FileJSON(p, slog.LevelDebug, RotationConfig{})},
	})
	log.Info("hello", "k", "v")
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestNewWithStdoutHandler(t *testing.T) {
	t.Parallel()
	log, closer := New(Config{
		BuildVersion: "v",
		Handlers:     []slog.Handler{StdoutJSON(slog.LevelInfo)},
	})
	defer func() { _ = closer.Close() }()
	log.Info("probe")
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"":         slog.LevelWarn,
		"debug":    slog.LevelDebug,
		"DEBUG":    slog.LevelDebug,
		"info":     slog.LevelInfo,
		"INFO":     slog.LevelInfo,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"WARNING":  slog.LevelWarn,
		"error":    slog.LevelError,
		"ERROR":    slog.LevelError,
		"  WARN  ": slog.LevelWarn,
		"bogus":    slog.LevelWarn,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if ParseEmailMinLevel("ERROR") != slog.LevelError {
		t.Fatal("ParseEmailMinLevel must alias ParseLevel")
	}
}

func TestNewLoggerWithDiscardHandler(t *testing.T) {
	t.Parallel()
	log, closer := New(Config{
		BuildVersion: "test-build-id",
		Handlers:     []slog.Handler{slog.NewJSONHandler(io.Discard, nil)},
	})
	defer func() { _ = closer.Close() }()
	if log == nil {
		t.Fatal("nil logger")
	}
}
