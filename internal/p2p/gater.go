// Package p2p - Connection gating for peer allowlist
package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// AllowlistGater implements connmgr.ConnectionGater to restrict connections
// to a specific set of peer IDs
type AllowlistGater struct {
	allowlist map[peer.ID]struct{}
	mu        sync.RWMutex
	enabled   bool
}

// NewAllowlistGater creates a new allowlist-based connection gater
// If peers is empty, gating is disabled (all connections allowed)
func NewAllowlistGater(peers []peer.ID) *AllowlistGater {
	g := &AllowlistGater{
		allowlist: make(map[peer.ID]struct{}),
		enabled:   len(peers) > 0,
	}

	for _, p := range peers {
		g.allowlist[p] = struct{}{}
	}

	return g
}

// Enabled returns whether allowlist gating is active
func (g *AllowlistGater) Enabled() bool {
	return g.enabled
}

// AddPeer adds a peer to the allowlist
func (g *AllowlistGater) AddPeer(id peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allowlist[id] = struct{}{}
}

// RemovePeer removes a peer from the allowlist
func (g *AllowlistGater) RemovePeer(id peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.allowlist, id)
}

// ListPeers returns all allowed peer IDs
func (g *AllowlistGater) ListPeers() []peer.ID {
	g.mu.RLock()
	defer g.mu.RUnlock()

	peers := make([]peer.ID, 0, len(g.allowlist))
	for p := range g.allowlist {
		peers = append(peers, p)
	}
	return peers
}

// isAllowed checks if a peer is in the allowlist
func (g *AllowlistGater) isAllowed(p peer.ID) bool {
	if !g.enabled {
		return true
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.allowlist[p]
	return ok
}

// InterceptPeerDial is called when we're about to dial a peer
func (g *AllowlistGater) InterceptPeerDial(p peer.ID) bool {
	return g.isAllowed(p)
}

// InterceptAddrDial is called when we're about to dial a specific address
func (g *AllowlistGater) InterceptAddrDial(id peer.ID, addr multiaddr.Multiaddr) bool {
	return g.isAllowed(id)
}

// InterceptAccept is called when we're about to accept an inbound connection
func (g *AllowlistGater) InterceptAccept(addrs network.ConnMultiaddrs) bool {
	// Can't check peer ID here, allow and check in InterceptSecured
	return true
}

// InterceptSecured is called after the security handshake completes
func (g *AllowlistGater) InterceptSecured(dir network.Direction, id peer.ID, addrs network.ConnMultiaddrs) bool {
	return g.isAllowed(id)
}

// InterceptUpgraded is called after the connection is fully upgraded
func (g *AllowlistGater) InterceptUpgraded(conn network.Conn) (bool, control.DisconnectReason) {
	if g.isAllowed(conn.RemotePeer()) {
		return true, 0
	}
	return false, control.DisconnectReason(0)
}
