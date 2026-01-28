// Package cache provides content-addressed storage for .deb packages
package cache

import (
	"path/filepath"
	"strings"
)

// ParseDebFilename extracts package name, version, and architecture from a Debian package filename.
// The expected format is: {name}_{version}_{arch}.deb
// Returns name, version, arch, and a boolean indicating if parsing was successful.
//
// Examples:
//   - "curl_7.88.1-10+deb12u5_amd64.deb" -> ("curl", "7.88.1-10+deb12u5", "amd64", true)
//   - "libssl3_3.0.11-1~deb12u2_amd64.deb" -> ("libssl3", "3.0.11-1~deb12u2", "amd64", true)
//   - "pool/main/c/curl/curl_7.88.1-10_amd64.deb" -> ("curl", "7.88.1-10", "amd64", true)
func ParseDebFilename(filename string) (name, version, arch string, ok bool) {
	// Extract basename if full path is provided
	base := filepath.Base(filename)

	// Remove .deb extension
	if !strings.HasSuffix(strings.ToLower(base), ".deb") {
		return "", "", "", false
	}
	base = base[:len(base)-4]

	// Split by underscores
	// Format: {name}_{version}_{arch}
	// Note: package names may contain underscores (rare but possible)
	// so we split from the right
	parts := strings.Split(base, "_")
	if len(parts) < 3 {
		return "", "", "", false
	}

	// Architecture is always the last part
	arch = parts[len(parts)-1]

	// Version is the second-to-last part
	version = parts[len(parts)-2]

	// Name is everything before version (in case name contains underscores)
	name = strings.Join(parts[:len(parts)-2], "_")

	// Basic validation
	if name == "" || version == "" || arch == "" {
		return "", "", "", false
	}

	return name, version, arch, true
}

// ParseDebFilenameFromPath is a convenience function that handles full paths
// and returns empty strings if parsing fails.
func ParseDebFilenameFromPath(path string) (name, version, arch string) {
	name, version, arch, _ = ParseDebFilename(path)
	return
}
