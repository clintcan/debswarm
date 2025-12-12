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

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
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

	// Initialize P2P node with QUIC preference
	p2pCfg := &p2p.Config{
		ListenPort:      cfg.Network.ListenPort,
		BootstrapPeers:  cfg.Network.BootstrapPeers,
		EnableMDNS:      cfg.Privacy.EnableMDNS,
		PreferQUIC:      preferQUIC,
		MaxUploadRate:   parsedUploadRate,
		MaxDownloadRate: parsedDownloadRate,
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
  debswarm seed import package1.deb package2.deb`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeedImport(args, recursive, announce)
		},
	}

	importCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively scan directories")
	importCmd.Flags().BoolVarP(&announce, "announce", "a", true, "Announce packages to DHT")

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

func runSeedImport(args []string, recursive, announce bool) error {
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

	// Import each file
	var imported, skipped, failed int
	for _, path := range debFiles {
		hash, size, err := importDebFile(pkgCache, path)
		if err != nil {
			if err.Error() == "already cached" {
				skipped++
				fmt.Printf("  [SKIP] %s (already cached)\n", filepath.Base(path))
			} else {
				failed++
				fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(path), err)
			}
			continue
		}

		imported++
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
