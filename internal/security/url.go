// Package security provides security utilities for debswarm
package security

import (
	"net"
	"strings"
)

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

// IsDebianRepoURL checks if a URL looks like a legitimate Debian/Ubuntu/Mint repository
// Valid repository URLs contain /dists/, /pool/, /debian/, /ubuntu/, or /linuxmint/
func IsDebianRepoURL(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, "/dists/") ||
		strings.Contains(lower, "/pool/") ||
		strings.Contains(lower, "/debian/") ||
		strings.Contains(lower, "/ubuntu/") ||
		strings.Contains(lower, "/linuxmint/")
}

// IsAllowedMirrorURL validates that a URL is safe to fetch from
// It must not target internal services and must look like a Debian repository
func IsAllowedMirrorURL(url string) bool {
	return IsAllowedMirrorURLWithHosts(url, nil)
}

// IsAllowedMirrorURLWithHosts validates that a URL is safe to fetch from,
// allowing additional configured hosts beyond the built-in list.
// The URL must not target internal services and must look like a Debian repository.
func IsAllowedMirrorURLWithHosts(url string, allowedHosts []string) bool {
	if IsBlockedHost(url) {
		return false
	}
	// Must have Debian-style URL patterns
	if !IsDebianRepoURL(url) {
		return false
	}
	// Check if host is in the allowed list (built-in or configured)
	return isAllowedHost(url, allowedHosts)
}

// isAllowedHost checks if a URL's host is in the allowed list
func isAllowedHost(url string, additionalHosts []string) bool {
	lower := strings.ToLower(url)

	// Check built-in patterns
	for _, pattern := range knownMirrorPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	// Check additional configured hosts
	for _, host := range additionalHosts {
		hostLower := strings.ToLower(host)
		if strings.Contains(lower, hostLower) {
			return true
		}
	}

	return false
}


// knownMirrorPatterns contains hostname patterns for known Debian/Ubuntu/Mint mirrors
var knownMirrorPatterns = []string{
	"deb.debian.org",
	"debian.org",
	"archive.ubuntu.com",
	"ubuntu.com",
	"security.debian.org",
	"security.ubuntu.com",
	"packages.linuxmint.com",
	"linuxmint.com",
	"mirrors.",
	"mirror.",
	"ftp.",
}

// IsAllowedConnectTarget validates that a CONNECT target is a legitimate Debian/Ubuntu mirror
// Returns true only for known Debian/Ubuntu repository hosts on ports 443 or 80
func IsAllowedConnectTarget(hostPort string) bool {
	return IsAllowedConnectTargetWithHosts(hostPort, nil)
}

// IsAllowedConnectTargetWithHosts validates that a CONNECT target is allowed,
// checking both built-in mirrors and additional configured hosts.
// Returns true only for allowed hosts on ports 443 or 80.
func IsAllowedConnectTargetWithHosts(hostPort string, allowedHosts []string) bool {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		// If no port specified, assume it's just a host (unusual for CONNECT)
		host = hostPort
		port = "443"
	}

	// Only allow standard HTTPS/HTTP ports
	if port != "443" && port != "80" {
		return false
	}

	// Block private/internal hosts
	if isBlockedConnectHost(host) {
		return false
	}

	// Allow known mirrors or configured hosts
	return isKnownMirrorOrAllowed(host, allowedHosts)
}

// isKnownMirrorOrAllowed checks if a host is a known mirror or in the allowed list
func isKnownMirrorOrAllowed(host string, allowedHosts []string) bool {
	if isKnownDebianMirror(host) {
		return true
	}

	// Check additional configured hosts
	lower := strings.ToLower(host)
	for _, allowed := range allowedHosts {
		allowedLower := strings.ToLower(allowed)
		if lower == allowedLower || strings.HasSuffix(lower, "."+allowedLower) {
			return true
		}
	}

	return false
}

// isBlockedConnectHost checks if a host is a private/internal address
func isBlockedConnectHost(host string) bool {
	lower := strings.ToLower(host)
	for _, blocked := range blockedHostPatterns {
		if strings.Contains(lower, blocked) {
			return true
		}
	}
	return false
}

// isKnownDebianMirror checks if a host matches known Debian/Ubuntu mirror patterns
func isKnownDebianMirror(host string) bool {
	lower := strings.ToLower(host)

	for _, pattern := range knownMirrorPatterns {
		if strings.Contains(lower, pattern) || strings.HasSuffix(lower, pattern) {
			return true
		}
	}
	return false
}
