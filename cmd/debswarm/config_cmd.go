package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/debswarm/debswarm/internal/config"
	"github.com/spf13/cobra"
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
	}
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
