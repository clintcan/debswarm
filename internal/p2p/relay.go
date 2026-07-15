package p2p

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/metrics"
)

// NamespaceRelay is the DHT namespace under which nodes running a circuit-relay
// service advertise themselves, so NAT'd peers can discover a relay to reserve on.
const NamespaceRelay = "/debswarm/relay/1.0.0"

// relayAdvertiseInterval is how often an active relay re-announces itself.
const relayAdvertiseInterval = 1 * time.Hour

// Relay service modes (mirrors config.RelayService*).
const (
	RelayServiceAuto = "auto"
	RelayServiceOn   = "on"
	RelayServiceOff  = "off"
)

// Reachability override modes (mirrors config.Reachability*). Kept as local
// constants so the p2p package need not import config.
const (
	ReachabilityPublic  = "public"
	ReachabilityPrivate = "private"
)

// relayServiceMode normalizes the configured relay-service mode, defaulting to
// "auto" for empty or unrecognized values (Validate rejects bad values before we
// get here; this is belt-and-braces so a bad value degrades to the safe default
// rather than silently meaning "off").
func relayServiceMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case RelayServiceOn:
		return RelayServiceOn
	case RelayServiceOff:
		return RelayServiceOff
	default:
		return RelayServiceAuto
	}
}

// relayResourcesFrom builds the circuit-relay v2 resource bounds from config.
// Zero values leave circuit-relay v2's own defaults in place, which are already
// sized for hole-punch coordination rather than bulk transfer.
func relayResourcesFrom(cfg *Config) relay.Resources {
	res := relay.DefaultResources()

	if cfg.RelayMaxReservations > 0 {
		res.MaxReservations = cfg.RelayMaxReservations
	}
	if cfg.RelayMaxCircuits > 0 {
		res.MaxCircuits = cfg.RelayMaxCircuits
	}
	if cfg.RelayBufferSize > 0 || cfg.RelayDuration > 0 {
		limit := *res.Limit // copy; never mutate the shared default
		if cfg.RelayBufferSize > 0 {
			limit.Data = cfg.RelayBufferSize
		}
		if cfg.RelayDuration > 0 {
			limit.Duration = cfg.RelayDuration
		}
		res.Limit = &limit
	}

	return res
}

// isCircuitAddr reports whether a multiaddr is a circuit-relay address, i.e. one
// handed to us by a relay we hold a reservation with.
func isCircuitAddr(addr multiaddr.Multiaddr) bool {
	_, err := addr.ValueForProtocol(multiaddr.P_CIRCUIT)
	return err == nil
}

// ParseRelayPeers converts configured relay multiaddrs into AddrInfos. Entries
// must carry a /p2p/<peer-id> component — an address without one cannot be
// reserved on. Malformed entries are logged and skipped rather than failing
// startup, so one bad line in a config does not take a node offline.
func ParseRelayPeers(addrs []string, logger *zap.Logger) []peer.AddrInfo {
	infos := make([]peer.AddrInfo, 0, len(addrs))
	for _, a := range addrs {
		if a == "" {
			continue
		}
		info, err := peer.AddrInfoFromString(a)
		if err != nil {
			logger.Warn("Invalid relay peer address, skipping",
				zap.String("addr", a), zap.Error(err))
			continue
		}
		infos = append(infos, *info)
	}
	return infos
}

// relaySource supplies relay candidates to AutoRelay.
//
// AutoRelay is a libp2p *host option*, so the peer source has to exist before the
// host — and therefore before the DHT it wants to query. This type bridges that
// gap: it is handed to libp2p at construction and attached to the finished node
// afterwards. Calls that arrive before attach() (AutoRelay starts looking for
// candidates as soon as the host is up) return no candidates rather than racing
// on a half-built Node.
type relaySource struct {
	mu   sync.RWMutex
	node *Node
}

func (rs *relaySource) attach(n *Node) {
	rs.mu.Lock()
	rs.node = n
	rs.mu.Unlock()
}

// FindCandidates satisfies autorelay.PeerSource. It streams peers that have
// advertised themselves as relays in the DHT. AutoRelay handles reservation,
// renewal, and failover from there.
func (rs *relaySource) FindCandidates(ctx context.Context, num int) <-chan peer.AddrInfo {
	out := make(chan peer.AddrInfo)

	go func() {
		defer close(out)

		rs.mu.RLock()
		n := rs.node
		rs.mu.RUnlock()

		// Not attached yet (AutoRelay can ask before New() returns), or no DHT to
		// query. Returning an empty, closed channel tells AutoRelay "no candidates
		// right now"; it will ask again.
		if n == nil || n.routingDiscovery == nil {
			return
		}

		// A private (PSK) swarm has no public DHT to discover relays through — its
		// relays must be configured explicitly via relay_peers. Querying here would
		// leak nothing (the DHT is unreachable) but would waste lookups.
		if n.privateSwarm {
			return
		}

		peerChan, err := n.routingDiscovery.FindPeers(ctx, NamespaceRelay)
		if err != nil {
			n.logger.Debug("Relay discovery failed", zap.Error(err))
			return
		}

		sent := 0
		for p := range peerChan {
			if p.ID == n.host.ID() || len(p.Addrs) == 0 {
				continue // never relay through ourselves
			}
			select {
			case out <- p:
				sent++
				if sent >= num {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}

// startRelayService begins relaying for other peers, bounded by the configured
// limits. Safe to call when already running (no-op).
func (n *Node) startRelayService() {
	n.relayMu.Lock()
	defer n.relayMu.Unlock()

	if n.relayService != nil {
		return
	}

	r, err := relay.New(n.host,
		relay.WithResources(n.relayResources),
		relay.WithMetricsTracer(&relayMetricsTracer{metrics: n.metrics}),
	)
	if err != nil {
		n.logger.Warn("Failed to start relay service", zap.Error(err))
		return
	}
	n.relayService = r

	n.logger.Info("Relay service started (helping NAT'd peers connect)",
		zap.Int("maxReservations", n.relayResources.MaxReservations),
		zap.Int("maxCircuits", n.relayResources.MaxCircuits))

	if n.metrics != nil {
		n.metrics.RelayServiceActive.Set(1)
	}

	// Advertise ourselves so NAT'd peers can find us to reserve on.
	go n.advertiseRelay()
}

// stopRelayService stops relaying for other peers. Safe to call when not running.
func (n *Node) stopRelayService() {
	n.relayMu.Lock()
	defer n.relayMu.Unlock()

	if n.relayService == nil {
		return
	}

	if err := n.relayService.Close(); err != nil {
		n.logger.Warn("Failed to close relay service", zap.Error(err))
	}
	n.relayService = nil

	n.logger.Info("Relay service stopped")

	if n.metrics != nil {
		n.metrics.RelayServiceActive.Set(0)
		n.metrics.RelayCircuitsActive.Set(0)
	}
}

// relayServiceRunning reports whether we are currently relaying for other peers.
func (n *Node) relayServiceRunning() bool {
	n.relayMu.Lock()
	defer n.relayMu.Unlock()
	return n.relayService != nil
}

// advertiseRelay periodically announces this node in the DHT relay namespace for
// as long as the relay service is running.
func (n *Node) advertiseRelay() {
	if n.privateSwarm || n.routingDiscovery == nil {
		return // no public DHT to advertise into
	}

	ticker := time.NewTicker(relayAdvertiseInterval)
	defer ticker.Stop()

	for {
		if !n.relayServiceRunning() {
			return
		}

		if _, err := n.routingDiscovery.Advertise(n.ctx, NamespaceRelay); err != nil {
			n.logger.Debug("Failed to advertise relay service", zap.Error(err))
		} else {
			n.logger.Debug("Advertised relay service to DHT")
		}

		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// watchReachability drives the relay service from AutoNAT's verdict about whether
// we are publicly reachable, and records that verdict as a metric.
//
// This is what makes relay_service="auto" work: a node only relays for others
// once AutoNAT confirms other peers can actually reach it. A NAT'd node running a
// relay service would be useless (nobody can reach it to be relayed through) and
// would waste the user's uplink.
func (n *Node) watchReachability() {
	sub, err := n.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		n.logger.Warn("Failed to subscribe to reachability events", zap.Error(err))
		return
	}
	defer func() {
		if closeErr := sub.Close(); closeErr != nil {
			n.logger.Debug("Failed to close reachability subscription", zap.Error(closeErr))
		}
	}()

	for {
		select {
		case <-n.ctx.Done():
			return
		case e, ok := <-sub.Out():
			if !ok {
				return
			}
			evt, isReachability := e.(event.EvtLocalReachabilityChanged)
			if !isReachability {
				continue
			}

			n.recordReachability(evt.Reachability)

			if n.relayServiceMode != RelayServiceAuto {
				continue // "on" started at construction; "off" never runs
			}

			switch evt.Reachability {
			case network.ReachabilityPublic:
				n.logger.Info("AutoNAT reports we are publicly reachable — offering relay service to the swarm")
				n.startRelayService()
			case network.ReachabilityPrivate:
				n.logger.Info("AutoNAT reports we are behind NAT — not relaying for others")
				n.stopRelayService()
			case network.ReachabilityUnknown:
				// Hold whatever state we're in rather than flapping.
			}
		}
	}
}

// recordReachability exports AutoNAT's verdict as a gauge, one series per state.
func (n *Node) recordReachability(r network.Reachability) {
	if n.metrics == nil {
		return
	}
	states := map[string]network.Reachability{
		"public":  network.ReachabilityPublic,
		"private": network.ReachabilityPrivate,
		"unknown": network.ReachabilityUnknown,
	}
	for label, state := range states {
		if state == r {
			n.metrics.Reachability.WithLabel(label).Set(1)
		} else {
			n.metrics.Reachability.WithLabel(label).Set(0)
		}
	}
}

// watchRelayReservations tracks whether we actually hold a relay reservation.
//
// A reservation is not directly observable, but it has an unmistakable symptom:
// the relay hands us a /p2p-circuit address, which shows up in our own advertised
// address set. Counting those circuit addresses therefore measures the exact step
// that was silently missing before AutoRelay existed — no reservation, no circuit
// address, nothing for a peer to dial, and hole punching could never fire.
func (n *Node) watchRelayReservations() {
	sub, err := n.host.EventBus().Subscribe(new(event.EvtLocalAddressesUpdated))
	if err != nil {
		n.logger.Warn("Failed to subscribe to address events", zap.Error(err))
		return
	}
	defer func() {
		if closeErr := sub.Close(); closeErr != nil {
			n.logger.Debug("Failed to close address subscription", zap.Error(closeErr))
		}
	}()

	var lastCount int

	for {
		select {
		case <-n.ctx.Done():
			return
		case _, ok := <-sub.Out():
			if !ok {
				return
			}

			count := 0
			for _, addr := range n.host.Addrs() {
				if isCircuitAddr(addr) {
					count++
				}
			}

			if n.metrics != nil {
				n.metrics.RelayReservations.WithLabel("active").Set(float64(count))
			}

			if count > lastCount {
				gained := count - lastCount
				if n.metrics != nil {
					n.metrics.RelayReservationsOK.Add(int64(gained))
				}
				n.logger.Info("Obtained relay reservation — NAT'd peers can now reach us",
					zap.Int("circuitAddrs", count))
			} else if count < lastCount && count == 0 {
				n.logger.Warn("Lost all relay reservations — NAT'd peers can no longer reach us")
			}
			lastCount = count
		}
	}
}

// trackConnectionTypes records how many of our current connections are direct
// versus relayed.
//
// This is the metric that tells the truth about hole punching. A connection that
// stays "relayed" means DCUtR failed and we are stuck on a circuit — which,
// under circuit-v2's tiny limits, cannot carry a package. A healthy cross-NAT
// transfer shows a relayed connection converting to a direct one.
func (n *Node) trackConnectionTypes() {
	n.host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			n.adjustConnectionGauge(c, 1)
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			n.adjustConnectionGauge(c, -1)
		},
	})
}

func (n *Node) adjustConnectionGauge(c network.Conn, delta float64) {
	if n.metrics == nil {
		return
	}
	// Stats().Limited is libp2p's own flag for "formed over a circuit v2 relay".
	label := "direct"
	if c.Stat().Limited {
		label = "relayed"
	}
	n.metrics.ConnectionsByType.WithLabel(label).Add(delta)
}

// relayedTransferSkipped reports whether a transfer must be skipped because the
// only path to the peer is a relay and relayed transfers are disabled (cap <= 0).
// When true the caller falls back to the mirror; a direct path (relayed=false) is
// never skipped, so enabling the feature never changes direct-transfer behavior.
func relayedTransferSkipped(relayed bool, maxBytes int64) bool {
	return relayed && maxBytes <= 0
}

// relayedSizeExceeded reports whether a relayed transfer of size bytes exceeds the
// configured cap. Only meaningful when relayed is true — a direct transfer is
// never bounded by this cap. The boundary is inclusive: size == maxBytes is
// allowed, size == maxBytes+1 is refused.
func relayedSizeExceeded(relayed bool, size, maxBytes int64) bool {
	return relayed && size > maxBytes
}

// onlyRelayedConn reports whether every current connection to the peer is a
// circuit-relay (Limited) connection — i.e. there is no direct path to it. Returns
// false when any direct connection exists, and false when there are no connections
// at all (the caller handles that as a connect failure). This is what gates the
// relayed-transfer fallback: bytes are only carried over a relay when a direct
// (hole-punched or publicly-reachable) path is genuinely unavailable.
func (n *Node) onlyRelayedConn(id peer.ID) bool {
	conns := n.host.Network().ConnsToPeer(id)
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		if !c.Stat().Limited {
			return false
		}
	}
	return true
}

// holePunchTracer records DCUtR outcomes.
type holePunchTracer struct {
	metrics *metrics.Metrics
	logger  *zap.Logger
}

func (t *holePunchTracer) Trace(evt *holepunch.Event) {
	end, ok := evt.Evt.(*holepunch.EndHolePunchEvt)
	if !ok {
		return
	}

	result := "failure"
	if end.Success {
		result = "success"
	}

	if t.metrics != nil {
		t.metrics.HolePunchTotal.WithLabel(result).Inc()
	}

	if end.Success {
		t.logger.Info("Hole punch succeeded — upgraded to a direct connection",
			zap.String("peer", evt.Remote.String()),
			zap.Duration("elapsed", end.EllapsedTime))
	} else {
		t.logger.Debug("Hole punch failed (falling back to mirror if no other peer has it)",
			zap.String("peer", evt.Remote.String()),
			zap.String("error", end.Error))
	}
}

// relayMetricsTracer implements relay.MetricsTracer so we can report what running
// a relay is actually costing this node.
type relayMetricsTracer struct {
	metrics *metrics.Metrics
}

func (t *relayMetricsTracer) RelayStatus(enabled bool) {
	if t.metrics == nil {
		return
	}
	if enabled {
		t.metrics.RelayServiceActive.Set(1)
	} else {
		t.metrics.RelayServiceActive.Set(0)
	}
}

func (t *relayMetricsTracer) ConnectionOpened() {
	if t.metrics != nil {
		t.metrics.RelayCircuitsActive.Inc()
	}
}

func (t *relayMetricsTracer) ConnectionClosed(_ time.Duration) {
	if t.metrics != nil {
		t.metrics.RelayCircuitsActive.Dec()
	}
}

func (t *relayMetricsTracer) ConnectionRequestHandled(_ pbv2.Status)  {}
func (t *relayMetricsTracer) ReservationAllowed(_ bool)               {}
func (t *relayMetricsTracer) ReservationClosed(_ int)                 {}
func (t *relayMetricsTracer) ReservationRequestHandled(_ pbv2.Status) {}
func (t *relayMetricsTracer) BytesTransferred(_ int)                  {}
