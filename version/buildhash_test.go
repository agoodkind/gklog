package version

import (
	"encoding/hex"
	"testing"
)

// TestBuildHashReturnsRuntimeDigest verifies BuildHash hashes the running test
// binary and returns a 12-character hex digest rather than the "unknown"
// fallback, so the update path always has a non-empty build identity.
func TestBuildHashReturnsRuntimeDigest(t *testing.T) {
	got := BuildHash()
	if got == "unknown" {
		t.Fatal("BuildHash returned \"unknown\" for the test binary; expected a runtime digest")
	}
	if len(got) != 12 {
		t.Fatalf("BuildHash length = %d, want 12", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("BuildHash %q is not hex: %v", got, err)
	}
}
