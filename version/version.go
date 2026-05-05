// Package version exposes build-time identification variables that are
// stamped at link time by every binary that imports gklog. The four
// exported vars (Commit, Dirty, BinHash, BuildTime) are part of the
// public stamping ABI; do not rename or remove them.
package version

var (
	// Commit is the git commit SHA stamped at build time via
	// -ldflags "-X goodkind.io/gklog/version.Commit=...".
	// Defaults to "unknown" when not stamped.
	Commit = "unknown"
	// Dirty is "true" when the working tree had uncommitted changes
	// at build time, "false" when clean, and "unknown" otherwise.
	Dirty = "unknown"
	// BinHash is a content hash of the built binary. Defaults to
	// "unknown" when not stamped.
	BinHash = "unknown"
	// BuildTime is the RFC3339 timestamp at which the binary was
	// built. Defaults to "unknown" when not stamped.
	BuildTime = "unknown"
)

// String returns a human-readable build identifier suitable for log
// attrs. Format: "<short-commit>[+dirty] built <BuildTime>".
func String() string {
	commit := Commit
	if commit != "unknown" && len(commit) > 12 {
		commit = commit[:12]
	}
	out := commit
	if Dirty == "true" {
		out += "+dirty"
	}
	if BuildTime != "unknown" {
		out += " built " + BuildTime
	}
	return out
}
