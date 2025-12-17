// Package security provides security utilities for debswarm
package security

import (
	"net"
	"strings"

	"github.com/multiformats/go-multiaddr"
)

// IsBlockedMultiaddr checks if a multiaddr contains a blocked/private IP address.
// This prevents connections to internal services and protects against eclipse attacks
// where attackers announce private IPs in DHT provider records.
func IsBlockedMultiaddr(ma multiaddr.Multiaddr) bool {
	if ma == nil {
		return false
	}

	ip := extractIPFromMultiaddr(ma)
	if ip == "" {
		// No IP component (e.g., DNS-based multiaddr) - allow it
		// The IP will be validated after DNS resolution
		return false
	}

	return isBlockedIP(ip)
}

// FilterBlockedAddrs removes multiaddrs containing blocked IPs from a slice.
// Returns a new slice with only allowed addresses.
func FilterBlockedAddrs(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	if len(addrs) == 0 {
		return addrs
	}

	filtered := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		if !IsBlockedMultiaddr(addr) {
			filtered = append(filtered, addr)
		}
	}
	return filtered
}

// extractIPFromMultiaddr extracts the IP address string from a multiaddr.
// Returns empty string if no IP component is found.
func extractIPFromMultiaddr(ma multiaddr.Multiaddr) string {
	var ip string

	multiaddr.ForEach(ma, func(c multiaddr.Component) bool {
		switch c.Protocol().Code {
		case multiaddr.P_IP4, multiaddr.P_IP6:
			ip = c.Value()
			return false // Stop iteration
		}
		return true // Continue
	})

	return ip
}

// isBlockedIP checks if an IP address string is in a blocked range.
// Blocked ranges include:
// - Loopback (127.0.0.0/8, ::1)
// - Private networks (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
// - Link-local (169.254.0.0/16, fe80::/10)
// - IPv6 unique local (fd00::/8)
// - Unspecified (0.0.0.0, ::)
func isBlockedIP(ipStr string) bool {
	// Parse the IP
	ip := net.ParseIP(ipStr)
	if ip == nil {
		// Can't parse - use string matching as fallback
		return isBlockedIPString(ipStr)
	}

	// Check using net package functions
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() {
		return true
	}

	// Additional checks for IPv6 unique local addresses (fd00::/8)
	// net.IP.IsPrivate() covers fc00::/7 which includes fd00::/8
	// but let's be explicit
	if ip.To4() == nil && len(ip) == net.IPv6len {
		// IPv6 - check for fd00::/8
		if ip[0] == 0xfd {
			return true
		}
	}

	return false
}

// isBlockedIPString is a fallback using string matching for unparseable IPs.
// Uses the same patterns as blockedHostPatterns in url.go.
func isBlockedIPString(ipStr string) bool {
	lower := strings.ToLower(ipStr)

	blockedPatterns := []string{
		"127.",    // IPv4 loopback
		"0.0.0.0", // Unspecified
		"10.",     // Private (RFC 1918)
		"172.16.", // Private (RFC 1918)
		"172.17.", "172.18.", "172.19.",
		"172.20.", "172.21.", "172.22.", "172.23.",
		"172.24.", "172.25.", "172.26.", "172.27.",
		"172.28.", "172.29.", "172.30.", "172.31.",
		"192.168.", // Private (RFC 1918)
		"169.254.", // Link-local
		"::1",      // IPv6 loopback
		"::",       // IPv6 unspecified (alone)
		"fd",       // IPv6 unique local (fd00::/8)
		"fe80:",    // IPv6 link-local
	}

	for _, pattern := range blockedPatterns {
		if strings.HasPrefix(lower, pattern) {
			return true
		}
	}

	return false
}
