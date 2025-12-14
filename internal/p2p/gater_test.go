package p2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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

	// InterceptAccept always returns true (can't determine peer ID yet)
	if !gater.InterceptAccept(nil) {
		t.Error("InterceptAccept should always return true")
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
