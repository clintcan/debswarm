// Package security provides security utilities for debswarm
package security

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// blockedHostnamePatterns contains non-IP hostname patterns that should never be accessed.
// IP-based blocking is handled separately by isBlockedIP using net.IP methods,
// which correctly handles hex/octal/decimal encoded addresses.
var blockedHostnamePatterns = []string{
	"localhost", // Loopback hostname
	"metadata.", // Cloud metadata endpoints (metadata.google.internal, etc.)
}

// IsBlockedHost checks if a URL targets a blocked/internal host.
// This prevents SSRF attacks against internal services.
// It parses the URL to extract the host (fixing false positives on URL paths),
// then checks IP addresses including hex/octal/decimal encodings.
func IsBlockedHost(rawURL string) bool {
	host := extractHost(rawURL)
	if host == "" {
		return false
	}
	return isBlockedHostOrIP(host)
}

// IsBlockedIP checks if an IP address is in a blocked range
// (loopback, private, link-local, unspecified).
// Exported for use by other packages (e.g., post-DNS-resolution checks in CONNECT handler).
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// isBlockedHostOrIP checks if a host string (no port) is a blocked target.
func isBlockedHostOrIP(host string) bool {
	lower := strings.ToLower(host)

	// Try to parse as IP (handles standard and alternate encodings like hex/octal/decimal)
	if ip := parseIPPermissive(lower); ip != nil {
		return IsBlockedIP(ip)
	}

	// Hostname-based checks
	for _, pattern := range blockedHostnamePatterns {
		if strings.HasSuffix(pattern, ".") {
			// Prefix pattern (e.g., "metadata.") — matches hosts starting with this
			if strings.HasPrefix(lower, pattern) {
				return true
			}
		} else {
			// Exact or subdomain pattern (e.g., "localhost" matches "localhost" and "sub.localhost")
			if lower == pattern || strings.HasSuffix(lower, "."+pattern) {
				return true
			}
		}
	}

	return false
}

// parseIPPermissive parses an IP address string, including alternate encodings
// that standard net.ParseIP doesn't handle:
//   - Hex: 0x7f000001 → 127.0.0.1
//   - Octal: 0177.0.0.01 → 127.0.0.1
//   - Decimal integer: 2130706433 → 127.0.0.1
//   - Mixed dotted: 0x7f.0.0.1 → 127.0.0.1
func parseIPPermissive(host string) net.IP {
	// Standard parse first (handles dotted decimal IPv4 and IPv6)
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}

	// Handle bracket-wrapped IPv6 (e.g., [::1])
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if ip := net.ParseIP(inner); ip != nil {
			return ip
		}
	}

	// Try non-dotted integer formats (hex, octal, decimal)
	// These are accepted by some HTTP clients via system resolvers (getaddrinfo)
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		var num uint64
		var err error
		switch {
		case strings.HasPrefix(host, "0x") || strings.HasPrefix(host, "0X"):
			if len(host) > 2 {
				num, err = strconv.ParseUint(host[2:], 16, 32)
			} else {
				return nil
			}
		case strings.HasPrefix(host, "0") && len(host) > 1:
			num, err = strconv.ParseUint(host[1:], 8, 32)
		default:
			num, err = strconv.ParseUint(host, 10, 32)
		}

		if err == nil && num <= 0xFFFFFFFF {
			return net.IPv4(byte(num>>24), byte(num>>16), byte(num>>8), byte(num))
		}
	}

	// Try dotted format with mixed octal/hex octets (e.g., 0177.0.0.01)
	if strings.Contains(host, ".") && !strings.Contains(host, ":") {
		parts := strings.Split(host, ".")
		if len(parts) == 4 {
			octets := make([]byte, 4)
			allValid := true
			for i, part := range parts {
				val, ok := parseOctet(part)
				if !ok {
					allValid = false
					break
				}
				octets[i] = val
			}
			if allValid {
				return net.IPv4(octets[0], octets[1], octets[2], octets[3])
			}
		}
	}

	return nil
}

// parseOctet parses a single IP octet that may be in decimal, hex, or octal notation.
func parseOctet(s string) (byte, bool) {
	if s == "" {
		return 0, false
	}
	var val uint64
	var err error
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		if len(s) > 2 {
			val, err = strconv.ParseUint(s[2:], 16, 8)
		} else {
			return 0, false
		}
	case strings.HasPrefix(s, "0") && len(s) > 1:
		val, err = strconv.ParseUint(s[1:], 8, 8)
	default:
		val, err = strconv.ParseUint(s, 10, 8)
	}
	if err != nil {
		return 0, false
	}
	return byte(val), true
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
func isAllowedHost(rawURL string, additionalHosts []string) bool {
	host := extractHost(rawURL)
	if host == "" {
		return false
	}

	// Check built-in mirror patterns
	if isKnownDebianMirror(host) {
		return true
	}

	// Check additional configured hosts (exact or subdomain match)
	for _, allowed := range additionalHosts {
		allowedLower := strings.ToLower(allowed)
		if matchesHost(host, allowedLower) {
			return true
		}
	}

	return false
}

// knownMirrorDomains contains domain names for known Debian/Ubuntu/Mint mirrors.
// Matching uses exact match or subdomain suffix (e.g., "foo.debian.org" matches "debian.org").
var knownMirrorDomains = []string{
	"deb.debian.org",
	"debian.org",
	"archive.ubuntu.com",
	"ubuntu.com",
	"security.debian.org",
	"security.ubuntu.com",
	"packages.linuxmint.com",
	"linuxmint.com",
}

// Note: Mirror hostname prefixes (mirrors.*, mirror.*, ftp.*) are NOT automatically
// trusted. Attackers could register domains like "mirrors.evil.com". Third-party
// mirrors must be explicitly added via the allowed_hosts configuration.

// extractHost parses the host from a URL string, stripping any port.
func extractHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname() // strips port and brackets
	return strings.ToLower(host)
}

// matchesHost checks if host exactly matches domain or is a subdomain of it.
// e.g., matchesHost("cdn.debian.org", "debian.org") = true
//
//	matchesHost("attack-debian.org", "debian.org") = false
func matchesHost(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
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

// isBlockedConnectHost checks if a host is a private/internal address.
// Uses the same IP-aware and hostname-aware checks as IsBlockedHost.
func isBlockedConnectHost(host string) bool {
	return isBlockedHostOrIP(host)
}

// isKnownDebianMirror checks if a host matches known Debian/Ubuntu mirror patterns.
// Uses exact or subdomain matching for domains, and prefix matching for naming conventions.
func isKnownDebianMirror(host string) bool {
	lower := strings.ToLower(host)

	for _, domain := range knownMirrorDomains {
		if matchesHost(lower, domain) {
			return true
		}
	}

	return false
}
