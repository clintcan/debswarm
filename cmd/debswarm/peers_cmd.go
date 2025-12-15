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
			fmt.Println("Peer information available via metrics endpoint:")
			fmt.Println("  curl http://127.0.0.1:9978/stats")
			fmt.Println("\nFor detailed peer scores, check the daemon logs with --log-level debug")
			return nil
		},
	}
}
