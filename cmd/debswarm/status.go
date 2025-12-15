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
