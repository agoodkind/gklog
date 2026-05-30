package gklog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Router is a [slog.Handler] that writes every record to a combined handler
// and to a per-concern JSONL file. By default the concern is the first
// dot-separated segment of the message (so "semantic.reindex" lands in
// semantic.jsonl and "mcp.tool.started" in mcp.jsonl), with a fallback concern
// for messages that carry no dot. Concern files are created lazily under dir on
// first use and share one [RotationConfig].
//
// The default message-segment behavior is preserved for callers that pass a
// zero [RouterOptions]. Callers that tag records with an explicit concern
// attribute (and want nested per-concern paths or per-concern levels) set
// [RouterOptions.ConcernAttr], [RouterOptions.PathForConcern], and
// [RouterOptions.LevelForConcern].
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

	concernAttr     string
	pathForConcern  func(concern string) string
	levelForConcern func(concern string) slog.Level

	mu       *sync.Mutex
	children map[string]slog.Handler

	attrs  []slog.Attr
	groups []string
}

// RouterOptions configures a [Router]. Zero values pick safe defaults:
// FallbackConcern defaults to "default", Rotation uses [RotationConfig]'s own
// zero-value defaults (matching [FileJSON]), the concern is read from the
// message's first dot-segment, every concern's file is "<concern>.jsonl", and
// every concern logs at the router level.
type RouterOptions struct {
	// FallbackConcern names the concern used when no concern can be derived
	// from a record. Empty defaults to "default".
	FallbackConcern string
	// Rotation controls every concern file's rotation.
	Rotation RotationConfig
	// ConcernAttr, when non-empty, makes the router select the concern from
	// this record attribute (searching attributes added via WithAttrs and the
	// record's own attributes) instead of the first dot-segment of the
	// message. Records that lack the attribute fall back to FallbackConcern.
	ConcernAttr string
	// PathForConcern maps a concern to its file path relative to dir. A nil
	// func defaults to "<concern>.jsonl". Returning an empty string routes the
	// record to the combined sink only, with no per-concern file (so callers
	// can disable a concern's file without dropping it from the combined log).
	PathForConcern func(concern string) string
	// LevelForConcern returns the minimum level for a concern's file. A nil
	// func uses the router level for every concern.
	LevelForConcern func(concern string) slog.Level
}

// NewRouter returns a Router that writes concern files under dir at level or
// above and mirrors every record to combined. combined must not be nil; pass
// [slog.DiscardHandler] when no combined sink is desired.
func NewRouter(dir string, level slog.Level, combined slog.Handler, opts RouterOptions) *Router {
	fallback := opts.FallbackConcern
	if fallback == "" {
		fallback = "default"
	}
	pathForConcern := opts.PathForConcern
	if pathForConcern == nil {
		pathForConcern = func(concern string) string { return concern + ".jsonl" }
	}
	levelForConcern := opts.LevelForConcern
	if levelForConcern == nil {
		levelForConcern = func(string) slog.Level { return level }
	}
	return &Router{
		dir:             dir,
		level:           level,
		rotation:        opts.Rotation,
		fallbackConcern: fallback,
		combined:        combined,
		concernAttr:     strings.TrimSpace(opts.ConcernAttr),
		pathForConcern:  pathForConcern,
		levelForConcern: levelForConcern,
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
	if combined := router.applyState(router.combined); combined != nil && combined.Enabled(ctx, record.Level) {
		_ = combined.Handle(ctx, record.Clone())
	}
	if child := router.applyState(router.childFor(router.concernOf(record))); child != nil && child.Enabled(ctx, record.Level) {
		_ = child.Handle(ctx, record.Clone())
	}
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

// concernOf selects the concern for a record. When ConcernAttr is set it reads
// that attribute (from the WithAttrs-accumulated attrs and the record's own
// attrs), falling back to FallbackConcern when absent; otherwise it uses the
// message's first dot-segment.
func (router *Router) concernOf(record slog.Record) string {
	if router.concernAttr != "" {
		if value, ok := router.attrConcern(record); ok && value != "" {
			return value
		}
		return router.fallbackConcern
	}
	if index := strings.IndexByte(record.Message, '.'); index > 0 {
		return record.Message[:index]
	}
	return router.fallbackConcern
}

func (router *Router) attrConcern(record slog.Record) (string, bool) {
	for _, attr := range router.attrs {
		if attr.Key == router.concernAttr {
			return attr.Value.Resolve().String(), true
		}
	}
	found := ""
	ok := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == router.concernAttr {
			found = attr.Value.Resolve().String()
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

// childFor returns the cached per-concern handler, creating it lazily. A
// PathForConcern that returns an empty string yields a nil handler so the
// record reaches only the combined sink.
func (router *Router) childFor(concern string) slog.Handler {
	router.mu.Lock()
	defer router.mu.Unlock()
	if handler, found := router.children[concern]; found {
		return handler
	}
	rel := router.pathForConcern(concern)
	if rel == "" {
		router.children[concern] = nil
		return nil
	}
	path := filepath.Join(router.dir, rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	handler := FileJSON(path, router.levelForConcern(concern), router.rotation)
	router.children[concern] = handler
	return handler
}
