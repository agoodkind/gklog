package version

var (
	Commit    = "unknown"
	Dirty     = "unknown"
	BinHash   = "unknown"
	BuildTime = "unknown"
)

// String returns a human-readable build identifier suitable for log attrs.
// Format: "<short-commit>[+dirty] built <BuildTime>"
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
