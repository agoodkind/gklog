package version

import (
	"testing"
)

func saveVersion() (string, string, string, string) {
	return Commit, Dirty, BinHash, BuildTime
}

func restoreVersion(c, d, b, bt string) {
	Commit, Dirty, BinHash, BuildTime = c, d, b, bt
}

func TestStringAllUnknown(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "unknown"
	Dirty = "unknown"
	BinHash = "unknown"
	BuildTime = "unknown"
	if got := String(); got != "unknown" {
		t.Fatalf("got %q want %q", got, "unknown")
	}
}

func TestStringCleanCommitWithBuildTime(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "abcdef1234567890"
	Dirty = "false"
	BinHash = "unknown"
	BuildTime = "2020-01-01T00:00:00Z"
	want := "abcdef123456 built 2020-01-01T00:00:00Z"
	if got := String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStringDirty(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "abc"
	Dirty = "true"
	BinHash = "unknown"
	BuildTime = "unknown"
	want := "abc+dirty"
	if got := String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStringLongCommitTrimmed(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	long := "0123456789abcdef0123456789abcdef"
	Commit = long
	Dirty = "unknown"
	BinHash = "unknown"
	BuildTime = "unknown"
	got := String()
	want := "0123456789ab"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
