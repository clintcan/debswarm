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
			} else if fileSize != "" {
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
	cmd.AddCommand(benchmarkStressCmd())
	cmd.AddCommand(benchmarkConcurrencyCmd())
	cmd.AddCommand(benchmarkProxyCmd())

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

func benchmarkStressCmd() *cobra.Command {
	var (
		fileSize    string
		peerCount   int
		concurrency int
	)

	cmd := &cobra.Command{
		Use:   "stress",
		Short: "Run concurrent download stress test",
		Long: `Run multiple concurrent downloads to stress test the downloader.

This tests how the system handles parallel load from multiple clients.

Examples:
  debswarm benchmark stress                           # 10 concurrent, 10MB each
  debswarm benchmark stress --concurrency 50          # 50 concurrent downloads
  debswarm benchmark stress --file-size 50MB -c 20    # 20 concurrent, 50MB each`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle interrupt
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigChan
				fmt.Println("\nInterrupted, stopping stress test...")
				cancel()
			}()

			size, err := config.ParseSize(fileSize)
			if err != nil {
				return fmt.Errorf("invalid file-size: %w", err)
			}

			// Build peer configs
			peerConfigs := make([]benchmark.PeerConfig, peerCount)
			for i := 0; i < peerCount; i++ {
				peerConfigs[i] = benchmark.PeerConfig{
					ID:            fmt.Sprintf("peer-%d", i+1),
					LatencyMin:    10 * time.Millisecond,
					LatencyMax:    30 * time.Millisecond,
					ThroughputBps: 100 * 1024 * 1024, // 100 MB/s per peer
				}
			}

			scenario := benchmark.Scenario{
				Name:        "stress_test",
				FileSize:    size,
				PeerConfigs: peerConfigs,
				MaxWorkers:  4,
			}

			fmt.Printf("Stress Test\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("  Concurrent downloads: %d\n", concurrency)
			fmt.Printf("  File size:           %s\n", formatBytes(size))
			fmt.Printf("  Simulated peers:     %d\n", peerCount)
			fmt.Printf("══════════════════════════════════════\n\n")

			runner := benchmark.NewRunner(os.Stdout)
			result, err := runner.StressBenchmark(ctx, scenario, concurrency)
			if err != nil {
				return err
			}

			// Print results
			fmt.Printf("\nStress Test Results\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("  Total duration:      %v\n", result.TotalDuration.Round(time.Millisecond))
			fmt.Printf("  Successful:          %d / %d\n", result.SuccessCount, result.ConcurrentDownloads)
			fmt.Printf("  Failed:              %d\n", result.ErrorCount)

			if len(result.Durations) > 0 {
				var total time.Duration
				min, max := result.Durations[0], result.Durations[0]
				for _, d := range result.Durations {
					total += d
					if d < min {
						min = d
					}
					if d > max {
						max = d
					}
				}
				avg := total / time.Duration(len(result.Durations))

				fmt.Printf("  Avg download time:   %v\n", avg.Round(time.Millisecond))
				fmt.Printf("  Min/Max:             %v / %v\n",
					min.Round(time.Millisecond), max.Round(time.Millisecond))

				// Calculate aggregate throughput
				totalBytes := size * int64(result.SuccessCount)
				throughput := float64(totalBytes) / result.TotalDuration.Seconds() / (1024 * 1024)
				fmt.Printf("  Aggregate throughput: %.2f MB/s\n", throughput)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&fileSize, "file-size", "10MB", "File size per download")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "n", 10, "Number of concurrent downloads")
	cmd.Flags().IntVar(&peerCount, "peers", 4, "Number of simulated peers")

	return cmd
}

func benchmarkConcurrencyCmd() *cobra.Command {
	var (
		fileSize  string
		peerCount int
		maxWorker int
	)

	cmd := &cobra.Command{
		Use:   "concurrency",
		Short: "Test different worker concurrency levels",
		Long: `Test download performance with varying numbers of parallel chunk workers.

This helps identify the optimal concurrency level for your network conditions.

Examples:
  debswarm benchmark concurrency
  debswarm benchmark concurrency --file-size 200MB --max-workers 16`,
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

			size, err := config.ParseSize(fileSize)
			if err != nil {
				return fmt.Errorf("invalid file-size: %w", err)
			}

			fmt.Printf("Concurrency Benchmark\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("  File size:       %s\n", formatBytes(size))
			fmt.Printf("  Simulated peers: %d\n", peerCount)
			fmt.Printf("  Testing workers: 1 to %d\n", maxWorker)
			fmt.Printf("══════════════════════════════════════\n\n")

			// Test worker counts: 1, 2, 4, 8, ... up to max
			workerCounts := []int{}
			for w := 1; w <= maxWorker; w *= 2 {
				workerCounts = append(workerCounts, w)
			}
			// Add max if not already included
			if workerCounts[len(workerCounts)-1] != maxWorker {
				workerCounts = append(workerCounts, maxWorker)
			}

			runner := benchmark.NewRunner(os.Stdout)
			results, err := runner.ConcurrencyBenchmark(ctx, size, peerCount, workerCounts)
			if err != nil {
				return err
			}

			// Print summary table
			fmt.Printf("\nConcurrency Results Summary\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("  %-10s %-15s %-15s\n", "Workers", "Avg Duration", "Throughput")
			fmt.Printf("  %-10s %-15s %-15s\n", "-------", "------------", "----------")

			for _, r := range results {
				// Extract worker count from scenario name
				var workers int
				fmt.Sscanf(r.Scenario, "workers_%d", &workers)
				fmt.Printf("  %-10d %-15v %.2f MB/s\n",
					workers,
					r.AvgDuration.Round(time.Millisecond),
					r.AvgThroughputMB)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&fileSize, "file-size", "100MB", "File size to test")
	cmd.Flags().IntVar(&peerCount, "peers", 4, "Number of simulated peers")
	cmd.Flags().IntVar(&maxWorker, "max-workers", 8, "Maximum worker count to test")

	return cmd
}

func benchmarkProxyCmd() *cobra.Command {
	var (
		proxyAddr   string
		targetURL   string
		concurrency int
		duration    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Load test a running debswarm proxy",
		Long: `Send concurrent HTTP requests to a running debswarm proxy.

This tests the proxy's ability to handle concurrent client requests.
The proxy must be running before starting this test.

Examples:
  debswarm benchmark proxy --url http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb
  debswarm benchmark proxy --concurrency 50 --duration 30s --url <package-url>
  debswarm benchmark proxy --proxy localhost:9977 --url <package-url>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetURL == "" {
				return fmt.Errorf("--url is required")
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle interrupt
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigChan
				fmt.Println("\nInterrupted, stopping load test...")
				cancel()
			}()

			// Build proxy URL
			fullURL := fmt.Sprintf("http://%s/%s", proxyAddr, targetURL)

			fmt.Printf("Proxy Load Test\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("  Proxy:       %s\n", proxyAddr)
			fmt.Printf("  Target URL:  %s\n", targetURL)
			fmt.Printf("  Concurrency: %d\n", concurrency)
			fmt.Printf("  Duration:    %v\n", duration)
			fmt.Printf("══════════════════════════════════════\n\n")
			fmt.Printf("Running load test...\n")

			lt := &benchmark.ProxyLoadTest{
				ProxyAddr:   proxyAddr,
				TargetURL:   fullURL,
				Concurrency: concurrency,
				Duration:    duration,
			}

			result, err := lt.Run(ctx)
			if err != nil {
				return err
			}

			benchmark.PrintProxyLoadResult(os.Stdout, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&proxyAddr, "proxy", "127.0.0.1:9977", "Proxy address")
	cmd.Flags().StringVar(&targetURL, "url", "", "Target URL to fetch through proxy (required)")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "n", 10, "Number of concurrent requests")
	cmd.Flags().DurationVar(&duration, "duration", 10*time.Second, "Test duration")

	return cmd
}
