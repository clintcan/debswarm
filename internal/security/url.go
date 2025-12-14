// Package security provides security utilities for debswarm
package security

import "strings"

// blockedHostPatterns contains patterns that should never be accessed
// to prevent SSRF attacks against internal services
var blockedHostPatterns = []string{
	"localhost",
	"127.0.0.1",
	"[::1]",
	"0.0.0.0",
	"169.254.",  // AWS/cloud metadata service (link-local)
	"metadata.", // Cloud metadata endpoints
	"10.",       // Private network (RFC 1918)
	"172.16.", "172.17.", "172.18.", "172.19.",
	"172.20.", "172.21.", "172.22.", "172.23.",
	"172.24.", "172.25.", "172.26.", "172.27.",
	"172.28.", "172.29.", "172.30.", "172.31.", // Private network (RFC 1918)
	"192.168.", // Private network (RFC 1918)
	"fd00:",    // IPv6 unique local addresses
	"fe80:",    // IPv6 link-local
}

// IsBlockedHost checks if a URL contains a blocked host pattern
// This prevents SSRF attacks against internal services
func IsBlockedHost(url string) bool {
	lower := strings.ToLower(url)
	for _, blocked := range blockedHostPatterns {
		if strings.Contains(lower, blocked) {
			return true
		}
	}
	return false
}

// IsDebianRepoURL checks if a URL looks like a legitimate Debian/Ubuntu repository
// Valid repository URLs contain /dists/, /pool/, /debian/, or /ubuntu/
func IsDebianRepoURL(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, "/dists/") ||
		strings.Contains(lower, "/pool/") ||
		strings.Contains(lower, "/debian/") ||
		strings.Contains(lower, "/ubuntu/")
}

// IsAllowedMirrorURL validates that a URL is safe to fetch from
// It must not target internal services and must look like a Debian repository
func IsAllowedMirrorURL(url string) bool {
	if IsBlockedHost(url) {
		return false
	}
	return IsDebianRepoURL(url)
}
