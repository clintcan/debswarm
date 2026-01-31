package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/aptlists"
	"github.com/debswarm/debswarm/internal/audit"
	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/config"
	"github.com/debswarm/debswarm/internal/connectivity"
	"github.com/debswarm/debswarm/internal/dashboard"
	"github.com/debswarm/debswarm/internal/fleet"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/proxy"
	"github.com/debswarm/debswarm/internal/scheduler"
	"github.com/debswarm/debswarm/internal/timeouts"
	"github.com/debswarm/debswarm/internal/verify"
)

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the debswarm daemon",
		Long: `Start the debswarm daemon which provides an HTTP proxy for APT
and participates in the P2P network.

The daemon listens on localhost:9977 for APT requests and automatically
downloads packages from P2P peers when available, falling back to mirrors.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Validate port numbers
			if proxyPort < 1 || proxyPort > 65535 {
				return fmt.Errorf("invalid proxy-port: must be between 1 and 65535")
			}
			if p2pPort < 1 || p2pPort > 65535 {
				return fmt.Errorf("invalid p2p-port: must be between 1 and 65535")
			}
			if metricsPort < 0 || metricsPort > 65535 {
				return fmt.Errorf("invalid metrics-port: must be between 0 and 65535")
			}
			return nil
		},
		RunE: runDaemon,
	}

	cmd.Flags().IntVarP(&proxyPort, "proxy-port", "p", 9977, "HTTP proxy port")
	cmd.Flags().IntVar(&p2pPort, "p2p-port", 4001, "P2P listen port")
	cmd.Flags().IntVar(&metricsPort, "metrics-port", 9978, "Metrics endpoint port (0 to disable)")
	cmd.Flags().StringVar(&metricsBind, "metrics-bind", "127.0.0.1", "Metrics endpoint bind address (SECURITY: 0.0.0.0 exposes stats externally)")
	cmd.Flags().BoolVar(&preferQUIC, "prefer-quic", true, "Prefer QUIC transport over TCP")
	cmd.Flags().StringVar(&maxUploadRate, "max-upload-rate", "", "Max upload rate (e.g., 10MB/s, 0 = unlimited)")
	cmd.Flags().StringVar(&maxDownloadRate, "max-download-rate", "", "Max download rate (e.g., 50MB/s, 0 = unlimited)")

	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	// Set up logger
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("Starting debswarm daemon",
		zap.Int("proxyPort", proxyPort),
		zap.Int("p2pPort", p2pPort),
		zap.Int("metricsPort", metricsPort),
		zap.Bool("preferQUIC", preferQUIC))

	// Load configuration with security warnings
	cfg, warnings, err := loadConfigWithWarnings()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	for _, warn := range warnings {
		logger.Warn("Security warning", zap.String("message", warn.Message), zap.String("file", warn.File))
	}

	// Validate configuration - fail fast on invalid settings
	if validateErr := cfg.Validate(); validateErr != nil {
		return fmt.Errorf("invalid configuration: %w", validateErr)
	}

	// Override with command-line flags
	if proxyPort != 9977 {
		cfg.Network.ProxyPort = proxyPort
	}
	if p2pPort != 4001 {
		cfg.Network.ListenPort = p2pPort
	}
	if metricsPort != 9978 {
		cfg.Metrics.Port = metricsPort
	}
	if metricsBind != "127.0.0.1" {
		cfg.Metrics.Bind = metricsBind
	}

	// Determine data directory for persistent identity
	// Priority: --data-dir flag > STATE_DIRECTORY env > /var/lib/debswarm > ~/.local/share/debswarm
	p2pDataDir := os.Getenv("STATE_DIRECTORY")
	if p2pDataDir == "" {
		// Check standard system location first
		info, statErr := os.Stat("/var/lib/debswarm")
		if statErr == nil && info.IsDir() {
			p2pDataDir = "/var/lib/debswarm"
		} else {
			// Fall back to user data directory
			homeDir, _ := os.UserHomeDir()
			if homeDir != "" {
				p2pDataDir = filepath.Join(homeDir, ".local", "share", "debswarm")
			} else {
				p2pDataDir = filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
			}
		}
	}
	if dataDir != "" {
		p2pDataDir = dataDir
	}

	// Pre-flight directory validation - fail fast if directories are unusable
	if dirErr := validateDirectories(cfg.Cache.Path, p2pDataDir); dirErr != nil {
		return fmt.Errorf("directory validation failed: %w", dirErr)
	}
	logger.Debug("Directory validation passed",
		zap.String("cachePath", cfg.Cache.Path),
		zap.String("dataDir", p2pDataDir))

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Initialize metrics
	m := metrics.New()

	// Initialize audit logger
	var auditLogger audit.Logger = &audit.NoopLogger{}
	if cfg.Logging.Audit.Enabled {
		auditWriter, auditErr := audit.NewJSONWriter(audit.JSONWriterConfig{
			Path:       cfg.Logging.Audit.Path,
			MaxSizeMB:  cfg.Logging.Audit.GetMaxSizeMB(),
			MaxBackups: cfg.Logging.Audit.GetMaxBackups(),
		})
		if auditErr != nil {
			return fmt.Errorf("failed to initialize audit logger: %w", auditErr)
		}
		auditLogger = auditWriter
		defer func() { _ = auditWriter.Close() }()
		logger.Info("Audit logging enabled",
			zap.String("path", cfg.Logging.Audit.Path),
			zap.Int("maxSizeMB", cfg.Logging.Audit.GetMaxSizeMB()),
			zap.Int("maxBackups", cfg.Logging.Audit.GetMaxBackups()))
	}

	// Initialize peer scorer
	scorer := peers.NewScorer()

	// Initialize timeout manager
	tm := timeouts.NewManager(timeouts.DefaultConfig())

	// Initialize cache
	maxSize := cfg.Cache.MaxSizeBytes()
	minFreeSpace := cfg.Cache.MinFreeSpaceBytes()
	pkgCache, err := cache.NewWithMinFreeSpace(cfg.Cache.Path, maxSize, minFreeSpace, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}
	defer func() { _ = pkgCache.Close() }()

	logger.Info("Initialized cache",
		zap.String("path", cfg.Cache.Path),
		zap.Int64("maxSize", maxSize),
		zap.Int64("minFreeSpace", minFreeSpace),
		zap.Int("currentCount", pkgCache.Count()),
		zap.Int64("currentSize", pkgCache.Size()))

	// Update cache metrics
	m.CacheSize.Set(float64(pkgCache.Size()))
	m.CacheCount.Set(float64(pkgCache.Count()))

	// Initialize index
	idx := index.New(cfg.Cache.Path, logger)

	// Initialize APT lists watcher to populate index from local APT cache
	var aptListsWatcher *aptlists.Watcher
	if cfg.Index.GetWatchAPTLists() {
		aptListsWatcher = aptlists.New(idx, logger, &aptlists.Config{
			ListsPath:    cfg.Index.APTListsPath,
			WatchEnabled: true,
		})
		if err := aptListsWatcher.Start(ctx); err != nil {
			logger.Warn("Failed to start APT lists watcher", zap.Error(err))
		}
	}

	// Initialize mirror fetcher
	fetcher := mirror.NewFetcher(nil, logger)

	// Parse rate limits (CLI flags override config)
	uploadRate := maxUploadRate
	if uploadRate == "" {
		uploadRate = cfg.Transfer.MaxUploadRate
	}
	downloadRate := maxDownloadRate
	if downloadRate == "" {
		downloadRate = cfg.Transfer.MaxDownloadRate
	}

	parsedUploadRate, err := config.ParseRate(uploadRate)
	if err != nil {
		return fmt.Errorf("invalid max-upload-rate: %w", err)
	}
	parsedDownloadRate, err := config.ParseRate(downloadRate)
	if err != nil {
		return fmt.Errorf("invalid max-download-rate: %w", err)
	}

	// Load PSK for private swarm if configured
	var psk []byte
	if cfg.Privacy.PSKPath != "" {
		loadedPSK, pskErr := p2p.LoadPSK(cfg.Privacy.PSKPath)
		if pskErr != nil {
			return fmt.Errorf("failed to load PSK: %w", pskErr)
		}
		psk = loadedPSK
		logger.Info("Loaded PSK from file",
			zap.String("path", cfg.Privacy.PSKPath),
			zap.String("fingerprint", p2p.PSKFingerprint(loadedPSK)))
	} else if cfg.Privacy.PSK != "" {
		loadedPSK, pskErr := p2p.ParsePSKFromHex(cfg.Privacy.PSK)
		if pskErr != nil {
			return fmt.Errorf("failed to parse inline PSK: %w", pskErr)
		}
		psk = loadedPSK
		logger.Warn("Using inline PSK from config (consider using psk_path instead)",
			zap.String("fingerprint", p2p.PSKFingerprint(loadedPSK)))
	}

	// Initialize P2P node with QUIC preference
	p2pCfg := &p2p.Config{
		ListenPort:           cfg.Network.ListenPort,
		BootstrapPeers:       cfg.Network.BootstrapPeers,
		EnableMDNS:           cfg.Privacy.EnableMDNS,
		DataDir:              p2pDataDir,
		PreferQUIC:           preferQUIC,
		MaxUploadRate:        parsedUploadRate,
		MaxDownloadRate:      parsedDownloadRate,
		MaxConnections:       cfg.Network.MaxConnections,
		MaxConcurrentUploads: cfg.Transfer.MaxConcurrentUploads,
		PSK:                  psk,
		PeerAllowlist:        cfg.Privacy.PeerAllowlist,
		PeerBlocklist:        cfg.Privacy.PeerBlocklist,
		Scorer:               scorer,
		Timeouts:             tm,
		Metrics:              m,
		Audit:                auditLogger,
		// NAT traversal configuration
		EnableRelay:        cfg.Network.IsRelayEnabled(),
		EnableHolePunching: cfg.Network.IsHolePunchingEnabled(),
		// Per-peer rate limiting configuration
		PerPeerUploadRate:   cfg.Transfer.PerPeerUploadRateBytes(),
		PerPeerDownloadRate: cfg.Transfer.PerPeerDownloadRateBytes(),
		ExpectedPeers:       cfg.Transfer.GetExpectedPeers(),
		AdaptiveEnabled:     cfg.Transfer.IsAdaptiveEnabled(),
		AdaptiveMinRate:     cfg.Transfer.AdaptiveMinRateBytes(),
		AdaptiveMaxBoost:    cfg.Transfer.AdaptiveMaxBoostFactor(),
	}

	p2pNode, err := p2p.New(ctx, p2pCfg, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize P2P node: %w", err)
	}
	defer func() { _ = p2pNode.Close() }()

	// Wait for DHT bootstrap in background
	go func() {
		p2pNode.WaitForBootstrap()
		logger.Info("DHT bootstrap complete",
			zap.Int("routingTableSize", p2pNode.RoutingTableSize()),
			zap.Int("connectedPeers", p2pNode.ConnectedPeers()))

		// Update metrics
		m.RoutingTableSize.Set(float64(p2pNode.RoutingTableSize()))
		m.ConnectedPeers.Set(float64(p2pNode.ConnectedPeers()))
	}()

	// Initialize connectivity monitor
	connectivityMonitor := connectivity.NewMonitor(&connectivity.Config{
		Mode:          cfg.Network.ConnectivityMode,
		CheckInterval: cfg.Network.GetConnectivityCheckInterval(),
		CheckURL:      cfg.Network.GetConnectivityCheckURL(),
		CheckTimeout:  5 * time.Second,
		GetMDNSPeerCount: func() int {
			return p2pNode.ConnectedPeers() // Approximate - includes all connected peers
		},
		OnModeChange: func(old, new connectivity.Mode) {
			logger.Info("Connectivity mode changed",
				zap.String("from", old.String()),
				zap.String("to", new.String()))
		},
	}, logger)

	// Start connectivity monitor in background
	go connectivityMonitor.Start(ctx)

	// Initialize scheduler if enabled
	var sched *scheduler.Scheduler
	if cfg.Scheduler.Enabled {
		// Convert config windows to scheduler windows
		var windows []scheduler.Window
		for _, w := range cfg.Scheduler.Windows {
			windows = append(windows, scheduler.Window{
				Days:      w.Days,
				StartTime: w.StartTime,
				EndTime:   w.EndTime,
			})
		}

		var err error
		sched, err = scheduler.New(&scheduler.Config{
			Enabled:           true,
			Windows:           windows,
			Timezone:          cfg.Scheduler.Timezone,
			OutsideWindowRate: cfg.Scheduler.OutsideWindowRateBytes(),
			InsideWindowRate:  cfg.Scheduler.InsideWindowRateBytes(),
			UrgentFullSpeed:   cfg.Scheduler.IsUrgentFullSpeed(),
		}, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize scheduler: %w", err)
		}
		if sched != nil {
			logger.Info("Scheduler enabled",
				zap.Int("windows", len(windows)),
				zap.String("timezone", cfg.Scheduler.Timezone),
				zap.Int64("outside_rate", cfg.Scheduler.OutsideWindowRateBytes()),
				zap.Bool("in_window", sched.IsInWindow()))
		}
	}

	// Initialize fleet coordinator if enabled
	var fleetCoord *fleet.Coordinator
	if cfg.Fleet.Enabled {
		fleetCoord = fleet.New(&fleet.Config{
			ClaimTimeout:    cfg.Fleet.ClaimTimeoutDuration(),
			MaxWaitTime:     cfg.Fleet.MaxWaitTimeDuration(),
			AllowConcurrent: cfg.Fleet.AllowConcurrent,
			RefreshInterval: cfg.Fleet.RefreshIntervalDuration(),
		}, p2pNode, pkgCache, logger)
		defer func() { _ = fleetCoord.Close() }()

		logger.Info("Fleet coordination enabled",
			zap.Duration("claimTimeout", cfg.Fleet.ClaimTimeoutDuration()),
			zap.Duration("maxWaitTime", cfg.Fleet.MaxWaitTimeDuration()),
			zap.Int("allowConcurrent", cfg.Fleet.AllowConcurrent))
	}

	// Initialize multi-source verifier
	verifier := verify.New(
		verify.DefaultConfig(),
		&providerFinderAdapter{node: p2pNode},
		logger,
		m,
		auditLogger,
	)
	defer func() { _ = verifier.Close() }()
	logger.Debug("Multi-source verifier initialized")

	// Initialize proxy server
	proxyCfg := &proxy.Config{
		Addr:                       fmt.Sprintf("127.0.0.1:%d", cfg.Network.ProxyPort),
		P2PTimeout:                 5 * time.Second,
		DHTLookupLimit:             10,
		MetricsPort:                cfg.Metrics.Port,
		MetricsBind:                cfg.Metrics.Bind,
		CacheMaxSize:               maxSize,
		MaxConcurrentPeerDownloads: cfg.Transfer.MaxConcurrentPeerDownloads,
		Metrics:                    m,
		Timeouts:                   tm,
		Scorer:                     scorer,
		Audit:                      auditLogger,
		Connectivity:               connectivityMonitor,
		Scheduler:                  sched,
		Fleet:                      fleetCoord,
		Verifier:                   verifier,
		RetryMaxAttempts:           cfg.Transfer.RetryMaxAttempts,
		RetryInterval:              cfg.Transfer.RetryIntervalDuration(),
		RetryMaxAge:                cfg.Transfer.RetryMaxAgeDuration(),
	}

	proxyServer := proxy.NewServer(proxyCfg, pkgCache, idx, p2pNode, fetcher, logger)
	proxyServer.SetP2PNode(p2pNode)

	// Initialize dashboard
	dashCfg := &dashboard.Config{
		Version:         version,
		PeerID:          p2pNode.PeerID().String(),
		MaxUploadRate:   uploadRate,
		MaxDownloadRate: downloadRate,
	}
	dash := dashboard.New(dashCfg, proxyServer.GetDashboardStats, proxyServer.GetPeerInfo)
	proxyServer.SetDashboard(dash)

	// Start periodic tasks
	go runPeriodicTasks(ctx, proxyServer, pkgCache, p2pNode, m, logger, cfg.DHT.AnnounceIntervalDuration())

	// Start proxy server in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := proxyServer.Start(); err != nil {
			errChan <- err
		}
	}()

	logger.Info("debswarm daemon started",
		zap.String("peerID", p2pNode.PeerID().String()),
		zap.String("proxyAddr", proxyCfg.Addr),
		zap.String("metricsAddr", fmt.Sprintf("%s:%d/metrics", cfg.Metrics.Bind, cfg.Metrics.Port)))

	// Wait for shutdown signal or error
	for {
		select {
		case sig := <-sigChan:
			if sig == syscall.SIGHUP {
				logger.Info("Received SIGHUP, reloading configuration")
				if err := reloadConfig(logger, p2pNode, pkgCache); err != nil {
					logger.Error("Config reload failed", zap.Error(err))
				} else {
					logger.Info("Configuration reloaded successfully")
				}
				continue
			}
			logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		case err := <-errChan:
			logger.Error("Server error", zap.Error(err))
			return err
		}
		break
	}

	// Graceful shutdown
	logger.Info("Shutting down...")

	// Stop APT lists watcher
	if aptListsWatcher != nil {
		aptListsWatcher.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := proxyServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("Proxy shutdown error", zap.Error(err))
	}

	logger.Info("Shutdown complete")
	return nil
}

// providerFinderAdapter adapts p2p.Node to the verify.ProviderFinder interface
type providerFinderAdapter struct {
	node *p2p.Node
}

func (a *providerFinderAdapter) FindProviders(ctx context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error) {
	return a.node.FindProviders(ctx, sha256Hash, limit)
}

func (a *providerFinderAdapter) ID() peer.ID {
	return a.node.PeerID()
}

func runPeriodicTasks(
	ctx context.Context,
	proxyServer *proxy.Server,
	pkgCache *cache.Cache,
	p2pNode *p2p.Node,
	m *metrics.Metrics,
	logger *zap.Logger,
	announceInterval time.Duration,
) {
	announceTicker := time.NewTicker(announceInterval)
	metricsTicker := time.NewTicker(30 * time.Second)
	defer announceTicker.Stop()
	defer metricsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-announceTicker.C:
			logger.Info("Running periodic reannouncement")
			if err := proxyServer.ReannouncePackages(ctx); err != nil {
				logger.Warn("Reannouncement failed", zap.Error(err))
			}

		case <-metricsTicker.C:
			// Update metrics
			m.CacheSize.Set(float64(pkgCache.Size()))
			m.CacheCount.Set(float64(pkgCache.Count()))
			m.ConnectedPeers.Set(float64(p2pNode.ConnectedPeers()))
			m.RoutingTableSize.Set(float64(p2pNode.RoutingTableSize()))
		}
	}
}

// validateDirectories performs pre-flight checks on required directories.
// This ensures the daemon fails fast with clear errors if directories are
// missing or not writable, rather than failing later during operation.
func validateDirectories(cachePath, dataDir string) error {
	// Check cache directory - try to use it directly first
	if checkErr := checkDirectory(cachePath, "cache"); checkErr != nil {
		if os.IsNotExist(checkErr) {
			// Cache directory doesn't exist - try to create it
			if mkdirErr := os.MkdirAll(cachePath, 0755); mkdirErr != nil {
				return fmt.Errorf("cannot create cache directory %s: %w", cachePath, mkdirErr)
			}
			// Verify it's now writable
			if verifyErr := checkDirectory(cachePath, "cache"); verifyErr != nil {
				return verifyErr
			}
		} else {
			return checkErr
		}
	}

	// Check data directory (for identity keys, etc.)
	if dataDir != "" {
		if checkErr := checkDirectory(dataDir, "data"); checkErr != nil {
			if os.IsNotExist(checkErr) {
				// Data directory doesn't exist - try to create it
				if mkdirErr := os.MkdirAll(dataDir, 0700); mkdirErr != nil {
					return fmt.Errorf("cannot create data directory %s: %w", dataDir, mkdirErr)
				}
			} else {
				return checkErr
			}
		}
	}

	return nil
}

// checkDirectory verifies a directory exists and is writable.
func checkDirectory(path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("%s directory %s: %w", name, path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s path %s is not a directory", name, path)
	}

	// Check if writable by attempting to create a temp file
	testFile := filepath.Join(path, ".debswarm-write-test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("%s directory %s is not writable: %w", name, path, err)
	}
	_ = f.Close()
	_ = os.Remove(testFile)

	return nil
}

// reloadConfig reloads configuration that can be changed at runtime.
// Some settings (ports, cache path) require a full restart.
func reloadConfig(logger *zap.Logger, _ *p2p.Node, pkgCache *cache.Cache) error {
	// Load new configuration
	newCfg, warnings, err := loadConfigWithWarnings()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Log security warnings
	for _, warn := range warnings {
		logger.Warn("Security warning", zap.String("message", warn.Message), zap.String("file", warn.File))
	}

	// Validate new configuration
	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Reload rate limits (if p2p node supports it)
	newUploadRate := newCfg.Transfer.MaxUploadRateBytes()
	newDownloadRate := newCfg.Transfer.MaxDownloadRateBytes()

	if newUploadRate > 0 || newDownloadRate > 0 {
		logger.Info("Rate limits updated",
			zap.Int64("uploadRate", newUploadRate),
			zap.Int64("downloadRate", newDownloadRate))
	}

	// Check database integrity during reload
	if err := pkgCache.CheckIntegrity(); err != nil {
		logger.Warn("Cache database integrity check failed", zap.Error(err))
	}

	// Log what was reloaded and what requires restart
	logger.Info("Configuration reload complete",
		zap.String("note", "Port changes require daemon restart"))

	return nil
}
