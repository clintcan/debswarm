// Package p2p - Connection gating for peer allowlist
package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/debswarm/debswarm/internal/security"
)

// AllowlistGater implements connmgr.ConnectionGater to restrict connections
// to a specific set of peer IDs and block specific peers
type AllowlistGater struct {
	allowlist        map[peer.ID]struct{}
	blocklist        map[peer.ID]struct{}
	mu               sync.RWMutex
	allowlistEnabled bool
}

// NewAllowlistGater creates a new allowlist-based connection gater
// If peers is empty, gating is disabled (all connections allowed)
func NewAllowlistGater(peers []peer.ID) *AllowlistGater {
	g := &AllowlistGater{
		allowlist:        make(map[peer.ID]struct{}),
		blocklist:        make(map[peer.ID]struct{}),
		allowlistEnabled: len(peers) > 0,
	}

	for _, p := range peers {
		g.allowlist[p] = struct{}{}
	}

	return g
}

// NewGater creates a connection gater with both allowlist and blocklist support
// If allowlist is empty, only blocklist filtering is applied
func NewGater(allowlist []peer.ID, blocklist []peer.ID) *AllowlistGater {
	g := &AllowlistGater{
		allowlist:        make(map[peer.ID]struct{}),
		blocklist:        make(map[peer.ID]struct{}),
		allowlistEnabled: len(allowlist) > 0,
	}

	for _, p := range allowlist {
		g.allowlist[p] = struct{}{}
	}
	for _, p := range blocklist {
		g.blocklist[p] = struct{}{}
	}

	return g
}

// Enabled returns whether allowlist gating is active
func (g *AllowlistGater) Enabled() bool {
	return g.allowlistEnabled
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

// BlockPeer adds a peer to the blocklist
func (g *AllowlistGater) BlockPeer(id peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blocklist[id] = struct{}{}
}

// UnblockPeer removes a peer from the blocklist
func (g *AllowlistGater) UnblockPeer(id peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.blocklist, id)
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

// ListBlockedPeers returns all blocked peer IDs
func (g *AllowlistGater) ListBlockedPeers() []peer.ID {
	g.mu.RLock()
	defer g.mu.RUnlock()

	peers := make([]peer.ID, 0, len(g.blocklist))
	for p := range g.blocklist {
		peers = append(peers, p)
	}
	return peers
}

// isAllowed checks if a peer is allowed (not blocked and passes allowlist if enabled)
func (g *AllowlistGater) isAllowed(p peer.ID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Check blocklist first - always deny blocked peers
	if _, blocked := g.blocklist[p]; blocked {
		return false
	}

	// If allowlist is enabled, peer must be in it
	if g.allowlistEnabled {
		_, ok := g.allowlist[p]
		return ok
	}

	return true
}

// InterceptPeerDial is called when we're about to dial a peer
func (g *AllowlistGater) InterceptPeerDial(p peer.ID) bool {
	return g.isAllowed(p)
}

// InterceptAddrDial is called when we're about to dial a specific address
func (g *AllowlistGater) InterceptAddrDial(id peer.ID, addr multiaddr.Multiaddr) bool {
	// Block dialing to private/reserved IPs (defense against eclipse attacks)
	if security.IsBlockedMultiaddr(addr) {
		return false
	}
	return g.isAllowed(id)
}

// InterceptAccept is called when we're about to accept an inbound connection
func (g *AllowlistGater) InterceptAccept(addrs network.ConnMultiaddrs) bool {
	// Block connections from private/reserved IPs early (defense-in-depth)
	// This saves resources by rejecting before security handshake
	if addrs != nil && security.IsBlockedMultiaddr(addrs.RemoteMultiaddr()) {
		return false
	}
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
