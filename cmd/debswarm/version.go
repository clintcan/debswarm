package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

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
			fmt.Printf("  • Web dashboard with live charts\n")
			fmt.Printf("  • Private swarms (PSK)\n")
			fmt.Printf("  • Persistent identity\n")
			fmt.Printf("  • Simulated benchmarking\n")
		},
	}
}
