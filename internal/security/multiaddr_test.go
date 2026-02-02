package security

import (
	"testing"

	"github.com/multiformats/go-multiaddr"
)

func mustMultiaddr(t *testing.T, s string) multiaddr.Multiaddr {
	t.Helper()
	ma, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("failed to create multiaddr %q: %v", s, err)
	}
	return ma
}

func TestIsBlockedMultiaddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		blocked bool
	}{
		// Loopback - should be blocked
		{"IPv4 loopback", "/ip4/127.0.0.1/tcp/4001", true},
		{"IPv4 loopback other", "/ip4/127.0.0.2/tcp/4001", true},
		{"IPv6 loopback", "/ip6/::1/tcp/4001", true},

		// Unspecified - should be blocked
		{"IPv4 unspecified", "/ip4/0.0.0.0/tcp/4001", true},

		// Private RFC 1918 - should be blocked
		{"10.x.x.x", "/ip4/10.0.0.1/tcp/4001", true},
		{"10.x.x.x other", "/ip4/10.255.255.255/tcp/4001", true},
		{"172.16.x.x", "/ip4/172.16.0.1/tcp/4001", true},
		{"172.31.x.x", "/ip4/172.31.255.255/tcp/4001", true},
		{"192.168.x.x", "/ip4/192.168.1.1/tcp/4001", true},
		{"192.168.x.x other", "/ip4/192.168.0.100/tcp/4001", true},

		// Link-local - should be blocked
		{"IPv4 link-local", "/ip4/169.254.1.1/tcp/4001", true},
		{"IPv4 link-local other", "/ip4/169.254.169.254/tcp/4001", true},
		{"IPv6 link-local", "/ip6/fe80::1/tcp/4001", true},

		// IPv6 unique local - should be blocked
		{"IPv6 ULA fd00", "/ip6/fd00::1/tcp/4001", true},
		{"IPv6 ULA fdxx", "/ip6/fd12:3456::1/tcp/4001", true},

		// Public IPs - should be allowed
		{"Public IPv4 8.8.8.8", "/ip4/8.8.8.8/tcp/4001", false},
		{"Public IPv4 1.1.1.1", "/ip4/1.1.1.1/tcp/4001", false},
		{"Public IPv4 random", "/ip4/203.0.113.1/tcp/4001", false},
		{"Public IPv6", "/ip6/2001:db8::1/tcp/4001", false},
		{"Public IPv6 Google", "/ip6/2607:f8b0:4004:800::200e/tcp/4001", false},

		// Edge cases for 172.x.x.x range
		{"172.15.x.x allowed", "/ip4/172.15.0.1/tcp/4001", false},
		{"172.32.x.x allowed", "/ip4/172.32.0.1/tcp/4001", false},

		// QUIC addresses
		{"Private QUIC", "/ip4/192.168.1.1/udp/4001/quic-v1", true},
		{"Public QUIC", "/ip4/8.8.8.8/udp/4001/quic-v1", false},

		// DNS-based addresses - should be allowed (resolved later)
		{"DNS4", "/dns4/example.com/tcp/4001", false},
		{"DNS6", "/dns6/example.com/tcp/4001", false},
		{"DNSaddr", "/dnsaddr/bootstrap.libp2p.io", false},

		// P2P-only address - should be allowed (no IP component)
		{"P2P only", "/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ma := mustMultiaddr(t, tc.addr)
			result := IsBlockedMultiaddr(ma)
			if result != tc.blocked {
				t.Errorf("IsBlockedMultiaddr(%q) = %v, want %v", tc.addr, result, tc.blocked)
			}
		})
	}
}

func TestIsBlockedMultiaddr_Nil(t *testing.T) {
	if IsBlockedMultiaddr(nil) {
		t.Error("IsBlockedMultiaddr(nil) should return false")
	}
}

func TestFilterBlockedAddrs(t *testing.T) {
	addrs := []multiaddr.Multiaddr{
		mustMultiaddr(t, "/ip4/8.8.8.8/tcp/4001"),      // allowed
		mustMultiaddr(t, "/ip4/192.168.1.1/tcp/4001"),  // blocked
		mustMultiaddr(t, "/ip4/1.2.3.4/tcp/4001"),      // allowed
		mustMultiaddr(t, "/ip4/10.0.0.1/tcp/4001"),     // blocked
		mustMultiaddr(t, "/dns4/example.com/tcp/4001"), // allowed (DNS)
		mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001"),    // blocked
		mustMultiaddr(t, "/ip6/2001:db8::1/tcp/4001"),  // allowed
		mustMultiaddr(t, "/ip6/fd00::1/tcp/4001"),      // blocked
	}

	filtered := FilterBlockedAddrs(addrs)

	// Should have 4 allowed addresses
	if len(filtered) != 4 {
		t.Errorf("FilterBlockedAddrs returned %d addresses, want 4", len(filtered))
	}

	// Verify the filtered addresses are the expected ones
	expected := map[string]bool{
		"/ip4/8.8.8.8/tcp/4001":      true,
		"/ip4/1.2.3.4/tcp/4001":      true,
		"/dns4/example.com/tcp/4001": true,
		"/ip6/2001:db8::1/tcp/4001":  true,
	}

	for _, addr := range filtered {
		if !expected[addr.String()] {
			t.Errorf("unexpected address in filtered result: %s", addr.String())
		}
	}
}

func TestFilterBlockedAddrs_Empty(t *testing.T) {
	result := FilterBlockedAddrs(nil)
	if result != nil {
		t.Errorf("FilterBlockedAddrs(nil) = %v, want nil", result)
	}

	result = FilterBlockedAddrs([]multiaddr.Multiaddr{})
	if len(result) != 0 {
		t.Errorf("FilterBlockedAddrs([]) = %v, want empty slice", result)
	}
}

func TestFilterBlockedAddrs_AllBlocked(t *testing.T) {
	addrs := []multiaddr.Multiaddr{
		mustMultiaddr(t, "/ip4/192.168.1.1/tcp/4001"),
		mustMultiaddr(t, "/ip4/10.0.0.1/tcp/4001"),
		mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001"),
	}

	filtered := FilterBlockedAddrs(addrs)
	if len(filtered) != 0 {
		t.Errorf("FilterBlockedAddrs with all blocked returned %d addresses, want 0", len(filtered))
	}
}

func TestFilterBlockedAddrs_AllAllowed(t *testing.T) {
	addrs := []multiaddr.Multiaddr{
		mustMultiaddr(t, "/ip4/8.8.8.8/tcp/4001"),
		mustMultiaddr(t, "/ip4/1.1.1.1/tcp/4001"),
		mustMultiaddr(t, "/dns4/example.com/tcp/4001"),
	}

	filtered := FilterBlockedAddrs(addrs)
	if len(filtered) != 3 {
		t.Errorf("FilterBlockedAddrs with all allowed returned %d addresses, want 3", len(filtered))
	}
}

func TestExtractIPFromMultiaddr(t *testing.T) {
	tests := []struct {
		addr     string
		expected string
	}{
		{"/ip4/192.168.1.1/tcp/4001", "192.168.1.1"},
		{"/ip4/10.0.0.1/udp/4001/quic-v1", "10.0.0.1"},
		{"/ip6/fe80::1/tcp/4001", "fe80::1"},
		{"/ip6/2001:db8::1/tcp/4001", "2001:db8::1"},
		{"/dns4/example.com/tcp/4001", ""},
		{"/dnsaddr/bootstrap.libp2p.io", ""},
		{"/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN", ""},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			ma := mustMultiaddr(t, tc.addr)
			result := extractIPFromMultiaddr(ma)
			if result != tc.expected {
				t.Errorf("extractIPFromMultiaddr(%q) = %q, want %q", tc.addr, result, tc.expected)
			}
		})
	}
}

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		// Blocked
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"0.0.0.0", true},
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"169.254.1.1", true},
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},

		// Allowed
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"2001:db8::1", false},
	}

	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			result := isBlockedIP(tc.ip)
			if result != tc.blocked {
				t.Errorf("isBlockedIP(%q) = %v, want %v", tc.ip, result, tc.blocked)
			}
		})
	}
}

func TestIsBlockedIPString(t *testing.T) {
	// Test the string-based fallback function directly
	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		// IPv4 blocked patterns
		{"loopback 127.x", "127.0.0.1", true},
		{"loopback 127.x other", "127.255.255.255", true},
		{"unspecified", "0.0.0.0", true},
		{"private 10.x", "10.0.0.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"private 172.31.x", "172.31.255.255", true},
		{"private 192.168.x", "192.168.1.1", true},
		{"link-local", "169.254.1.1", true},

		// IPv6 blocked patterns
		{"ipv6 loopback", "::1", true},
		{"ipv6 unique local fd", "fd00::1", true},
		{"ipv6 link-local", "fe80::1", true},

		// Allowed
		{"public ipv4", "8.8.8.8", false},
		{"public ipv4 2", "1.1.1.1", false},
		{"172.15 allowed", "172.15.0.1", false},
		{"172.32 allowed", "172.32.0.1", false},
		{"public ipv6", "2001:db8::1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isBlockedIPString(tc.ip)
			if result != tc.blocked {
				t.Errorf("isBlockedIPString(%q) = %v, want %v", tc.ip, result, tc.blocked)
			}
		})
	}
}
