package gklog

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
)

// Router is a [slog.Handler] that writes every record to a combined handler
// and to a per-concern JSONL file selected from the record message. The
// concern is the first dot-separated segment of the message (so
// "semantic.reindex" lands in semantic.jsonl and "mcp.tool.started" in
// mcp.jsonl), with a fallback concern for messages that carry no dot. Concern
// files are created lazily under dir on first use and share one
// [RotationConfig].
//
// Pair Router with a correlation-injecting outer handler when correlation ids
// must reach every concern file: route the outer handler at Router and the
// outer handler enriches each record before routing.
type Router struct {
	dir             string
	level           slog.Level
	rotation        RotationConfig
	fallbackConcern string
	combined        slog.Handler

	mu       *sync.Mutex
	children map[string]slog.Handler

	attrs  []slog.Attr
	groups []string
}

// RouterOptions configures a [Router]. Zero values pick safe defaults:
// FallbackConcern defaults to "default" and Rotation uses [RotationConfig]'s
// own zero-value defaults (matching [FileJSON]).
type RouterOptions struct {
	FallbackConcern string
	Rotation        RotationConfig
}

// NewRouter returns a Router that writes concern files under dir at level or
// above and mirrors every record to combined. combined must not be nil; pass
// [slog.DiscardHandler] when no combined sink is desired.
func NewRouter(dir string, level slog.Level, combined slog.Handler, opts RouterOptions) *Router {
	fallback := opts.FallbackConcern
	if fallback == "" {
		fallback = "default"
	}
	return &Router{
		dir:             dir,
		level:           level,
		rotation:        opts.Rotation,
		fallbackConcern: fallback,
		combined:        combined,
		mu:              &sync.Mutex{},
		children:        make(map[string]slog.Handler),
		attrs:           nil,
		groups:          nil,
	}
}

// Enabled reports whether the level clears the Router threshold.
func (router *Router) Enabled(_ context.Context, level slog.Level) bool {
	return level >= router.level
}

// Handle writes the record to the combined sink and the concern file on a
// best-effort basis. slog discards the error returned by a handler's Handle,
// and there is no useful place to report a logging failure from inside the
// logger, so a sink write error is ignored here rather than propagated.
func (router *Router) Handle(ctx context.Context, record slog.Record) error {
	combined := router.applyState(router.combined)
	child := router.applyState(router.childFor(router.concernOf(record.Message)))

	if combined != nil {
		_ = combined.Handle(ctx, record.Clone())
	}
	_ = child.Handle(ctx, record.Clone())
	return nil
}

// WithAttrs records attrs to apply to both sinks at handle time.
func (router *Router) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *router
	clone.attrs = append(append([]slog.Attr{}, router.attrs...), attrs...)
	return &clone
}

// WithGroup records a group to apply to both sinks at handle time.
func (router *Router) WithGroup(name string) slog.Handler {
	if name == "" {
		return router
	}
	clone := *router
	clone.groups = append(append([]string{}, router.groups...), name)
	return &clone
}

func (router *Router) applyState(handler slog.Handler) slog.Handler {
	if handler == nil {
		return nil
	}
	for _, group := range router.groups {
		handler = handler.WithGroup(group)
	}
	if len(router.attrs) > 0 {
		handler = handler.WithAttrs(router.attrs)
	}
	return handler
}

func (router *Router) concernOf(message string) string {
	if index := strings.IndexByte(message, '.'); index > 0 {
		return message[:index]
	}
	return router.fallbackConcern
}

func (router *Router) childFor(concern string) slog.Handler {
	router.mu.Lock()
	defer router.mu.Unlock()
	if handler, found := router.children[concern]; found {
		return handler
	}
	path := filepath.Join(router.dir, concern+".jsonl")
	handler := FileJSON(path, router.level, router.rotation)
	router.children[concern] = handler
	return handler
}
