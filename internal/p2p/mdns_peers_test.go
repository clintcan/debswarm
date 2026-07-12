package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestGetMDNSPeers_FiltersToMDNSDiscovered verifies that fleet peer selection
// only returns peers actually discovered via mDNS. A DHT-bootstrapped node is
// connected to many unrelated public peers; returning those made every fleet
// broadcast dial strangers and every cold download wait out the claim window.
func TestGetMDNSPeers_FiltersToMDNSDiscovered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	node1, err := New(ctx, newTestConfig(t), logger)
	if err != nil {
		t.Fatalf("New node1 failed: %v", err)
	}
	defer node1.Close()

	node2, err := New(ctx, newTestConfig(t), logger)
	if err != nil {
		t.Fatalf("New node2 failed: %v", err)
	}
	defer node2.Close()

	node1Info := peer.AddrInfo{ID: node1.PeerID(), Addrs: node1.Addrs()}
	if err := node2.host.Connect(ctx, node1Info); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Connected but not discovered via mDNS: must not count as a fleet peer.
	if got := node2.GetMDNSPeers(); len(got) != 0 {
		t.Fatalf("GetMDNSPeers() returned %d peers for a non-mDNS connection, want 0", len(got))
	}

	// Once the peer is marked as mDNS-discovered (as HandlePeerFound does),
	// it becomes a fleet peer.
	node2.scorer.MarkAsMDNSPeer(node1.PeerID())
	got := node2.GetMDNSPeers()
	if len(got) != 1 || got[0].ID != node1.PeerID() {
		t.Fatalf("GetMDNSPeers() = %v, want exactly the mDNS-discovered peer %v", got, node1.PeerID())
	}
}

// TestGetMDNSPeers_PSKSwarmIncludesAllPeers verifies the private-swarm case:
// with a pre-shared key isolating the swarm and mDNS disabled, every connected
// peer is a trusted swarm member and counts as a fleet peer.
func TestGetMDNSPeers_PSKSwarmIncludesAllPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	cfg1 := newTestConfig(t)
	cfg1.PSK = psk
	node1, err := New(ctx, cfg1, logger)
	if err != nil {
		t.Fatalf("New node1 failed: %v", err)
	}
	defer node1.Close()

	cfg2 := newTestConfig(t)
	cfg2.PSK = psk
	node2, err := New(ctx, cfg2, logger)
	if err != nil {
		t.Fatalf("New node2 failed: %v", err)
	}
	defer node2.Close()

	node1Info := peer.AddrInfo{ID: node1.PeerID(), Addrs: node1.Addrs()}
	if err := node2.host.Connect(ctx, node1Info); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	got := node2.GetMDNSPeers()
	if len(got) != 1 || got[0].ID != node1.PeerID() {
		t.Fatalf("GetMDNSPeers() = %v, want the connected PSK swarm member %v", got, node1.PeerID())
	}
}
