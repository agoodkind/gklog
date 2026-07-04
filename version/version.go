// Package version exposes build-time identification variables that are
// stamped at link time by every binary that imports gklog. The five
// exported vars (Version, Commit, Dirty, BinHash, BuildTime) are part of
// the public stamping ABI; do not rename or remove them.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var (
	// Version is the release tag stamped at build time via
	// -ldflags "-X goodkind.io/gklog/version.Version=...".
	// Defaults to "unknown" when not stamped. Release tooling stamps
	// this with the published release tag so consumers can compare the
	// running build against the latest release.
	Version = "unknown"
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

var (
	buildHashOnce  sync.Once
	buildHashValue string
)

// BuildHash returns the SHA-256 of the running binary, truncated to 12 hex
// characters and computed once per process. It is a best-effort runtime
// identity: it returns "unknown" when the executable cannot be read. Unlike
// BinHash, a build-time stamp left empty for locally built binaries, BuildHash
// is derived at runtime, so it is populated for both local and release builds.
// Update tooling uses it as the non-empty build identity that its Config
// validation requires.
func BuildHash() string {
	buildHashOnce.Do(func() {
		buildHashValue = computeBuildHash()
	})
	return buildHashValue
}

func computeBuildHash() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

// String returns a human-readable build identifier suitable for log
// attrs. Format: "[<Version> ]<short-commit>[+dirty] built <BuildTime>".
// Version is included as a leading token when it has been stamped.
func String() string {
	commit := Commit
	if commit != "unknown" && len(commit) > 12 {
		commit = commit[:12]
	}
	out := commit
	if Version != "unknown" && Version != "" {
		out = Version + " " + out
	}
	if Dirty == "true" {
		out += "+dirty"
	}
	if BuildTime != "unknown" {
		out += " built " + BuildTime
	}
	return out
}
