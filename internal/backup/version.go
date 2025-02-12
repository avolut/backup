package backup

import (
	"regexp"
	"strings"
)

// extractMajorVersion extracts the major version number from PostgreSQL version string
func extractMajorVersion(version string) string {
	// Handle pg_dump version string (e.g., "pg_dump (PostgreSQL) 14.2")
	if strings.Contains(version, "pg_dump") {
		re := regexp.MustCompile(`PostgreSQL[)\s]+([0-9]+)`)
		matches := re.FindStringSubmatch(version)
		if len(matches) > 1 {
			return matches[1]
		}
	}

	// Handle database version string (e.g., "PostgreSQL 14.2 on x86_64-apple-darwin...")
	re := regexp.MustCompile(`PostgreSQL\s+([0-9]+)`)
	matches := re.FindStringSubmatch(version)
	if len(matches) > 1 {
		return matches[1]
	}

	return ""
}
