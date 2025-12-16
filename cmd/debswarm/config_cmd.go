package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/config"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configInitCmd())

	return cmd
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Resolve data directory using same logic as daemon
			dataDirectory := resolveDataDir(cfg)

			fmt.Printf("Configuration\n")
			fmt.Printf("══════════════════════════════════════\n")

			fmt.Printf("\n[network]\n")
			fmt.Printf("  listen_port      = %d\n", cfg.Network.ListenPort)
			fmt.Printf("  proxy_port       = %d\n", cfg.Network.ProxyPort)
			fmt.Printf("  max_connections  = %d\n", cfg.Network.MaxConnections)
			fmt.Printf("  bootstrap_peers  = %d configured\n", len(cfg.Network.BootstrapPeers))

			fmt.Printf("\n[cache]\n")
			fmt.Printf("  path             = %s\n", cfg.Cache.Path)
			fmt.Printf("  max_size         = %s\n", cfg.Cache.MaxSize)
			fmt.Printf("  min_free_space   = %s\n", cfg.Cache.MinFreeSpace)

			fmt.Printf("\n[transfer]\n")
			fmt.Printf("  max_upload_rate  = %s\n", displayRate(cfg.Transfer.MaxUploadRate))
			fmt.Printf("  max_download_rate = %s\n", displayRate(cfg.Transfer.MaxDownloadRate))
			fmt.Printf("  max_concurrent_uploads = %d\n", cfg.Transfer.MaxConcurrentUploads)
			fmt.Printf("  max_concurrent_peer_downloads = %d\n", cfg.Transfer.MaxConcurrentPeerDownloads)
			fmt.Printf("  retry_max_attempts = %d\n", cfg.Transfer.RetryMaxAttempts)
			fmt.Printf("  retry_interval   = %s\n", cfg.Transfer.RetryInterval)
			fmt.Printf("  retry_max_age    = %s\n", cfg.Transfer.RetryMaxAge)

			fmt.Printf("\n[dht]\n")
			fmt.Printf("  provider_ttl     = %s\n", cfg.DHT.ProviderTTL)
			fmt.Printf("  announce_interval = %s\n", cfg.DHT.AnnounceInterval)

			fmt.Printf("\n[privacy]\n")
			fmt.Printf("  enable_mdns      = %v\n", cfg.Privacy.EnableMDNS)
			fmt.Printf("  announce_packages = %v\n", cfg.Privacy.AnnouncePackages)
			if cfg.Privacy.PSKPath != "" {
				fmt.Printf("  psk_path         = %s\n", cfg.Privacy.PSKPath)
			}
			if len(cfg.Privacy.PeerAllowlist) > 0 {
				fmt.Printf("  peer_allowlist   = %d peers\n", len(cfg.Privacy.PeerAllowlist))
			}

			fmt.Printf("\n[metrics]\n")
			fmt.Printf("  port             = %d\n", cfg.Metrics.Port)
			fmt.Printf("  bind             = %s\n", cfg.Metrics.Bind)

			fmt.Printf("\n[logging]\n")
			fmt.Printf("  level            = %s\n", cfg.Logging.Level)
			if cfg.Logging.File != "" {
				fmt.Printf("  file             = %s\n", cfg.Logging.File)
			}

			fmt.Printf("\n[resolved paths]\n")
			fmt.Printf("  data_directory   = %s\n", dataDirectory)

			return nil
		},
	}
}

// displayRate returns a user-friendly rate string
func displayRate(rate string) string {
	if rate == "" || rate == "0" {
		return "unlimited"
	}
	return rate
}

func configInitCmd() *cobra.Command {
	return &cobra.Command{
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
	}
}
