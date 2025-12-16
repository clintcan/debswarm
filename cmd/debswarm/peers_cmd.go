package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func peersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "Show peer information",
		Long:  "Show information about known peers and their scores",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if cfg.Metrics.Port > 0 {
				fmt.Println("Peer information available via metrics endpoint:")
				fmt.Printf("  curl http://%s:%d/stats\n", cfg.Metrics.Bind, cfg.Metrics.Port)
				fmt.Printf("\nDashboard: http://%s:%d/dashboard\n", cfg.Metrics.Bind, cfg.Metrics.Port)
			} else {
				fmt.Println("Metrics endpoint is disabled (port = 0)")
			}
			fmt.Println("\nFor detailed peer scores, check the daemon logs with --log-level debug")
			return nil
		},
	}
}
