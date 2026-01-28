// debswarm is a peer-to-peer package distribution helper for APT
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	// version is set at build time via -ldflags
	version = "dev"

	// Global flags
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
	rootCmd.AddCommand(rollbackCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
