// Package p2p provides peer-to-peer networking using libp2p
package p2p

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/audit"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/ratelimit"
	"github.com/debswarm/debswarm/internal/sanitize"
	"github.com/debswarm/debswarm/internal/security"
	"github.com/debswarm/debswarm/internal/timeouts"
)

const (
	// ProtocolTransfer is the protocol ID for file transfers
	ProtocolTransfer = "/debswarm/transfer/1.0.0"

	// ProtocolTransferRange is the protocol ID for range-based transfers
	ProtocolTransferRange = "/debswarm/transfer-range/1.0.0"

	// NamespacePackage is the DHT namespace for package providers
	NamespacePackage = "/debswarm/pkg/"

	// MaxTransferSize is the maximum file size for transfer (500MB)
	MaxTransferSize = 500 * 1024 * 1024

	// Connection limits
	MaxConcurrentUploads = 20
	MaxUploadsPerPeer    = 4
)

// Node represents a P2P node
type Node struct {
	host             host.Host
	dht              *dht.IpfsDHT
	routingDiscovery *drouting.RoutingDiscovery // Cached for reuse
	pingService      *ping.PingService          // Keepalive ping service
	logger           *zap.Logger
	ctx              context.Context
	cancel           context.CancelFunc
	getContent       ContentGetter
	scorer           *peers.Scorer
	timeouts         *timeouts.Manager
	metrics          *metrics.Metrics
	audit            audit.Logger
	mdnsService      mdns.Service
	bootstrapDone    chan struct{}

	// Rate limiting (global)
	uploadLimiter   *ratelimit.Limiter
	downloadLimiter *ratelimit.Limiter

	// Per-peer rate limiting (optional, nil if disabled)
	peerUploadLimiter   *ratelimit.PeerLimiterManager
	peerDownloadLimiter *ratelimit.PeerLimiterManager

	// Upload tracking
	uploadsMu            sync.Mutex
	activeUploads        int
	uploadsPerPeer       map[peer.ID]int
	maxConcurrentUploads int

	// Private swarm mode (when peer allowlist is active)
	// Skips DHT announcements to prevent information leakage
	privateSwarm bool
}

// ContentGetter is a function that retrieves content by hash
type ContentGetter func(sha256Hash string) (io.ReadCloser, int64, error)

// Config holds P2P node configuration
type Config struct {
	ListenPort           int
	BootstrapPeers       []string
	EnableMDNS           bool
	PrivateKey           crypto.PrivKey
	DataDir              string   // Directory for persistent data (identity key, etc.)
	PreferQUIC           bool     // Prefer QUIC over TCP
	MaxUploadRate        int64    // bytes per second, 0 = unlimited
	MaxDownloadRate      int64    // bytes per second, 0 = unlimited
	MaxConnections       int      // Maximum number of connections (0 = default 100)
	MaxConcurrentUploads int      // Maximum concurrent uploads (0 = default 20)
	PSK                  []byte   // Pre-shared key for private swarm
	PeerAllowlist        []string // Allowed peer IDs (empty = all allowed)
	PeerBlocklist        []string // Blocked peer IDs
	Scorer               *peers.Scorer
	Timeouts             *timeouts.Manager
	Metrics              *metrics.Metrics
	Audit                audit.Logger // Audit logger for structured event logging

	// NAT traversal configuration
	EnableRelay        bool // Use circuit relays to reach NAT'd peers (default: true)
	EnableHolePunching bool // Enable NAT hole punching (default: true)

	// Per-peer rate limiting configuration
	PerPeerUploadRate   int64   // bytes per second, 0 = auto-calculate from global/expected
	PerPeerDownloadRate int64   // bytes per second, 0 = auto-calculate from global/expected
	ExpectedPeers       int     // Expected concurrent peers for auto-calculation
	AdaptiveEnabled     bool    // Enable adaptive rate adjustment based on peer scores
	AdaptiveMinRate     int64   // Minimum rate floor for adaptive (bytes/sec)
	AdaptiveMaxBoost    float64 // Maximum boost factor for high-performing peers
}

// New creates a new P2P node with QUIC preference
func New(ctx context.Context, cfg *Config, logger *zap.Logger) (*Node, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Load or generate identity key
	var privKey crypto.PrivKey
	var err error

	if cfg.PrivateKey != nil {
		// Use explicitly provided key
		privKey = cfg.PrivateKey
	} else if cfg.DataDir != "" {
		// Load from persistent storage or create new
		privKey, err = LoadOrCreateIdentity(cfg.DataDir)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to load/create identity: %w", err)
		}
		logger.Info("Loaded persistent identity",
			zap.String("peerID", IdentityFingerprint(privKey)),
			zap.String("dataDir", cfg.DataDir))
	} else {
		// Generate ephemeral key (not persisted)
		privKey, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}
		logger.Debug("Generated ephemeral identity (not persisted)")
	}

	// Create listen addresses - QUIC first for preference
	var listenAddrs []multiaddr.Multiaddr

	if cfg.PreferQUIC {
		// QUIC addresses first (preferred)
		quicAddrs := []string{
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.ListenPort),
			fmt.Sprintf("/ip6/::/udp/%d/quic-v1", cfg.ListenPort),
		}
		for _, addr := range quicAddrs {
			ma, maErr := multiaddr.NewMultiaddr(addr)
			if maErr == nil {
				listenAddrs = append(listenAddrs, ma)
			}
		}
	}

	// TCP addresses (fallback or primary if QUIC disabled)
	tcpAddrs := []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.ListenPort),
		fmt.Sprintf("/ip6/::/tcp/%d", cfg.ListenPort),
	}
	for _, addr := range tcpAddrs {
		ma, maErr := multiaddr.NewMultiaddr(addr)
		if maErr == nil {
			listenAddrs = append(listenAddrs, ma)
		}
	}

	if !cfg.PreferQUIC {
		// Add QUIC as fallback
		quicAddrs := []string{
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.ListenPort),
			fmt.Sprintf("/ip6/::/udp/%d/quic-v1", cfg.ListenPort),
		}
		for _, addr := range quicAddrs {
			ma, maErr := multiaddr.NewMultiaddr(addr)
			if maErr == nil {
				listenAddrs = append(listenAddrs, ma)
			}
		}
	}

	// Set up connection manager with limits
	maxConns := cfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 100 // default
	}
	lowWater := maxConns * 80 / 100 // Start pruning at 80% capacity
	highWater := maxConns

	connMgr, err := connmgr.NewConnManager(
		lowWater,
		highWater,
		connmgr.WithGracePeriod(10*time.Minute),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create connection manager: %w", err)
	}

	logger.Info("Connection limits configured",
		zap.Int("maxConnections", maxConns),
		zap.Int("lowWater", lowWater),
		zap.Int("highWater", highWater))

	// Build libp2p options with QUIC preference
	opts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.ConnectionManager(connMgr),

		// NAT traversal (always enabled)
		libp2p.EnableNATService(),
		libp2p.NATPortMap(),
	}

	// Optional: Circuit relay for reaching NAT'd peers
	if cfg.EnableRelay {
		opts = append(opts, libp2p.EnableRelay())
		logger.Info("Circuit relay enabled (can reach NAT'd peers via relays)")
	}

	// Optional: NAT hole punching
	if cfg.EnableHolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
		logger.Debug("NAT hole punching enabled")
	}

	// Add PSK for private swarm if configured
	if len(cfg.PSK) > 0 {
		opts = append(opts, libp2p.PrivateNetwork(pnet.PSK(cfg.PSK)))
		logger.Info("Private swarm enabled",
			zap.String("fingerprint", PSKFingerprint(cfg.PSK)))
	}

	// Add peer allowlist/blocklist if configured
	// Also track if we're in private swarm mode to skip DHT announcements
	var privateSwarmMode bool
	if len(cfg.PeerAllowlist) > 0 || len(cfg.PeerBlocklist) > 0 {
		// Parse allowlist
		allowedPeerIDs := make([]peer.ID, 0, len(cfg.PeerAllowlist))
		for _, pidStr := range cfg.PeerAllowlist {
			pid, decodeErr := peer.Decode(pidStr)
			if decodeErr != nil {
				logger.Warn("Invalid peer ID in allowlist", zap.String("peer", pidStr), zap.Error(decodeErr))
				continue
			}
			allowedPeerIDs = append(allowedPeerIDs, pid)
		}

		// Parse blocklist
		blockedPeerIDs := make([]peer.ID, 0, len(cfg.PeerBlocklist))
		for _, pidStr := range cfg.PeerBlocklist {
			pid, decodeErr := peer.Decode(pidStr)
			if decodeErr != nil {
				logger.Warn("Invalid peer ID in blocklist", zap.String("peer", pidStr), zap.Error(decodeErr))
				continue
			}
			blockedPeerIDs = append(blockedPeerIDs, pid)
		}

		if len(allowedPeerIDs) > 0 || len(blockedPeerIDs) > 0 {
			gater := NewGater(allowedPeerIDs, blockedPeerIDs)
			opts = append(opts, libp2p.ConnectionGater(gater))
			if len(allowedPeerIDs) > 0 {
				privateSwarmMode = true // Enable private swarm mode to skip DHT announcements
				logger.Info("Peer allowlist enabled", zap.Int("count", len(allowedPeerIDs)))
			}
			if len(blockedPeerIDs) > 0 {
				logger.Info("Peer blocklist enabled", zap.Int("count", len(blockedPeerIDs)))
			}
		}
	}

	// Create libp2p host
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	logger.Info("Created P2P host",
		zap.String("peerID", h.ID().String()),
		zap.Any("addrs", h.Addrs()),
		zap.Bool("quicPreferred", cfg.PreferQUIC))

	// Create DHT
	kadDHT, err := dht.New(ctx, h,
		dht.Mode(dht.ModeAutoServer),
		dht.ProtocolPrefix("/debswarm"),
	)
	if err != nil {
		if closeErr := h.Close(); closeErr != nil {
			logger.Debug("Failed to close host during cleanup", zap.Error(closeErr))
		}
		cancel()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	// Use provided or create new scorer/timeouts
	scorer := cfg.Scorer
	if scorer == nil {
		scorer = peers.NewScorer()
	}

	tm := cfg.Timeouts
	if tm == nil {
		tm = timeouts.NewManager(nil)
	}

	// Set default audit logger if not provided
	auditLogger := cfg.Audit
	if auditLogger == nil {
		auditLogger = &audit.NoopLogger{}
	}

	node := &Node{
		host:                 h,
		dht:                  kadDHT,
		routingDiscovery:     drouting.NewRoutingDiscovery(kadDHT), // Reuse for all lookups
		pingService:          ping.NewPingService(h),               // Keepalive pings
		logger:               logger,
		ctx:                  ctx,
		cancel:               cancel,
		scorer:               scorer,
		timeouts:             tm,
		metrics:              cfg.Metrics,
		audit:                auditLogger,
		bootstrapDone:        make(chan struct{}),
		uploadsPerPeer:       make(map[peer.ID]int),
		maxConcurrentUploads: cfg.MaxConcurrentUploads,
		uploadLimiter:        ratelimit.New(cfg.MaxUploadRate),
		downloadLimiter:      ratelimit.New(cfg.MaxDownloadRate),
		privateSwarm:         privateSwarmMode,
	}

	// Apply default for max concurrent uploads if not set
	if node.maxConcurrentUploads <= 0 {
		node.maxConcurrentUploads = MaxConcurrentUploads
	}

	if cfg.MaxUploadRate > 0 {
		logger.Info("Upload rate limiting enabled", zap.Int64("bytesPerSecond", cfg.MaxUploadRate))
	}
	if cfg.MaxDownloadRate > 0 {
		logger.Info("Download rate limiting enabled", zap.Int64("bytesPerSecond", cfg.MaxDownloadRate))
	}

	// Initialize per-peer rate limiters if configured
	// Per-peer limiting is enabled by default (ExpectedPeers > 0 or explicit rates)
	if cfg.ExpectedPeers > 0 || cfg.PerPeerUploadRate > 0 || cfg.PerPeerDownloadRate > 0 || cfg.AdaptiveEnabled {
		// Upload per-peer limiter
		uploadPeerCfg := ratelimit.PeerLimiterConfig{
			GlobalLimit:            cfg.MaxUploadRate,
			PerPeerLimit:           cfg.PerPeerUploadRate,
			ExpectedPeers:          cfg.ExpectedPeers,
			MinPeerLimit:           cfg.AdaptiveMinRate,
			AdaptiveEnabled:        cfg.AdaptiveEnabled,
			MaxBoostFactor:         cfg.AdaptiveMaxBoost,
			LatencyThresholdMs:     ratelimit.DefaultLatencyThreshold,
			IdleTimeout:            ratelimit.DefaultIdleTimeout,
			AdaptiveRecalcInterval: ratelimit.DefaultAdaptiveRecalc,
			Logger:                 logger.Named("peer-upload-limiter"),
		}
		node.peerUploadLimiter = ratelimit.NewPeerLimiterManager(uploadPeerCfg, node.uploadLimiter, scorer)

		// Download per-peer limiter
		downloadPeerCfg := ratelimit.PeerLimiterConfig{
			GlobalLimit:            cfg.MaxDownloadRate,
			PerPeerLimit:           cfg.PerPeerDownloadRate,
			ExpectedPeers:          cfg.ExpectedPeers,
			MinPeerLimit:           cfg.AdaptiveMinRate,
			AdaptiveEnabled:        cfg.AdaptiveEnabled,
			MaxBoostFactor:         cfg.AdaptiveMaxBoost,
			LatencyThresholdMs:     ratelimit.DefaultLatencyThreshold,
			IdleTimeout:            ratelimit.DefaultIdleTimeout,
			AdaptiveRecalcInterval: ratelimit.DefaultAdaptiveRecalc,
			Logger:                 logger.Named("peer-download-limiter"),
		}
		node.peerDownloadLimiter = ratelimit.NewPeerLimiterManager(downloadPeerCfg, node.downloadLimiter, scorer)

		logger.Info("Per-peer rate limiting enabled",
			zap.Int("expectedPeers", cfg.ExpectedPeers),
			zap.Bool("adaptiveEnabled", cfg.AdaptiveEnabled))
	}

	// Set up transfer protocol handlers
	h.SetStreamHandler(protocol.ID(ProtocolTransfer), node.handleTransferStream)
	h.SetStreamHandler(protocol.ID(ProtocolTransferRange), node.handleRangeTransferStream)

	// Start mDNS discovery if enabled
	if cfg.EnableMDNS {
		mdnsServiceName := "_debswarm._tcp"
		mdnsService := mdns.NewMdnsService(h, mdnsServiceName, node)
		if err := mdnsService.Start(); err != nil {
			logger.Warn("Failed to start mDNS discovery",
				zap.String("service", mdnsServiceName),
				zap.Error(err))
		} else {
			node.mdnsService = mdnsService
			logger.Info("Started mDNS discovery for local peer discovery",
				zap.String("service", mdnsServiceName),
				zap.Strings("listenAddrs", multiaddrsToStrings(h.Addrs())))
		}
	} else {
		logger.Info("mDNS discovery disabled")
	}

	// Bootstrap DHT
	go node.bootstrap(ctx, cfg.BootstrapPeers)

	// Start periodic tasks
	go node.periodicTasks()

	// Start keepalive pings to prevent idle connection pruning
	go node.keepalivePings()

	return node, nil
}

// SetContentGetter sets the function used to get content for serving to peers
func (n *Node) SetContentGetter(getter ContentGetter) {
	n.getContent = getter
}

// bootstrap connects to bootstrap peers and initializes the DHT
func (n *Node) bootstrap(ctx context.Context, bootstrapPeers []string) {
	defer close(n.bootstrapDone)

	n.logger.Info("Starting DHT bootstrap", zap.Int("bootstrapPeers", len(bootstrapPeers)))

	// Connect to bootstrap peers
	var wg sync.WaitGroup
	for _, addr := range bootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			n.logger.Warn("Invalid bootstrap address", zap.String("addr", sanitize.String(addr)), zap.Error(err))
			continue
		}

		peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			n.logger.Warn("Failed to parse bootstrap peer", zap.Error(err))
			continue
		}

		wg.Add(1)
		go func(connectCtx context.Context, pi *peer.AddrInfo) {
			defer wg.Done()
			timeout := n.timeouts.Get(timeouts.OpPeerConnect)
			timeoutCtx, cancel := context.WithTimeout(connectCtx, timeout)
			defer cancel()

			start := time.Now()
			if connectErr := n.host.Connect(timeoutCtx, *pi); connectErr != nil {
				n.logger.Debug("Failed to connect to bootstrap peer",
					zap.String("peer", pi.ID.String()),
					zap.Error(connectErr))
				n.timeouts.RecordFailure(timeouts.OpPeerConnect)
			} else {
				n.logger.Debug("Connected to bootstrap peer",
					zap.String("peer", pi.ID.String()))
				n.timeouts.RecordSuccess(timeouts.OpPeerConnect, time.Since(start))
			}
		}(ctx, peerInfo)
	}
	wg.Wait()

	// Bootstrap the DHT
	if bootstrapErr := n.dht.Bootstrap(ctx); bootstrapErr != nil {
		n.logger.Error("DHT bootstrap failed", zap.Error(bootstrapErr))
		return
	}

	n.logger.Info("DHT bootstrap complete",
		zap.Int("routingTableSize", n.dht.RoutingTable().Size()))

	// Update metrics
	if n.metrics != nil {
		n.metrics.RoutingTableSize.Set(float64(n.dht.RoutingTable().Size()))
		n.metrics.ConnectedPeers.Set(float64(len(n.host.Network().Peers())))
	}
}

// periodicTasks runs background maintenance tasks
func (n *Node) periodicTasks() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			// Cleanup stale peers
			removed := n.scorer.Cleanup()
			if removed > 0 {
				n.logger.Debug("Cleaned up stale peers", zap.Int("removed", removed))
			}

			// Decay timeouts toward base
			n.timeouts.ResetDecay(0.1)

			// Update metrics
			if n.metrics != nil {
				n.metrics.RoutingTableSize.Set(float64(n.dht.RoutingTable().Size()))
				n.metrics.ConnectedPeers.Set(float64(len(n.host.Network().Peers())))
			}
		}
	}
}

// keepalivePings sends periodic pings to all connected peers to prevent
// the connection manager from pruning idle connections
func (n *Node) keepalivePings() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			peers := n.host.Network().Peers()
			if len(peers) == 0 {
				continue
			}

			n.logger.Debug("Sending keepalive pings", zap.Int("peers", len(peers)))

			// Ping all connected peers concurrently
			var wg sync.WaitGroup
			for _, peerID := range peers {
				wg.Add(1)
				go func(parentCtx context.Context, pid peer.ID) {
					defer wg.Done()

					// Short timeout for ping
					ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
					defer cancel()

					// Ping returns a channel with the result
					result := <-n.pingService.Ping(ctx, pid)
					if result.Error != nil {
						n.logger.Debug("Keepalive ping failed",
							zap.String("peer", pid.String()),
							zap.Error(result.Error))
					} else {
						n.logger.Debug("Keepalive ping succeeded",
							zap.String("peer", pid.String()),
							zap.Duration("rtt", result.RTT))
					}
				}(n.ctx, peerID)
			}
			wg.Wait()
		}
	}
}

// WaitForBootstrap blocks until DHT bootstrap is complete
func (n *Node) WaitForBootstrap() {
	<-n.bootstrapDone
}

// Provide announces to the DHT that we have a package with the given hash
func (n *Node) Provide(ctx context.Context, sha256Hash string) error {
	// Skip DHT announcements in private swarm mode to prevent information leakage
	if n.privateSwarm {
		n.logger.Debug("Skipping DHT announcement (private swarm mode)",
			zap.String("hash", sha256Hash[:16]+"..."))
		return nil
	}

	key := NamespacePackage + sha256Hash

	var timer *metrics.Timer
	if n.metrics != nil {
		timer = metrics.NewTimer(n.metrics.DHTLookupDuration)
	} else {
		timer = metrics.NewTimer(nil)
	}

	_, err := n.routingDiscovery.Advertise(ctx, key)

	duration := timer.ObserveDuration()

	if err != nil {
		n.timeouts.RecordFailure(timeouts.OpDHTLookup)
		return fmt.Errorf("failed to provide: %w", err)
	}

	n.timeouts.RecordSuccess(timeouts.OpDHTLookup, duration)
	n.logger.Debug("Announced package to DHT",
		zap.String("hash", sha256Hash[:16]+"..."))
	return nil
}

// FindProviders searches the DHT for peers that have a package
func (n *Node) FindProviders(ctx context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error) {
	key := NamespacePackage + sha256Hash

	var timer *metrics.Timer
	if n.metrics != nil {
		timer = metrics.NewTimer(n.metrics.DHTLookupDuration)
		n.metrics.DHTQueries.WithLabel("find_providers").Inc()
	} else {
		timer = metrics.NewTimer(nil)
	}

	peerChan, err := n.routingDiscovery.FindPeers(ctx, key)
	if err != nil {
		n.timeouts.RecordFailure(timeouts.OpDHTLookup)
		return nil, fmt.Errorf("failed to find providers: %w", err)
	}

	providers := make([]peer.AddrInfo, 0, limit)
	for p := range peerChan {
		if p.ID == n.host.ID() {
			continue // Skip ourselves
		}
		// Skip blacklisted peers
		if n.scorer.IsBlacklisted(p.ID) {
			continue
		}
		providers = append(providers, p)
		if len(providers) >= limit {
			// Drain remaining channel entries in background to prevent goroutine leaks
			go func() {
				for range peerChan {
				}
			}()
			break
		}
	}

	duration := timer.ObserveDuration()
	n.timeouts.RecordSuccess(timeouts.OpDHTLookup, duration)

	// Filter out providers with blocked/private IP addresses (defense against eclipse attacks)
	filtered := make([]peer.AddrInfo, 0, len(providers))
	for _, p := range providers {
		allowedAddrs := security.FilterBlockedAddrs(p.Addrs)
		if len(allowedAddrs) > 0 {
			filtered = append(filtered, peer.AddrInfo{
				ID:    p.ID,
				Addrs: allowedAddrs,
			})
		} else {
			n.logger.Debug("Filtered provider with blocked addresses",
				zap.String("peer", p.ID.String()))
		}
	}

	return filtered, nil
}

// FindProvidersRanked returns providers sorted by score
func (n *Node) FindProvidersRanked(ctx context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error) {
	providers, err := n.FindProviders(ctx, sha256Hash, limit*2) // Get extra for filtering
	if err != nil {
		return nil, err
	}

	// Use scorer to select best peers, with some diversity
	return n.scorer.SelectDiverse(providers, limit), nil
}

// Download attempts to download a package from a peer
func (n *Node) Download(ctx context.Context, peerInfo peer.AddrInfo, sha256Hash string) ([]byte, error) {
	return n.DownloadRange(ctx, peerInfo, sha256Hash, 0, -1)
}

// DownloadRange downloads a range of bytes from a peer
// If end is -1, downloads from start to end of file
func (n *Node) DownloadRange(ctx context.Context, peerInfo peer.AddrInfo, sha256Hash string, start, end int64) ([]byte, error) {
	startTime := time.Now()

	// Connect to peer if not already connected
	if n.host.Network().Connectedness(peerInfo.ID) != network.Connected {
		connectTimeout := n.timeouts.Get(timeouts.OpPeerConnect)
		connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)

		connectStart := time.Now()
		err := n.host.Connect(connectCtx, peerInfo)
		cancel()

		if err != nil {
			n.scorer.RecordFailure(peerInfo.ID, "connect failed")
			n.timeouts.RecordFailure(timeouts.OpPeerConnect)
			return nil, fmt.Errorf("failed to connect to peer: %w", err)
		}
		n.timeouts.RecordSuccess(timeouts.OpPeerConnect, time.Since(connectStart))
	}

	// Choose protocol based on whether we need a range
	proto := ProtocolTransfer
	if start > 0 || end > 0 {
		proto = ProtocolTransferRange
	}

	// Open stream
	stream, err := n.host.NewStream(ctx, peerInfo.ID, protocol.ID(proto))
	if err != nil {
		n.scorer.RecordFailure(peerInfo.ID, "stream failed")
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	// Send request
	var request []byte
	if proto == ProtocolTransferRange {
		// Validate range values to prevent integer overflow
		if start < 0 || end < 0 {
			return nil, fmt.Errorf("invalid range: start=%d, end=%d (negative values not allowed)", start, end)
		}
		// Range request: hash + start (8 bytes) + end (8 bytes) + newline
		request = make([]byte, 64+16+1)
		copy(request, sha256Hash)
		binary.BigEndian.PutUint64(request[64:72], uint64(start)) // #nosec G115 -- validated non-negative above
		binary.BigEndian.PutUint64(request[72:80], uint64(end))   // #nosec G115 -- validated non-negative above
		request[80] = '\n'
	} else {
		// Simple request: hash + newline
		request = []byte(sha256Hash + "\n")
	}

	if _, err := stream.Write(request); err != nil {
		n.scorer.RecordFailure(peerInfo.ID, "write failed")
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response size (8 bytes)
	sizeBuf := make([]byte, 8)
	if _, err := io.ReadFull(stream, sizeBuf); err != nil {
		n.scorer.RecordFailure(peerInfo.ID, "read size failed")
		return nil, fmt.Errorf("failed to read size: %w", err)
	}

	sizeU64 := binary.BigEndian.Uint64(sizeBuf)
	if sizeU64 > math.MaxInt64 {
		return nil, fmt.Errorf("size overflow: %d exceeds max int64", sizeU64)
	}
	size := int64(sizeU64) // #nosec G115 -- validated above

	if size == 0 {
		return nil, fmt.Errorf("peer does not have the requested content")
	}

	if size > MaxTransferSize {
		return nil, fmt.Errorf("content too large: %d bytes", size)
	}

	// Read content with rate limiting (per-peer if available, else global)
	data := make([]byte, size)
	var reader io.Reader = stream
	if n.peerDownloadLimiter != nil && n.peerDownloadLimiter.Enabled() {
		// Use per-peer limiter (includes global limiting via composed reader)
		reader = n.peerDownloadLimiter.ReaderContext(ctx, peerInfo.ID, stream)
	} else if n.downloadLimiter.Enabled() {
		// Fall back to global limiter only
		reader = n.downloadLimiter.ReaderContext(ctx, stream)
	}
	if _, err := io.ReadFull(reader, data); err != nil {
		n.scorer.RecordFailure(peerInfo.ID, "read data failed")
		return nil, fmt.Errorf("failed to read content: %w", err)
	}

	// Record success
	duration := time.Since(startTime)
	latencyMs := float64(duration.Milliseconds())
	throughput := float64(size) / duration.Seconds()

	n.scorer.RecordSuccess(peerInfo.ID, size, latencyMs, throughput)
	n.timeouts.RecordSuccess(timeouts.OpPeerTransfer, duration)

	if n.metrics != nil {
		n.metrics.BytesDownloaded.WithLabel("peer").Add(size)
		n.metrics.DownloadsTotal.WithLabel("peer").Inc()
		n.metrics.PeerLatency.WithLabel(peerInfo.ID.String()).Observe(latencyMs)
	}

	return data, nil
}

// handleTransferStream handles incoming transfer requests (full file)
func (n *Node) handleTransferStream(stream network.Stream) {
	n.handleTransferRequest(stream, false)
}

// handleRangeTransferStream handles incoming range transfer requests
func (n *Node) handleRangeTransferStream(stream network.Stream) {
	n.handleTransferRequest(stream, true)
}

func (n *Node) handleTransferRequest(stream network.Stream, rangeSupport bool) {
	defer stream.Close()

	// Set stream deadline to prevent slowloris attacks
	// If this fails, subsequent I/O operations may hang indefinitely, so bail out
	if err := stream.SetDeadline(time.Now().Add(2 * time.Minute)); err != nil {
		n.logger.Warn("Failed to set stream deadline, rejecting request", zap.Error(err))
		return
	}

	peerID := stream.Conn().RemotePeer()

	// Check upload limits
	if !n.canAcceptUpload(peerID) {
		n.writeSize(stream, 0)
		return
	}
	n.trackUploadStart(peerID)
	defer n.trackUploadEnd(peerID)

	if n.metrics != nil {
		n.metrics.ActiveUploads.Inc()
		defer n.metrics.ActiveUploads.Dec()
	}

	// Read request using buffered reader for efficiency
	bufReader := bufio.NewReader(stream)
	var sha256Hash string
	var start, end int64 = 0, -1

	if rangeSupport {
		// Range request: hash (64) + start (8) + end (8) + newline
		line, err := bufReader.ReadBytes('\n')
		if err != nil {
			return
		}
		// Remove newline
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}

		if len(line) >= 80 {
			sha256Hash = string(line[:64])
			startU64 := binary.BigEndian.Uint64(line[64:72])
			endU64 := binary.BigEndian.Uint64(line[72:80])
			// Validate values fit in int64 to prevent overflow
			if startU64 > math.MaxInt64 || endU64 > math.MaxInt64 {
				n.logger.Warn("Invalid range values in request (overflow)",
					zap.Uint64("start", startU64),
					zap.Uint64("end", endU64))
				return
			}
			start = int64(startU64) // #nosec G115 -- validated above
			end = int64(endU64)     // #nosec G115 -- validated above
		} else {
			sha256Hash = string(line)
		}
	} else {
		// Simple request: hash + newline
		line, err := bufReader.ReadBytes('\n')
		if err != nil {
			return
		}
		// Remove newline
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		sha256Hash = string(line)
	}

	if len(sha256Hash) != 64 {
		n.logger.Debug("Invalid hash length", zap.Int("length", len(sha256Hash)))
		n.writeSize(stream, 0)
		return
	}

	// Validate hex
	if _, err := hex.DecodeString(sha256Hash); err != nil {
		n.logger.Debug("Invalid hash format", zap.Error(err))
		n.writeSize(stream, 0)
		return
	}

	// Get content
	if n.getContent == nil {
		n.writeSize(stream, 0)
		return
	}

	reader, totalSize, err := n.getContent(sha256Hash)
	if err != nil {
		n.logger.Debug("Content not found", zap.String("hash", sha256Hash[:16]+"..."))
		n.writeSize(stream, 0)
		return
	}
	defer reader.Close()

	// Handle range
	if end == -1 || end > totalSize {
		end = totalSize
	}
	if start < 0 {
		start = 0
	}

	// Validate range bounds to prevent negative responseSize or out-of-bounds access
	if start > end {
		n.logger.Debug("Invalid range: start > end",
			zap.Int64("start", start),
			zap.Int64("end", end),
			zap.String("hash", sha256Hash[:16]+"..."))
		n.writeSize(stream, 0)
		return
	}
	if start >= totalSize {
		n.logger.Debug("Invalid range: start >= totalSize",
			zap.Int64("start", start),
			zap.Int64("totalSize", totalSize),
			zap.String("hash", sha256Hash[:16]+"..."))
		n.writeSize(stream, 0)
		return
	}

	responseSize := end - start

	// Skip to start position if needed
	if start > 0 {
		if seeker, ok := reader.(io.Seeker); ok {
			if _, seekErr := seeker.Seek(start, io.SeekStart); seekErr != nil {
				n.writeSize(stream, 0)
				return
			}
		} else {
			// Can't seek, read and discard
			if _, discardErr := io.CopyN(io.Discard, reader, start); discardErr != nil {
				n.writeSize(stream, 0)
				return
			}
		}
	}

	// Send size
	n.writeSize(stream, responseSize)

	// Send content (limited to response size) with rate limiting (per-peer if available, else global)
	// Use context from the node to support proper cancellation
	var writer io.Writer = stream
	if n.peerUploadLimiter != nil && n.peerUploadLimiter.Enabled() {
		// Use per-peer limiter (includes global limiting via composed writer)
		writer = n.peerUploadLimiter.WriterContext(n.ctx, peerID, stream)
	} else if n.uploadLimiter.Enabled() {
		// Fall back to global limiter only
		writer = n.uploadLimiter.WriterContext(n.ctx, stream)
	}
	written, err := io.CopyN(writer, reader, responseSize)
	if err != nil {
		n.logger.Debug("Failed to send content", zap.Error(err))
		return
	}

	n.logger.Debug("Sent content to peer",
		zap.String("peer", peerID.String()),
		zap.String("hash", sha256Hash[:16]+"..."),
		zap.Int64("bytes", written),
		zap.Int64("start", start),
		zap.Int64("end", end))

	// Update metrics
	n.scorer.RecordUpload(peerID, written)
	if n.metrics != nil {
		n.metrics.BytesUploaded.WithLabel(peerID.String()).Add(written)
	}

	// Audit log upload complete
	n.audit.Log(audit.NewUploadCompleteEvent(sha256Hash, written, peerID.String(), 0))
}

func (n *Node) writeSize(stream network.Stream, size int64) {
	sizeBuf := make([]byte, 8)
	// Size is always non-negative (file sizes), safe to convert
	if size < 0 {
		size = 0
	}
	binary.BigEndian.PutUint64(sizeBuf, uint64(size)) // #nosec G115 -- validated non-negative above
	_, _ = stream.Write(sizeBuf)
}

func (n *Node) canAcceptUpload(peerID peer.ID) bool {
	n.uploadsMu.Lock()
	defer n.uploadsMu.Unlock()

	if n.activeUploads >= n.maxConcurrentUploads {
		return false
	}

	if n.uploadsPerPeer[peerID] >= MaxUploadsPerPeer {
		return false
	}

	return true
}

func (n *Node) trackUploadStart(peerID peer.ID) {
	n.uploadsMu.Lock()
	defer n.uploadsMu.Unlock()
	n.activeUploads++
	n.uploadsPerPeer[peerID]++
}

func (n *Node) trackUploadEnd(peerID peer.ID) {
	n.uploadsMu.Lock()
	defer n.uploadsMu.Unlock()
	n.activeUploads--
	n.uploadsPerPeer[peerID]--
	if n.uploadsPerPeer[peerID] <= 0 {
		delete(n.uploadsPerPeer, peerID)
	}
}

// HandlePeerFound implements mdns.Notifee
func (n *Node) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.host.ID() {
		return
	}

	n.logger.Info("Discovered peer via mDNS",
		zap.String("peerID", pi.ID.String()),
		zap.Strings("addrs", multiaddrsToStrings(pi.Addrs)))

	// Mark peer as mDNS-discovered for scoring priority
	if n.scorer != nil {
		n.scorer.MarkAsMDNSPeer(pi.ID)
	}

	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()

	if err := n.host.Connect(ctx, pi); err != nil {
		n.logger.Warn("Failed to connect to mDNS discovered peer",
			zap.String("peerID", pi.ID.String()),
			zap.Error(err))
	} else {
		n.logger.Info("Connected to mDNS discovered peer",
			zap.String("peerID", pi.ID.String()))
	}
}

// multiaddrsToStrings converts multiaddrs to string slice for logging
func multiaddrsToStrings(addrs []multiaddr.Multiaddr) []string {
	strs := make([]string, len(addrs))
	for i, addr := range addrs {
		strs[i] = addr.String()
	}
	return strs
}

// Getters for node information

func (n *Node) PeerID() peer.ID              { return n.host.ID() }
func (n *Node) Addrs() []multiaddr.Multiaddr { return n.host.Addrs() }
func (n *Node) ConnectedPeers() int          { return len(n.host.Network().Peers()) }
func (n *Node) RoutingTableSize() int        { return n.dht.RoutingTable().Size() }
func (n *Node) Scorer() *peers.Scorer        { return n.scorer }
func (n *Node) Timeouts() *timeouts.Manager  { return n.timeouts }

// GetPeerStats returns statistics for all known peers
func (n *Node) GetPeerStats() []*peers.PeerScore {
	return n.scorer.GetAllStats()
}

// GetMDNSPeers returns all connected peers discovered via mDNS.
// For fleet coordination, this returns all connected peers since mDNS
// discovery happens for LAN peers.
func (n *Node) GetMDNSPeers() []peer.AddrInfo {
	peerIDs := n.host.Network().Peers()
	result := make([]peer.AddrInfo, 0, len(peerIDs))
	for _, pid := range peerIDs {
		// Get peer addresses from peerstore
		addrs := n.host.Peerstore().Addrs(pid)
		if len(addrs) > 0 {
			result = append(result, peer.AddrInfo{
				ID:    pid,
				Addrs: addrs,
			})
		}
	}
	return result
}

// Host returns the underlying libp2p host for protocol registration.
func (n *Node) Host() host.Host {
	return n.host
}

// Close shuts down the P2P node
func (n *Node) Close() error {
	n.cancel()

	// Close per-peer rate limiters
	if n.peerUploadLimiter != nil {
		n.peerUploadLimiter.Close()
	}
	if n.peerDownloadLimiter != nil {
		n.peerDownloadLimiter.Close()
	}

	if n.mdnsService != nil {
		if err := n.mdnsService.Close(); err != nil {
			n.logger.Warn("Failed to close mDNS service", zap.Error(err))
		}
	}

	if err := n.dht.Close(); err != nil {
		n.logger.Warn("Failed to close DHT", zap.Error(err))
	}

	return n.host.Close()
}
