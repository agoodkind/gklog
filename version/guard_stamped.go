//go:build gklog_stamped

package version

import "strings"

const invalidBuildMetadataMessage = "required gklog ENV vars are empty or invalid"

func init() {
	if buildMetadataValid() {
		return
	}

	panic(invalidBuildMetadataMessage)
}

func buildMetadataValid() bool {
	if missingBuildMetadataValue(Commit) {
		return false
	}

	if missingBuildMetadataValue(BuildTime) {
		return false
	}

	if Dirty != "true" && Dirty != "false" {
		return false
	}

	return true
}

func missingBuildMetadataValue(value string) bool {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return true
	}

	return strings.EqualFold(trimmedValue, "unknown")
}
