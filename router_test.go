package gklog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRouterWritesPerConcernFiles proves a record routes to the file named by
// the first dot-separated segment of its message, and a message with no dot
// routes to the fallback concern.
func TestRouterWritesPerConcernFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(NewRouter(dir, slog.LevelInfo, combined, RouterOptions{FallbackConcern: "core"}))

	logger.Info("semantic.reindex", "path", "a.go")
	logger.Info("mcp.tool.started", "tool", "search_code")
	logger.Info("startup complete")

	cases := map[string]string{
		"semantic.jsonl": "semantic.reindex",
		"mcp.jsonl":      "mcp.tool.started",
		"core.jsonl":     "startup complete",
	}
	for file, want := range cases {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s missing %q; got %q", file, want, string(data))
		}
	}

	semanticData, err := os.ReadFile(filepath.Join(dir, "semantic.jsonl"))
	if err != nil {
		t.Fatalf("reading semantic.jsonl: %v", err)
	}
	if strings.Contains(string(semanticData), "mcp.tool.started") {
		t.Fatalf("semantic.jsonl leaked an mcp record: %q", string(semanticData))
	}
}

// TestRouterEnabledRespectsLevel proves the level threshold gates records.
func TestRouterEnabledRespectsLevel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})
	router := NewRouter(dir, slog.LevelWarn, combined, RouterOptions{})
	if router.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled(Info) = true, want false at Warn threshold")
	}
	if !router.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled(Error) = false, want true at Warn threshold")
	}
}

// TestRouterDefaultsFallbackConcern proves an empty FallbackConcern resolves
// to the documented "default" sink.
func TestRouterDefaultsFallbackConcern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(NewRouter(dir, slog.LevelInfo, combined, RouterOptions{}))

	logger.Info("plain message")

	data, err := os.ReadFile(filepath.Join(dir, "default.jsonl"))
	if err != nil {
		t.Fatalf("reading default.jsonl: %v", err)
	}
	if !strings.Contains(string(data), "plain message") {
		t.Fatalf("default.jsonl missing record; got %q", string(data))
	}
}
