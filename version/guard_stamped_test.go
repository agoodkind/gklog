//go:build gklog_stamped

package version

import "testing"

func TestBuildMetadataValidAcceptsRequiredValues(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "abcdef1234567890"
	Dirty = "false"
	BinHash = ""
	BuildTime = "2020-01-01T00:00:00Z"
	if !buildMetadataValid() {
		t.Fatal("expected metadata to be valid")
	}
}

func TestBuildMetadataValidRejectsMissingCommit(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "unknown"
	Dirty = "false"
	BinHash = ""
	BuildTime = "2020-01-01T00:00:00Z"
	if buildMetadataValid() {
		t.Fatal("expected metadata to be invalid")
	}
}

func TestBuildMetadataValidRejectsInvalidDirty(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "abcdef1234567890"
	Dirty = "unknown"
	BinHash = ""
	BuildTime = "2020-01-01T00:00:00Z"
	if buildMetadataValid() {
		t.Fatal("expected metadata to be invalid")
	}
}

func TestBuildMetadataValidRejectsMissingBuildTime(t *testing.T) {
	oc, od, ob, obt := saveVersion()
	defer restoreVersion(oc, od, ob, obt)
	Commit = "abcdef1234567890"
	Dirty = "false"
	BinHash = ""
	BuildTime = " "
	if buildMetadataValid() {
		t.Fatal("expected metadata to be invalid")
	}
}
