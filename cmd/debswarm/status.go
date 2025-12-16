package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Resolve data directory using same logic as daemon
			dataDirectory := resolveDataDir(cfg)

			fmt.Printf("debswarm Status\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Proxy Port:     %d\n", cfg.Network.ProxyPort)
			fmt.Printf("P2P Port:       %d\n", cfg.Network.ListenPort)
			fmt.Printf("Cache Path:     %s\n", cfg.Cache.Path)
			fmt.Printf("Data Path:      %s\n", dataDirectory)
			fmt.Printf("Cache Max Size: %s\n", cfg.Cache.MaxSize)
			fmt.Printf("mDNS Enabled:   %v\n", cfg.Privacy.EnableMDNS)

			if cfg.Metrics.Port > 0 {
				fmt.Printf("\nMetrics:        http://%s:%d/metrics\n", cfg.Metrics.Bind, cfg.Metrics.Port)
				fmt.Printf("Stats:          http://%s:%d/stats\n", cfg.Metrics.Bind, cfg.Metrics.Port)
				fmt.Printf("Dashboard:      http://%s:%d/dashboard\n", cfg.Metrics.Bind, cfg.Metrics.Port)
			} else {
				fmt.Printf("\nMetrics:        disabled\n")
			}

			return nil
		},
	}
}
