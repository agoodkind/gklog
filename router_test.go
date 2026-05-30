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

// TestRouterConcernFromAttrWritesNestedPath proves the router can take the
// concern from a record attribute (per-record and via WithAttrs) and write it
// to a nested path produced by PathForConcern.
func TestRouterConcernFromAttrWritesNestedPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	router := NewRouter(dir, slog.LevelInfo, combined, RouterOptions{
		ConcernAttr: "concern",
		PathForConcern: func(concern string) string {
			return filepath.Join(strings.Split(concern, ".")...) + ".jsonl"
		},
	})
	logger := slog.New(router)

	logger.Info("mitm.proxy.forwarded", "concern", "providers.mitm.wire", "host", "api.anthropic.com")
	logger.With("concern", "providers.mitm.cursor").Info("mitm.proxy.forwarded", "host", "api2.cursor.sh")

	wire, err := os.ReadFile(filepath.Join(dir, "providers", "mitm", "wire.jsonl"))
	if err != nil {
		t.Fatalf("reading per-record nested concern file: %v", err)
	}
	if !strings.Contains(string(wire), "api.anthropic.com") {
		t.Fatalf("wire.jsonl missing record; got %q", string(wire))
	}
	cursor, err := os.ReadFile(filepath.Join(dir, "providers", "mitm", "cursor.jsonl"))
	if err != nil {
		t.Fatalf("reading WithAttrs nested concern file: %v", err)
	}
	if !strings.Contains(string(cursor), "api2.cursor.sh") {
		t.Fatalf("cursor.jsonl missing record; got %q", string(cursor))
	}
	if strings.Contains(string(wire), "api2.cursor.sh") {
		t.Fatalf("wire.jsonl leaked a cursor record; got %q", string(wire))
	}
}

// TestRouterPathForConcernEmptyRoutesCombinedOnly proves an empty path from
// PathForConcern keeps the record out of any per-concern file while the
// combined sink still receives it.
func TestRouterPathForConcernEmptyRoutesCombinedOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	router := NewRouter(dir, slog.LevelInfo, combined, RouterOptions{
		ConcernAttr:    "concern",
		PathForConcern: func(string) string { return "" },
	})
	slog.New(router).Info("x.y", "concern", "muted")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no per-concern files, got %d", len(entries))
	}
}

// TestRouterLevelForConcernRaisesThreshold proves a per-concern level filters
// that concern's file without affecting the router's overall threshold.
func TestRouterLevelForConcernRaisesThreshold(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	combined := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	router := NewRouter(dir, slog.LevelDebug, combined, RouterOptions{
		ConcernAttr: "concern",
		LevelForConcern: func(concern string) slog.Level {
			if concern == "quiet" {
				return slog.LevelWarn
			}
			return slog.LevelDebug
		},
	})
	logger := slog.New(router)

	logger.Info("a.b", "concern", "quiet") // below quiet's Warn threshold -> not in file
	logger.Warn("a.b", "concern", "quiet") // at threshold -> written

	data, err := os.ReadFile(filepath.Join(dir, "quiet.jsonl"))
	if err != nil {
		t.Fatalf("reading quiet.jsonl: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 1 {
		t.Fatalf("quiet.jsonl should hold exactly the Warn record, got %d lines: %q", got, string(data))
	}
}
