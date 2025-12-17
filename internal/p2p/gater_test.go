package p2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func TestNewAllowlistGater_Empty(t *testing.T) {
	gater := NewAllowlistGater(nil)

	if gater.Enabled() {
		t.Error("Gater should be disabled when no peers provided")
	}

	// Empty allowlist should allow all peers
	randomPeer := peer.ID("12D3KooWRandomPeerID")
	if !gater.InterceptPeerDial(randomPeer) {
		t.Error("Disabled gater should allow all peer dials")
	}
}

func TestNewAllowlistGater_WithPeers(t *testing.T) {
	allowedPeer := peer.ID("12D3KooWAllowedPeer")
	blockedPeer := peer.ID("12D3KooWBlockedPeer")

	gater := NewAllowlistGater([]peer.ID{allowedPeer})

	if !gater.Enabled() {
		t.Error("Gater should be enabled when peers provided")
	}

	// Allowed peer should pass
	if !gater.InterceptPeerDial(allowedPeer) {
		t.Error("Allowed peer should be permitted to dial")
	}

	// Blocked peer should be rejected
	if gater.InterceptPeerDial(blockedPeer) {
		t.Error("Non-allowlisted peer should be blocked")
	}
}

func TestAllowlistGater_AddPeer(t *testing.T) {
	gater := NewAllowlistGater(nil)

	newPeer := peer.ID("12D3KooWNewPeer")
	gater.AddPeer(newPeer)

	peers := gater.ListPeers()
	found := false
	for _, p := range peers {
		if p == newPeer {
			found = true
			break
		}
	}

	if !found {
		t.Error("AddPeer did not add peer to allowlist")
	}
}

func TestAllowlistGater_RemovePeer(t *testing.T) {
	peerToRemove := peer.ID("12D3KooWPeerToRemove")
	gater := NewAllowlistGater([]peer.ID{peerToRemove})

	// Verify peer is initially allowed
	if !gater.InterceptPeerDial(peerToRemove) {
		t.Error("Peer should initially be allowed")
	}

	gater.RemovePeer(peerToRemove)

	// Verify peer is no longer in list
	peers := gater.ListPeers()
	for _, p := range peers {
		if p == peerToRemove {
			t.Error("RemovePeer did not remove peer from allowlist")
		}
	}
}

func TestAllowlistGater_ListPeers(t *testing.T) {
	peer1 := peer.ID("12D3KooWPeer1")
	peer2 := peer.ID("12D3KooWPeer2")
	peer3 := peer.ID("12D3KooWPeer3")

	gater := NewAllowlistGater([]peer.ID{peer1, peer2, peer3})

	peers := gater.ListPeers()
	if len(peers) != 3 {
		t.Errorf("Expected 3 peers, got %d", len(peers))
	}

	// All peers should be in the list
	peerMap := make(map[peer.ID]bool)
	for _, p := range peers {
		peerMap[p] = true
	}

	if !peerMap[peer1] || !peerMap[peer2] || !peerMap[peer3] {
		t.Error("ListPeers missing expected peers")
	}
}

func TestAllowlistGater_InterceptAddrDial(t *testing.T) {
	allowedPeer := peer.ID("12D3KooWAllowedPeer")
	blockedPeer := peer.ID("12D3KooWBlockedPeer")

	gater := NewAllowlistGater([]peer.ID{allowedPeer})

	// InterceptAddrDial should behave the same as InterceptPeerDial
	if !gater.InterceptAddrDial(allowedPeer, nil) {
		t.Error("InterceptAddrDial should allow allowlisted peer")
	}

	if gater.InterceptAddrDial(blockedPeer, nil) {
		t.Error("InterceptAddrDial should block non-allowlisted peer")
	}
}

func TestAllowlistGater_InterceptAccept(t *testing.T) {
	gater := NewAllowlistGater([]peer.ID{peer.ID("12D3KooWSomePeer")})

	// InterceptAccept with nil should return true (no address to check)
	if !gater.InterceptAccept(nil) {
		t.Error("InterceptAccept with nil should return true")
	}

	// InterceptAccept with public IP should return true
	publicAddr := mustMultiaddr(t, "/ip4/8.8.8.8/tcp/4001")
	if !gater.InterceptAccept(&mockConnMultiaddrs{remote: publicAddr}) {
		t.Error("InterceptAccept should allow public IP")
	}
}

func TestAllowlistGater_InterceptSecured(t *testing.T) {
	allowedPeer := peer.ID("12D3KooWAllowedPeer")
	blockedPeer := peer.ID("12D3KooWBlockedPeer")

	gater := NewAllowlistGater([]peer.ID{allowedPeer})

	// InterceptSecured should check the peer ID
	if !gater.InterceptSecured(network.DirInbound, allowedPeer, nil) {
		t.Error("InterceptSecured should allow allowlisted peer")
	}

	if gater.InterceptSecured(network.DirInbound, blockedPeer, nil) {
		t.Error("InterceptSecured should block non-allowlisted peer")
	}

	// Should work for both directions
	if !gater.InterceptSecured(network.DirOutbound, allowedPeer, nil) {
		t.Error("InterceptSecured should allow outbound to allowlisted peer")
	}
}

// mockConn implements network.Conn for testing
type mockConn struct {
	network.Conn
	remotePeer peer.ID
}

func (m *mockConn) RemotePeer() peer.ID {
	return m.remotePeer
}

func TestAllowlistGater_InterceptUpgraded(t *testing.T) {
	allowedPeer := peer.ID("12D3KooWAllowedPeer")
	blockedPeer := peer.ID("12D3KooWBlockedPeer")

	gater := NewAllowlistGater([]peer.ID{allowedPeer})

	// Test with allowed peer
	allowedConn := &mockConn{remotePeer: allowedPeer}
	allow, reason := gater.InterceptUpgraded(allowedConn)
	if !allow {
		t.Error("InterceptUpgraded should allow connection from allowlisted peer")
	}
	if reason != 0 {
		t.Error("InterceptUpgraded should return 0 reason for allowed peer")
	}

	// Test with blocked peer
	blockedConn := &mockConn{remotePeer: blockedPeer}
	allow, _ = gater.InterceptUpgraded(blockedConn)
	if allow {
		t.Error("InterceptUpgraded should block connection from non-allowlisted peer")
	}
}

func TestAllowlistGater_DisabledAllowsAll(t *testing.T) {
	gater := NewAllowlistGater(nil) // Disabled

	randomPeers := []peer.ID{
		peer.ID("12D3KooWPeer1"),
		peer.ID("12D3KooWPeer2"),
		peer.ID("12D3KooWPeer3"),
	}

	for _, p := range randomPeers {
		if !gater.InterceptPeerDial(p) {
			t.Errorf("Disabled gater should allow peer %s", p)
		}
		if !gater.InterceptAddrDial(p, nil) {
			t.Errorf("Disabled gater should allow addr dial for peer %s", p)
		}
		if !gater.InterceptSecured(network.DirInbound, p, nil) {
			t.Errorf("Disabled gater should allow secured for peer %s", p)
		}

		conn := &mockConn{remotePeer: p}
		allow, _ := gater.InterceptUpgraded(conn)
		if !allow {
			t.Errorf("Disabled gater should allow upgraded for peer %s", p)
		}
	}
}

func TestAllowlistGater_ConcurrentAccess(t *testing.T) {
	gater := NewAllowlistGater(nil)

	done := make(chan bool)

	// Concurrent adds
	go func() {
		for i := 0; i < 100; i++ {
			gater.AddPeer(peer.ID("peer" + string(rune(i))))
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			_ = gater.ListPeers()
			_ = gater.InterceptPeerDial(peer.ID("testpeer"))
		}
		done <- true
	}()

	// Concurrent removes
	go func() {
		for i := 0; i < 100; i++ {
			gater.RemovePeer(peer.ID("peer" + string(rune(i))))
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

// mockConnMultiaddrs implements network.ConnMultiaddrs for testing
type mockConnMultiaddrs struct {
	local, remote multiaddr.Multiaddr
}

func (m *mockConnMultiaddrs) LocalMultiaddr() multiaddr.Multiaddr {
	if m == nil {
		return nil
	}
	return m.local
}

func (m *mockConnMultiaddrs) RemoteMultiaddr() multiaddr.Multiaddr {
	if m == nil {
		return nil
	}
	return m.remote
}

func mustMultiaddr(t *testing.T, s string) multiaddr.Multiaddr {
	t.Helper()
	ma, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("failed to create multiaddr %q: %v", s, err)
	}
	return ma
}

func TestAllowlistGater_InterceptAccept_BlocksPrivateIPs(t *testing.T) {
	gater := NewAllowlistGater(nil) // Gating disabled but IP filtering active

	blockedAddrs := []string{
		"/ip4/192.168.1.1/tcp/4001",
		"/ip4/10.0.0.1/tcp/4001",
		"/ip4/127.0.0.1/tcp/4001",
		"/ip4/172.16.0.1/tcp/4001",
		"/ip4/169.254.1.1/tcp/4001",
		"/ip6/::1/tcp/4001",
		"/ip6/fe80::1/tcp/4001",
		"/ip6/fd00::1/tcp/4001",
	}

	for _, addr := range blockedAddrs {
		ma := mustMultiaddr(t, addr)
		mockAddrs := &mockConnMultiaddrs{remote: ma}
		if gater.InterceptAccept(mockAddrs) {
			t.Errorf("InterceptAccept should block %s", addr)
		}
	}
}

func TestAllowlistGater_InterceptAccept_AllowsPublicIPs(t *testing.T) {
	gater := NewAllowlistGater(nil)

	allowedAddrs := []string{
		"/ip4/8.8.8.8/tcp/4001",
		"/ip4/1.1.1.1/tcp/4001",
		"/ip4/203.0.113.1/tcp/4001",
		"/ip6/2001:db8::1/tcp/4001",
		"/dns4/example.com/tcp/4001",
	}

	for _, addr := range allowedAddrs {
		ma := mustMultiaddr(t, addr)
		mockAddrs := &mockConnMultiaddrs{remote: ma}
		if !gater.InterceptAccept(mockAddrs) {
			t.Errorf("InterceptAccept should allow %s", addr)
		}
	}
}

func TestAllowlistGater_InterceptAddrDial_BlocksPrivateIPs(t *testing.T) {
	gater := NewAllowlistGater(nil) // Gating disabled

	blockedAddrs := []string{
		"/ip4/192.168.1.1/tcp/4001",
		"/ip4/10.0.0.1/tcp/4001",
		"/ip4/127.0.0.1/tcp/4001",
		"/ip4/172.16.0.1/tcp/4001",
	}

	peerID := peer.ID("12D3KooWSomePeer")
	for _, addr := range blockedAddrs {
		ma := mustMultiaddr(t, addr)
		if gater.InterceptAddrDial(peerID, ma) {
			t.Errorf("InterceptAddrDial should block %s", addr)
		}
	}
}

func TestAllowlistGater_InterceptAddrDial_AllowsPublicIPs(t *testing.T) {
	gater := NewAllowlistGater(nil) // Gating disabled

	allowedAddrs := []string{
		"/ip4/8.8.8.8/tcp/4001",
		"/ip4/1.1.1.1/tcp/4001",
		"/dns4/example.com/tcp/4001",
	}

	peerID := peer.ID("12D3KooWSomePeer")
	for _, addr := range allowedAddrs {
		ma := mustMultiaddr(t, addr)
		if !gater.InterceptAddrDial(peerID, ma) {
			t.Errorf("InterceptAddrDial should allow %s", addr)
		}
	}
}

func TestAllowlistGater_InterceptAddrDial_CombinesIPAndPeerCheck(t *testing.T) {
	allowedPeer := peer.ID("12D3KooWAllowedPeer")
	blockedPeer := peer.ID("12D3KooWBlockedPeer")

	gater := NewAllowlistGater([]peer.ID{allowedPeer})

	publicAddr := mustMultiaddr(t, "/ip4/8.8.8.8/tcp/4001")
	privateAddr := mustMultiaddr(t, "/ip4/192.168.1.1/tcp/4001")

	// Allowed peer with public IP - should pass
	if !gater.InterceptAddrDial(allowedPeer, publicAddr) {
		t.Error("Should allow: allowed peer + public IP")
	}

	// Allowed peer with private IP - should fail (IP check)
	if gater.InterceptAddrDial(allowedPeer, privateAddr) {
		t.Error("Should block: allowed peer + private IP")
	}

	// Blocked peer with public IP - should fail (peer check)
	if gater.InterceptAddrDial(blockedPeer, publicAddr) {
		t.Error("Should block: blocked peer + public IP")
	}

	// Blocked peer with private IP - should fail (both checks)
	if gater.InterceptAddrDial(blockedPeer, privateAddr) {
		t.Error("Should block: blocked peer + private IP")
	}
}
