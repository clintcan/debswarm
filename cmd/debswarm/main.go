// debswarm is a peer-to-peer package distribution helper for APT
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/debswarm/debswarm/internal/benchmark"
	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/config"
	"github.com/debswarm/debswarm/internal/dashboard"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/proxy"
	"github.com/debswarm/debswarm/internal/timeouts"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// Set at build time via -ldflags
	version = "dev"

	cfgFile         string
	logLevel        string
	logFile         string
	dataDir         string
	proxyPort       int
	p2pPort         int
	metricsPort     int
	metricsBind     string
	preferQUIC      bool
	maxUploadRate   string
	maxDownloadRate string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "debswarm",
		Short: "Peer-to-peer package distribution for APT",
		Long: `debswarm is a peer-to-peer package distribution system that integrates
with APT to download Debian packages from other peers, reducing load on
mirrors while maintaining security through hash verification.

Features:
  • Parallel chunked downloads from multiple peers
  • Adaptive timeouts based on network conditions
  • Peer scoring and selection for optimal performance
  • QUIC transport preference for better NAT traversal
  • Prometheus metrics for monitoring`,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&logFile, "log-file", "", "log file path (default: stderr)")
	rootCmd.PersistentFlags().StringVarP(&dataDir, "data-dir", "d", "", "data directory")

	// Add commands
	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(cacheCmd())
	rootCmd.AddCommand(peersCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(seedCmd())
	rootCmd.AddCommand(pskCmd())
	rootCmd.AddCommand(identityCmd())
	rootCmd.AddCommand(benchmarkCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

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

	// Override with command-line flags
	if proxyPort != 9977 {
		cfg.Network.ProxyPort = proxyPort
	}
	if p2pPort != 4001 {
		cfg.Network.ListenPort = p2pPort
	}

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize metrics
	m := metrics.New()

	// Initialize peer scorer
	scorer := peers.NewScorer()

	// Initialize timeout manager
	tm := timeouts.NewManager(timeouts.DefaultConfig())

	// Initialize cache
	maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
	pkgCache, err := cache.New(cfg.Cache.Path, maxSize, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}
	defer pkgCache.Close()

	logger.Info("Initialized cache",
		zap.String("path", cfg.Cache.Path),
		zap.Int64("maxSize", maxSize),
		zap.Int("currentCount", pkgCache.Count()),
		zap.Int64("currentSize", pkgCache.Size()))

	// Update cache metrics
	m.CacheSize.Set(float64(pkgCache.Size()))
	m.CacheCount.Set(float64(pkgCache.Count()))

	// Initialize index
	idx := index.New(cfg.Cache.Path, logger)

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
		loadedPSK, err := p2p.LoadPSK(cfg.Privacy.PSKPath)
		if err != nil {
			return fmt.Errorf("failed to load PSK: %w", err)
		}
		psk = loadedPSK
		logger.Info("Loaded PSK from file",
			zap.String("path", cfg.Privacy.PSKPath),
			zap.String("fingerprint", p2p.PSKFingerprint(loadedPSK)))
	} else if cfg.Privacy.PSK != "" {
		loadedPSK, err := p2p.ParsePSKFromHex(cfg.Privacy.PSK)
		if err != nil {
			return fmt.Errorf("failed to parse inline PSK: %w", err)
		}
		psk = loadedPSK
		logger.Warn("Using inline PSK from config (consider using psk_path instead)",
			zap.String("fingerprint", p2p.PSKFingerprint(loadedPSK)))
	}

	// Determine data directory for persistent identity
	// Use parent of cache path + /data, or ~/.local/share/debswarm
	p2pDataDir := filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
	if dataDir != "" {
		p2pDataDir = dataDir
	}

	// Initialize P2P node with QUIC preference
	p2pCfg := &p2p.Config{
		ListenPort:      cfg.Network.ListenPort,
		BootstrapPeers:  cfg.Network.BootstrapPeers,
		EnableMDNS:      cfg.Privacy.EnableMDNS,
		DataDir:         p2pDataDir,
		PreferQUIC:      preferQUIC,
		MaxUploadRate:   parsedUploadRate,
		MaxDownloadRate: parsedDownloadRate,
		PSK:             psk,
		PeerAllowlist:   cfg.Privacy.PeerAllowlist,
		Scorer:          scorer,
		Timeouts:        tm,
		Metrics:         m,
	}

	p2pNode, err := p2p.New(ctx, p2pCfg, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize P2P node: %w", err)
	}
	defer p2pNode.Close()

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

	// Initialize proxy server
	proxyCfg := &proxy.Config{
		Addr:           fmt.Sprintf("127.0.0.1:%d", cfg.Network.ProxyPort),
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    metricsPort,
		MetricsBind:    metricsBind,
		CacheMaxSize:   maxSize,
		Metrics:        m,
		Timeouts:       tm,
		Scorer:         scorer,
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
	go runPeriodicTasks(ctx, proxyServer, pkgCache, p2pNode, m, logger, cfg.DHT.AnnounceInterval)

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
		zap.String("metricsAddr", fmt.Sprintf("127.0.0.1:%d/metrics", metricsPort)))

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
	case err := <-errChan:
		logger.Error("Server error", zap.Error(err))
		return err
	}

	// Graceful shutdown
	logger.Info("Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := proxyServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("Proxy shutdown error", zap.Error(err))
	}

	logger.Info("Shutdown complete")
	return nil
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

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("debswarm Status\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Proxy Port:     %d\n", cfg.Network.ProxyPort)
			fmt.Printf("P2P Port:       %d\n", cfg.Network.ListenPort)
			fmt.Printf("Cache Path:     %s\n", cfg.Cache.Path)
			fmt.Printf("Cache Max Size: %s\n", cfg.Cache.MaxSize)
			fmt.Printf("mDNS Enabled:   %v\n", cfg.Privacy.EnableMDNS)
			fmt.Printf("\nMetrics:        http://127.0.0.1:9978/metrics\n")
			fmt.Printf("Stats:          http://127.0.0.1:9978/stats\n")

			return nil
		},
	}
}

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the package cache",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List cached packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer c.Close()

			packages, err := c.List()
			if err != nil {
				return err
			}

			fmt.Printf("Cached Packages: %d\n", len(packages))
			fmt.Printf("Total Size:      %s\n", formatBytes(c.Size()))
			fmt.Println()

			for _, pkg := range packages {
				fmt.Printf("  %s  %10s  %s\n",
					pkg.SHA256[:16],
					formatBytes(pkg.Size),
					pkg.Filename)
			}

			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer c.Close()

			packages, err := c.List()
			if err != nil {
				return err
			}

			for _, pkg := range packages {
				if err := c.Delete(pkg.SHA256); err != nil {
					fmt.Printf("Failed to delete %s: %v\n", pkg.SHA256[:16], err)
				}
			}

			fmt.Printf("Cleared %d packages\n", len(packages))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stats",
		Short: "Show cache statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer c.Close()

			packages, _ := c.List()
			unannounced, _ := c.GetUnannounced()

			fmt.Printf("Cache Statistics\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Total Packages:    %d\n", len(packages))
			fmt.Printf("Total Size:        %s\n", formatBytes(c.Size()))
			fmt.Printf("Max Size:          %s\n", cfg.Cache.MaxSize)
			fmt.Printf("Usage:             %.1f%%\n", float64(c.Size())/float64(maxSize)*100)
			fmt.Printf("Unannounced:       %d\n", len(unannounced))

			return nil
		},
	})

	return cmd
}

func peersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "Show peer information",
		Long:  "Show information about known peers and their scores",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Peer information available via metrics endpoint:")
			fmt.Println("  curl http://127.0.0.1:9978/stats")
			fmt.Println("\nFor detailed peer scores, check the daemon logs with --log-level debug")
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("Configuration\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("\n[network]\n")
			fmt.Printf("  listen_port     = %d\n", cfg.Network.ListenPort)
			fmt.Printf("  proxy_port      = %d\n", cfg.Network.ProxyPort)
			fmt.Printf("  max_connections = %d\n", cfg.Network.MaxConnections)
			fmt.Printf("\n[cache]\n")
			fmt.Printf("  path            = %s\n", cfg.Cache.Path)
			fmt.Printf("  max_size        = %s\n", cfg.Cache.MaxSize)
			fmt.Printf("\n[transfer]\n")
			fmt.Printf("  max_upload_rate = %s\n", cfg.Transfer.MaxUploadRate)
			fmt.Printf("\n[privacy]\n")
			fmt.Printf("  enable_mdns     = %v\n", cfg.Privacy.EnableMDNS)
			fmt.Printf("  announce_pkgs   = %v\n", cfg.Privacy.AnnouncePackages)
			fmt.Printf("\n[logging]\n")
			fmt.Printf("  level           = %s\n", cfg.Logging.Level)

			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Create default configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.DefaultConfig()

			var cfgPath string
			if cfgFile != "" {
				cfgPath = cfgFile
			} else {
				homeDir, _ := os.UserHomeDir()
				cfgPath = filepath.Join(homeDir, ".config", "debswarm", "config.toml")
			}

			if err := cfg.Save(cfgPath); err != nil {
				return err
			}

			fmt.Printf("Created configuration file: %s\n", cfgPath)
			return nil
		},
	})

	return cmd
}

func seedCmd() *cobra.Command {
	var recursive bool
	var announce bool
	var sync bool

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed packages to the P2P network",
		Long: `Import and seed .deb packages to make them available to other peers.

This allows you to pre-populate the cache with packages from local files,
making them discoverable via the DHT without waiting for APT requests.`,
	}

	importCmd := &cobra.Command{
		Use:   "import [files or directories...]",
		Short: "Import .deb files into cache and announce to network",
		Long: `Import .deb packages from local files or directories.

Examples:
  debswarm seed import /var/cache/apt/archives/*.deb
  debswarm seed import --recursive /mirror/ubuntu/pool/
  debswarm seed import --recursive --sync /var/www/mirror/pool/
  debswarm seed import package1.deb package2.deb`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeedImport(args, recursive, announce, sync)
		},
	}

	importCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively scan directories")
	importCmd.Flags().BoolVarP(&announce, "announce", "a", true, "Announce packages to DHT")
	importCmd.Flags().BoolVar(&sync, "sync", false, "Remove cached packages not in source (mirror sync mode)")

	cmd.AddCommand(importCmd)

	// Add list command to show seeded packages
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List seeded packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer c.Close()

			packages, err := c.List()
			if err != nil {
				return err
			}

			fmt.Printf("Seeded Packages: %d\n", len(packages))
			fmt.Printf("Total Size:      %s\n\n", formatBytes(c.Size()))

			for _, pkg := range packages {
				fmt.Printf("  %s  %10s  %s\n",
					pkg.SHA256[:16],
					formatBytes(pkg.Size),
					pkg.Filename)
			}

			return nil
		},
	})

	return cmd
}

func runSeedImport(args []string, recursive, announce, syncMode bool) error {
	logger, err := setupLogger()
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Initialize cache
	maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)
	pkgCache, err := cache.New(cfg.Cache.Path, maxSize, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}
	defer pkgCache.Close()

	// Initialize P2P node if announcing
	var p2pNode *p2p.Node
	if announce {
		ctx := context.Background()
		p2pCfg := &p2p.Config{
			ListenPort:     cfg.Network.ListenPort,
			BootstrapPeers: cfg.Network.BootstrapPeers,
			EnableMDNS:     cfg.Privacy.EnableMDNS,
			PreferQUIC:     true,
		}

		p2pNode, err = p2p.New(ctx, p2pCfg, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize P2P: %w", err)
		}
		defer p2pNode.Close()

		fmt.Println("Waiting for DHT bootstrap...")
		p2pNode.WaitForBootstrap()
		fmt.Printf("Connected to %d peers\n\n", p2pNode.ConnectedPeers())
	}

	// Collect all .deb files
	var debFiles []string
	for _, arg := range args {
		files, err := collectDebFiles(arg, recursive)
		if err != nil {
			fmt.Printf("Warning: %s: %v\n", arg, err)
			continue
		}
		debFiles = append(debFiles, files...)
	}

	if len(debFiles) == 0 {
		return fmt.Errorf("no .deb files found")
	}

	fmt.Printf("Found %d .deb files to import\n\n", len(debFiles))

	// Track all source hashes for sync mode
	sourceHashes := make(map[string]struct{})

	// Import each file
	var imported, skipped, failed int
	for _, path := range debFiles {
		hash, size, err := importDebFile(pkgCache, path)
		if err != nil {
			if err.Error() == "already cached" {
				skipped++
				// Still track the hash for sync mode
				if syncMode {
					sourceHashes[hash] = struct{}{}
				}
				fmt.Printf("  [SKIP] %s (already cached)\n", filepath.Base(path))
			} else {
				failed++
				fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(path), err)
			}
			continue
		}

		imported++
		if syncMode {
			sourceHashes[hash] = struct{}{}
		}
		fmt.Printf("  [OK]   %s (%s, %s)\n", filepath.Base(path), formatBytes(size), hash[:12]+"...")

		// Announce to DHT
		if announce && p2pNode != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := p2pNode.Provide(ctx, hash); err != nil {
				fmt.Printf("         Warning: failed to announce: %v\n", err)
			}
			cancel()
		}
	}

	fmt.Printf("\nSummary: %d imported, %d skipped, %d failed\n", imported, skipped, failed)

	// Sync mode: remove packages not in source
	if syncMode {
		fmt.Println("\nSync mode: checking for packages to remove...")
		cachedPkgs, err := pkgCache.List()
		if err != nil {
			return fmt.Errorf("failed to list cache: %w", err)
		}

		var removed int
		for _, pkg := range cachedPkgs {
			if _, exists := sourceHashes[pkg.SHA256]; !exists {
				if err := pkgCache.Delete(pkg.SHA256); err != nil {
					fmt.Printf("  [FAIL] Could not remove %s: %v\n", pkg.Filename, err)
				} else {
					removed++
					fmt.Printf("  [DEL]  %s (%s)\n", pkg.Filename, pkg.SHA256[:12]+"...")
				}
			}
		}
		fmt.Printf("\nRemoved %d old packages\n", removed)
	}

	fmt.Printf("Cache size: %s (%d packages)\n", formatBytes(pkgCache.Size()), pkgCache.Count())

	return nil
}

func collectDebFiles(path string, recursive bool) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(path), ".deb") {
			return []string{path}, nil
		}
		return nil, nil
	}

	// Directory
	var files []string
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && p != path && !recursive {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(p), ".deb") {
			files = append(files, p)
		}
		return nil
	})

	return files, err
}

func importDebFile(c *cache.Cache, path string) (string, int64, error) {
	// Open file
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	// Get file size
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}

	// Calculate SHA256
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", 0, err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))

	// Check if already cached
	if c.Has(hash) {
		return hash, info.Size(), fmt.Errorf("already cached")
	}

	// Seek back to start
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}

	// Store in cache
	filename := filepath.Base(path)
	if err := c.Put(f, hash, filename); err != nil {
		return "", 0, err
	}

	return hash, info.Size(), nil
}

func pskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "psk",
		Short: "Manage Pre-Shared Keys for private swarms",
		Long: `Manage Pre-Shared Keys (PSK) for creating private debswarm networks.

PSK allows you to create isolated swarms that only allow connections from
nodes that have the same key. This is useful for corporate networks or
other private deployments.`,
	}

	// Generate subcommand
	var outputPath string
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new PSK file",
		Long: `Generate a new random Pre-Shared Key and save it to a file.

The generated file uses the standard libp2p PSK format and can be
distributed to all nodes that should participate in the private swarm.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			psk, err := p2p.GeneratePSK()
			if err != nil {
				return fmt.Errorf("failed to generate PSK: %w", err)
			}

			// Determine output path
			if outputPath == "" {
				homeDir, _ := os.UserHomeDir()
				outputPath = filepath.Join(homeDir, ".config", "debswarm", "swarm.key")
			}

			// Create parent directory with restricted permissions
			if err := os.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}

			// Save PSK
			if err := p2p.SavePSK(psk, outputPath); err != nil {
				return fmt.Errorf("failed to save PSK: %w", err)
			}

			fmt.Printf("Generated new PSK\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("File:        %s\n", outputPath)
			fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))
			fmt.Printf("\nTo use this PSK, add to config.toml:\n")
			fmt.Printf("  [privacy]\n")
			fmt.Printf("  psk_path = %q\n", outputPath)
			fmt.Printf("\nOr distribute swarm.key to all nodes in your private swarm.\n")

			return nil
		},
	}
	generateCmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default: ~/.config/debswarm/swarm.key)")

	// Show subcommand
	var pskPath string
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show PSK fingerprint",
		Long: `Display the fingerprint of a PSK file without revealing the actual key.

The fingerprint can be shared to verify that nodes are using the same PSK.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load from config if no path specified
			if pskPath == "" {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if cfg.Privacy.PSKPath != "" {
					pskPath = cfg.Privacy.PSKPath
				} else if cfg.Privacy.PSK != "" {
					psk, err := p2p.ParsePSKFromHex(cfg.Privacy.PSK)
					if err != nil {
						return fmt.Errorf("invalid inline PSK: %w", err)
					}
					fmt.Printf("PSK Fingerprint (from config)\n")
					fmt.Printf("══════════════════════════════════════\n")
					fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))
					fmt.Printf("Source:      inline config\n")
					return nil
				} else {
					return fmt.Errorf("no PSK configured; use --file or configure psk_path in config.toml")
				}
			}

			psk, err := p2p.LoadPSK(pskPath)
			if err != nil {
				return fmt.Errorf("failed to load PSK: %w", err)
			}

			fmt.Printf("PSK Fingerprint\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("File:        %s\n", pskPath)
			fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))

			return nil
		},
	}
	showCmd.Flags().StringVarP(&pskPath, "file", "f", "", "PSK file path (default: from config)")

	cmd.AddCommand(generateCmd)
	cmd.AddCommand(showCmd)

	return cmd
}

func identityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Manage node identity",
		Long: `Manage the persistent identity key for this node.

The identity key determines your peer ID in the P2P network. By default,
debswarm generates a new ephemeral identity on each start. When a data
directory is configured, the identity is persisted for stable peer IDs.`,
	}

	// Show subcommand
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show current identity information",
		Long:  `Display the current peer ID and identity key file location.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Determine data directory
			identityDir := filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
			if dataDir != "" {
				identityDir = dataDir
			}

			keyPath := filepath.Join(identityDir, p2p.IdentityKeyFile)

			fmt.Printf("Node Identity\n")
			fmt.Printf("══════════════════════════════════════\n")

			// Check if identity file exists
			if _, err := os.Stat(keyPath); os.IsNotExist(err) {
				fmt.Printf("Status:      No persistent identity\n")
				fmt.Printf("Key File:    %s (not created yet)\n", keyPath)
				fmt.Printf("\nA persistent identity will be created when the daemon starts.\n")
				fmt.Printf("Until then, ephemeral identities are used.\n")
				return nil
			}

			// Load the identity
			privKey, err := p2p.LoadIdentity(keyPath)
			if err != nil {
				return fmt.Errorf("failed to load identity: %w", err)
			}

			peerID := p2p.IdentityFingerprint(privKey)

			fmt.Printf("Peer ID:     %s\n", peerID)
			fmt.Printf("Key File:    %s\n", keyPath)
			fmt.Printf("Key Type:    Ed25519\n")
			fmt.Printf("\nThis peer ID is stable across daemon restarts.\n")
			fmt.Printf("Share it with others to add to their peer allowlists.\n")

			return nil
		},
	}

	// Regenerate subcommand
	var force bool
	regenerateCmd := &cobra.Command{
		Use:   "regenerate",
		Short: "Regenerate the identity key (WARNING: changes peer ID)",
		Long: `Generate a new identity key, replacing the existing one.

WARNING: This will change your peer ID. Other nodes will see you as a
different peer, and any peer allowlists that include your old ID will
need to be updated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Determine data directory
			identityDir := filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
			if dataDir != "" {
				identityDir = dataDir
			}

			keyPath := filepath.Join(identityDir, p2p.IdentityKeyFile)

			// Check if file exists and confirm
			if _, err := os.Stat(keyPath); err == nil && !force {
				// Load current identity to show what we're replacing
				privKey, err := p2p.LoadIdentity(keyPath)
				if err == nil {
					fmt.Printf("Current Peer ID: %s\n\n", p2p.IdentityFingerprint(privKey))
				}
				return fmt.Errorf("identity file exists at %s\n\nUse --force to regenerate (this will change your peer ID)", keyPath)
			}

			// Ensure directory exists
			if err := os.MkdirAll(identityDir, 0700); err != nil {
				return fmt.Errorf("failed to create identity directory: %w", err)
			}

			// Generate new identity
			privKey, err := p2p.GenerateIdentity()
			if err != nil {
				return fmt.Errorf("failed to generate identity: %w", err)
			}

			// Save identity
			if err := p2p.SaveIdentity(privKey, keyPath); err != nil {
				return fmt.Errorf("failed to save identity: %w", err)
			}

			peerID := p2p.IdentityFingerprint(privKey)

			fmt.Printf("New Identity Generated\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Peer ID:     %s\n", peerID)
			fmt.Printf("Key File:    %s\n", keyPath)
			fmt.Printf("\nThis is now your stable peer ID.\n")

			return nil
		},
	}
	regenerateCmd.Flags().BoolVar(&force, "force", false, "Force regeneration even if identity exists")

	cmd.AddCommand(showCmd)
	cmd.AddCommand(regenerateCmd)

	return cmd
}

func benchmarkCmd() *cobra.Command {
	var (
		fileSize   string
		peerCount  int
		iterations int
		workers    int
		scenario   string
	)

	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Run download performance benchmarks",
		Long: `Run benchmarks using simulated peers to test download performance.

This allows measuring throughput, chunk parallelization, peer scoring,
and retry behavior without needing real peers on the network.

Examples:
  debswarm benchmark                    # Run default scenarios
  debswarm benchmark --scenario all     # Run all scenarios
  debswarm benchmark --file-size 200MB --peers 4 --workers 8
  debswarm benchmark --scenario parallel_fast_peers`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle interrupt
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigChan
				fmt.Println("\nInterrupted, stopping benchmark...")
				cancel()
			}()

			runner := benchmark.NewRunner(os.Stdout)

			var scenarios []benchmark.Scenario

			if scenario != "" && scenario != "all" {
				// Find specific scenario
				for _, s := range benchmark.DefaultScenarios() {
					if s.Name == scenario {
						scenarios = []benchmark.Scenario{s}
						break
					}
				}
				if len(scenarios) == 0 {
					fmt.Printf("Unknown scenario: %s\n\nAvailable scenarios:\n", scenario)
					for _, s := range benchmark.DefaultScenarios() {
						fmt.Printf("  %-25s %s\n", s.Name, s.Description)
					}
					return fmt.Errorf("scenario not found")
				}
			} else if fileSize != "" || peerCount > 0 {
				// Custom scenario from flags
				size, err := config.ParseSize(fileSize)
				if err != nil {
					return fmt.Errorf("invalid file-size: %w", err)
				}

				peerConfigs := make([]benchmark.PeerConfig, peerCount)
				for i := 0; i < peerCount; i++ {
					peerConfigs[i] = benchmark.PeerConfig{
						ID:            fmt.Sprintf("peer-%d", i+1),
						LatencyMin:    10 * time.Millisecond,
						LatencyMax:    30 * time.Millisecond,
						ThroughputBps: 50 * 1024 * 1024, // 50 MB/s
					}
				}

				scenarios = []benchmark.Scenario{{
					Name:        "custom",
					Description: "Custom benchmark from flags",
					FileSize:    size,
					MaxWorkers:  workers,
					PeerConfigs: peerConfigs,
					Iterations:  iterations,
				}}
			} else {
				// Default: run all scenarios
				scenarios = benchmark.DefaultScenarios()
			}

			fmt.Printf("debswarm Benchmark\n")
			fmt.Printf("══════════════════════════════════════\n\n")

			results, err := runner.RunAll(ctx, scenarios)
			if err != nil && err != context.Canceled {
				return err
			}

			benchmark.PrintResults(os.Stdout, results)
			return nil
		},
	}

	cmd.Flags().StringVar(&fileSize, "file-size", "", "File size to test (e.g., 100MB)")
	cmd.Flags().IntVar(&peerCount, "peers", 3, "Number of simulated peers")
	cmd.Flags().IntVar(&iterations, "iterations", 3, "Number of iterations per test")
	cmd.Flags().IntVar(&workers, "workers", 4, "Number of parallel chunk workers")
	cmd.Flags().StringVar(&scenario, "scenario", "", "Run specific scenario (or 'all')")

	// Add list subcommand
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available benchmark scenarios",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Available Benchmark Scenarios\n")
			fmt.Printf("══════════════════════════════════════\n\n")
			for _, s := range benchmark.DefaultScenarios() {
				fmt.Printf("  %-25s %s\n", s.Name, s.Description)
				fmt.Printf("    File: %s, Peers: %d, Workers: %d\n\n",
					formatBytes(s.FileSize), len(s.PeerConfigs), s.MaxWorkers)
			}
		},
	}
	cmd.AddCommand(listCmd)

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("debswarm version %s\n", version)
			fmt.Printf("\nFeatures:\n")
			fmt.Printf("  • Parallel chunked downloads\n")
			fmt.Printf("  • Adaptive timeouts\n")
			fmt.Printf("  • Peer scoring\n")
			fmt.Printf("  • QUIC transport\n")
			fmt.Printf("  • Prometheus metrics\n")
			fmt.Printf("  • Package seeding\n")
			fmt.Printf("  • Bandwidth limiting\n")
			fmt.Printf("  • Web dashboard\n")
			fmt.Printf("  • Private swarms (PSK)\n")
			fmt.Printf("  • Persistent identity\n")
			fmt.Printf("  • Simulated benchmarking\n")
		},
	}
}

func setupLogger() (*zap.Logger, error) {
	level := zapcore.InfoLevel
	switch logLevel {
	case "debug":
		level = zapcore.DebugLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      false,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	if logFile != "" {
		cfg.OutputPaths = []string{logFile}
	}

	return cfg.Build()
}

func loadConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.Load(cfgFile)
	}

	homeDir, _ := os.UserHomeDir()
	paths := []string{
		"/etc/debswarm/config.toml",
		filepath.Join(homeDir, ".config", "debswarm", "config.toml"),
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return config.Load(path)
		}
	}

	return config.DefaultConfig(), nil
}

// loadConfigWithWarnings loads config and returns security warnings for sensitive settings
func loadConfigWithWarnings() (*config.Config, []config.SecurityWarning, error) {
	if cfgFile != "" {
		return config.LoadWithWarnings(cfgFile)
	}

	homeDir, _ := os.UserHomeDir()
	paths := []string{
		"/etc/debswarm/config.toml",
		filepath.Join(homeDir, ".config", "debswarm", "config.toml"),
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return config.LoadWithWarnings(path)
		}
	}

	return config.DefaultConfig(), nil, nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
