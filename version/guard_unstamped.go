//go:build !gklog_stamped

package version

// When the gklog_stamped build tag is absent, build metadata defaults
// (Commit="unknown", Dirty="unknown", BinHash="unknown",
// BuildTime="unknown") flow through unchanged. Production binaries
// must build with -tags=gklog_stamped and stamp the four package
// vars via -ldflags; the stamped variant in guard_stamped.go panics
// at init when stamping is incomplete. Unstamped builds (library
// tests, IDE indexing, downstream consumers compiling against gklog)
// stay buildable so the unstamped state is observable through
// [String] rather than a compile failure.
