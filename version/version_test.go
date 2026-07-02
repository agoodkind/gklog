package version

import (
	"testing"
)

func saveVersion() (string, string, string, string, string) {
	return Version, Commit, Dirty, BinHash, BuildTime
}

func restoreVersion(v, c, d, b, bt string) {
	Version, Commit, Dirty, BinHash, BuildTime = v, c, d, b, bt
}

func TestStringCleanCommitWithBuildTime(t *testing.T) {
	ov, oc, od, ob, obt := saveVersion()
	defer restoreVersion(ov, oc, od, ob, obt)
	Version = "unknown"
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
	ov, oc, od, ob, obt := saveVersion()
	defer restoreVersion(ov, oc, od, ob, obt)
	Version = "unknown"
	Commit = "abc"
	Dirty = "true"
	BinHash = "unknown"
	BuildTime = "2020-01-01T00:00:00Z"
	want := "abc+dirty built 2020-01-01T00:00:00Z"
	if got := String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStringLongCommitTrimmed(t *testing.T) {
	ov, oc, od, ob, obt := saveVersion()
	defer restoreVersion(ov, oc, od, ob, obt)
	Version = "unknown"
	long := "0123456789abcdef0123456789abcdef"
	Commit = long
	Dirty = "false"
	BinHash = "unknown"
	BuildTime = "2020-01-01T00:00:00Z"
	got := String()
	want := "0123456789ab built 2020-01-01T00:00:00Z"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStringIncludesVersionWhenStamped(t *testing.T) {
	ov, oc, od, ob, obt := saveVersion()
	defer restoreVersion(ov, oc, od, ob, obt)
	Version = "202607020744-85-94da4c4"
	Commit = "94da4c4000000000"
	Dirty = "false"
	BinHash = "unknown"
	BuildTime = "2020-01-01T00:00:00Z"
	want := "202607020744-85-94da4c4 94da4c400000 built 2020-01-01T00:00:00Z"
	if got := String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
