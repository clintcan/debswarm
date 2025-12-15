package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/benchmark"
	"github.com/debswarm/debswarm/internal/config"
)

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

	cmd.AddCommand(benchmarkListCmd())

	return cmd
}

func benchmarkListCmd() *cobra.Command {
	return &cobra.Command{
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
}
