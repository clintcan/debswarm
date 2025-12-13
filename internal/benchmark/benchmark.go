// Package benchmark provides tools for benchmarking debswarm download performance
package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/debswarm/debswarm/internal/downloader"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/peers"
)

// Scenario defines a benchmark test scenario
type Scenario struct {
	Name          string
	Description   string
	FileSize      int64
	ChunkSize     int64
	MaxWorkers    int
	PeerConfigs   []PeerConfig
	IncludeMirror bool
	MirrorConfig  *PeerConfig // Mirror simulated as a peer
	Iterations    int
}

// Result contains the results of a benchmark run
type Result struct {
	Scenario        string
	Iterations      int
	TotalDuration   time.Duration
	AvgDuration     time.Duration
	MinDuration     time.Duration
	MaxDuration     time.Duration
	TotalBytes      int64
	AvgThroughputMB float64
	PeerStats       []PeerStats
	ChunksTotal     int
	ChunksFromP2P   int
	Errors          int
}

// Runner executes benchmark scenarios
type Runner struct {
	scorer  *peers.Scorer
	metrics *metrics.Metrics
	output  io.Writer
}

// NewRunner creates a new benchmark runner
func NewRunner(output io.Writer) *Runner {
	return &Runner{
		scorer:  peers.NewScorer(),
		metrics: metrics.New(),
		output:  output,
	}
}

// Run executes a single benchmark scenario
func (r *Runner) Run(ctx context.Context, scenario Scenario) (*Result, error) {
	if scenario.Iterations <= 0 {
		scenario.Iterations = 1
	}
	if scenario.ChunkSize <= 0 {
		scenario.ChunkSize = downloader.DefaultChunkSize
	}
	if scenario.MaxWorkers <= 0 {
		scenario.MaxWorkers = downloader.MaxConcurrentChunks
	}

	// Create simulated peers
	simPeers := make([]*SimulatedPeer, len(scenario.PeerConfigs))
	peerSources := make([]downloader.Source, len(scenario.PeerConfigs))

	// Generate test data once
	testData := GenerateTestData(scenario.FileSize)
	hash := sha256.Sum256(testData)
	expectedHash := hex.EncodeToString(hash[:])

	for i, cfg := range scenario.PeerConfigs {
		simPeers[i] = NewSimulatedPeer(cfg)
		simPeers[i].AddContent(expectedHash, testData)
		peerSources[i] = simPeers[i]
	}

	// Create optional mirror source
	var mirrorSource downloader.Source
	var mirrorPeer *SimulatedPeer
	if scenario.IncludeMirror && scenario.MirrorConfig != nil {
		mirrorPeer = NewSimulatedPeer(*scenario.MirrorConfig)
		mirrorPeer.AddContent(expectedHash, testData)
		mirrorSource = mirrorPeer
	}

	// Create downloader
	dl := downloader.New(&downloader.Config{
		ChunkSize:     scenario.ChunkSize,
		MaxConcurrent: scenario.MaxWorkers,
		Scorer:        r.scorer,
		Metrics:       r.metrics,
	})

	// Run iterations
	var durations []time.Duration
	var totalBytes int64
	var totalChunks, totalP2PChunks int
	var errors int

	r.log("Running scenario: %s (%d iterations)\n", scenario.Name, scenario.Iterations)
	r.log("  File size: %s, Chunk size: %s, Workers: %d, Peers: %d\n",
		formatBytes(scenario.FileSize),
		formatBytes(scenario.ChunkSize),
		scenario.MaxWorkers,
		len(peerSources))

	for i := 0; i < scenario.Iterations; i++ {
		start := time.Now()

		result, err := dl.Download(ctx, expectedHash, scenario.FileSize, peerSources, mirrorSource)
		duration := time.Since(start)

		if err != nil {
			errors++
			r.log("  Iteration %d: ERROR - %v\n", i+1, err)
			continue
		}

		// Verify hash
		actualHash := sha256.Sum256(result.Data)
		if hex.EncodeToString(actualHash[:]) != expectedHash {
			errors++
			r.log("  Iteration %d: HASH MISMATCH\n", i+1)
			continue
		}

		durations = append(durations, duration)
		totalBytes += result.Size
		totalChunks += result.ChunksTotal
		totalP2PChunks += result.ChunksFromP2P

		throughput := float64(result.Size) / duration.Seconds() / (1024 * 1024)
		r.log("  Iteration %d: %v (%.2f MB/s) - %d chunks, %d from P2P\n",
			i+1, duration.Round(time.Millisecond), throughput,
			result.ChunksTotal, result.ChunksFromP2P)
	}

	if len(durations) == 0 {
		return nil, fmt.Errorf("all iterations failed")
	}

	// Calculate statistics
	res := &Result{
		Scenario:      scenario.Name,
		Iterations:    scenario.Iterations,
		TotalBytes:    totalBytes,
		ChunksTotal:   totalChunks,
		ChunksFromP2P: totalP2PChunks,
		Errors:        errors,
	}

	var totalDur time.Duration
	res.MinDuration = durations[0]
	res.MaxDuration = durations[0]

	for _, d := range durations {
		totalDur += d
		if d < res.MinDuration {
			res.MinDuration = d
		}
		if d > res.MaxDuration {
			res.MaxDuration = d
		}
	}

	res.TotalDuration = totalDur
	res.AvgDuration = totalDur / time.Duration(len(durations))
	res.AvgThroughputMB = float64(totalBytes) / totalDur.Seconds() / (1024 * 1024)

	// Collect peer stats
	for _, p := range simPeers {
		res.PeerStats = append(res.PeerStats, p.Stats())
	}
	if mirrorPeer != nil {
		res.PeerStats = append(res.PeerStats, mirrorPeer.Stats())
	}

	return res, nil
}

// RunAll executes multiple scenarios
func (r *Runner) RunAll(ctx context.Context, scenarios []Scenario) ([]*Result, error) {
	results := make([]*Result, 0, len(scenarios))

	for _, s := range scenarios {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		result, err := r.Run(ctx, s)
		if err != nil {
			r.log("Scenario %s failed: %v\n", s.Name, err)
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

func (r *Runner) log(format string, args ...interface{}) {
	if r.output != nil {
		fmt.Fprintf(r.output, format, args...)
	}
}

// DefaultScenarios returns a set of standard benchmark scenarios
func DefaultScenarios() []Scenario {
	return []Scenario{
		{
			Name:        "single_fast_peer",
			Description: "Single high-speed peer, baseline test",
			FileSize:    100 * 1024 * 1024, // 100 MB
			PeerConfigs: []PeerConfig{
				{
					ID:            "fast-peer-1",
					LatencyMin:    5 * time.Millisecond,
					LatencyMax:    10 * time.Millisecond,
					ThroughputBps: 100 * 1024 * 1024, // 100 MB/s
				},
			},
			Iterations: 3,
		},
		{
			Name:        "single_slow_peer",
			Description: "Single slow peer",
			FileSize:    50 * 1024 * 1024, // 50 MB
			PeerConfigs: []PeerConfig{
				{
					ID:            "slow-peer-1",
					LatencyMin:    50 * time.Millisecond,
					LatencyMax:    100 * time.Millisecond,
					ThroughputBps: 10 * 1024 * 1024, // 10 MB/s
				},
			},
			Iterations: 3,
		},
		{
			Name:        "multiple_heterogeneous",
			Description: "Multiple peers with varying performance",
			FileSize:    100 * 1024 * 1024, // 100 MB
			PeerConfigs: []PeerConfig{
				{
					ID:            "peer-fast",
					LatencyMin:    5 * time.Millisecond,
					LatencyMax:    15 * time.Millisecond,
					ThroughputBps: 80 * 1024 * 1024,
				},
				{
					ID:            "peer-medium",
					LatencyMin:    20 * time.Millisecond,
					LatencyMax:    40 * time.Millisecond,
					ThroughputBps: 40 * 1024 * 1024,
				},
				{
					ID:            "peer-slow",
					LatencyMin:    50 * time.Millisecond,
					LatencyMax:    80 * time.Millisecond,
					ThroughputBps: 15 * 1024 * 1024,
				},
			},
			Iterations: 3,
		},
		{
			Name:        "parallel_fast_peers",
			Description: "Multiple fast peers to test parallelization",
			FileSize:    200 * 1024 * 1024, // 200 MB
			MaxWorkers:  8,
			PeerConfigs: []PeerConfig{
				{ID: "peer-1", LatencyMin: 5 * time.Millisecond, LatencyMax: 10 * time.Millisecond, ThroughputBps: 50 * 1024 * 1024},
				{ID: "peer-2", LatencyMin: 5 * time.Millisecond, LatencyMax: 10 * time.Millisecond, ThroughputBps: 50 * 1024 * 1024},
				{ID: "peer-3", LatencyMin: 5 * time.Millisecond, LatencyMax: 10 * time.Millisecond, ThroughputBps: 50 * 1024 * 1024},
				{ID: "peer-4", LatencyMin: 5 * time.Millisecond, LatencyMax: 10 * time.Millisecond, ThroughputBps: 50 * 1024 * 1024},
			},
			Iterations: 3,
		},
		{
			Name:        "unreliable_peers",
			Description: "Peers with error rates to test retry logic",
			FileSize:    50 * 1024 * 1024,
			PeerConfigs: []PeerConfig{
				{
					ID:            "unreliable-1",
					LatencyMin:    10 * time.Millisecond,
					LatencyMax:    20 * time.Millisecond,
					ThroughputBps: 50 * 1024 * 1024,
					ErrorRate:     0.1, // 10% error rate
				},
				{
					ID:            "unreliable-2",
					LatencyMin:    10 * time.Millisecond,
					LatencyMax:    20 * time.Millisecond,
					ThroughputBps: 50 * 1024 * 1024,
					ErrorRate:     0.05, // 5% error rate
				},
				{
					ID:            "reliable",
					LatencyMin:    15 * time.Millisecond,
					LatencyMax:    25 * time.Millisecond,
					ThroughputBps: 40 * 1024 * 1024,
					ErrorRate:     0.0,
				},
			},
			Iterations: 5,
		},
		{
			Name:        "p2p_vs_mirror_race",
			Description: "Race between P2P and mirror (small file)",
			FileSize:    5 * 1024 * 1024, // 5 MB (below chunk threshold)
			PeerConfigs: []PeerConfig{
				{
					ID:            "peer-1",
					LatencyMin:    10 * time.Millisecond,
					LatencyMax:    20 * time.Millisecond,
					ThroughputBps: 30 * 1024 * 1024,
				},
			},
			IncludeMirror: true,
			MirrorConfig: &PeerConfig{
				ID:            "mirror",
				LatencyMin:    50 * time.Millisecond,
				LatencyMax:    100 * time.Millisecond,
				ThroughputBps: 20 * 1024 * 1024,
			},
			Iterations: 5,
		},
	}
}

// PrintResults prints benchmark results in a formatted table
func PrintResults(w io.Writer, results []*Result) {
	fmt.Fprintln(w, "\n=== Benchmark Results ===")
	fmt.Fprintln(w, "")

	for _, r := range results {
		fmt.Fprintf(w, "Scenario: %s\n", r.Scenario)
		fmt.Fprintf(w, "  Iterations:     %d (errors: %d)\n", r.Iterations, r.Errors)
		fmt.Fprintf(w, "  Avg Duration:   %v\n", r.AvgDuration.Round(time.Millisecond))
		fmt.Fprintf(w, "  Min/Max:        %v / %v\n",
			r.MinDuration.Round(time.Millisecond),
			r.MaxDuration.Round(time.Millisecond))
		fmt.Fprintf(w, "  Avg Throughput: %.2f MB/s\n", r.AvgThroughputMB)
		fmt.Fprintf(w, "  Total Bytes:    %s\n", formatBytes(r.TotalBytes))
		fmt.Fprintf(w, "  Chunks:         %d total, %d from P2P (%.1f%%)\n",
			r.ChunksTotal, r.ChunksFromP2P,
			float64(r.ChunksFromP2P)/float64(max(r.ChunksTotal, 1))*100)

		if len(r.PeerStats) > 0 {
			fmt.Fprintln(w, "  Peer Statistics:")
			for _, ps := range r.PeerStats {
				fmt.Fprintf(w, "    %s: %d requests, %s served, %.1f ms avg latency\n",
					ps.ID, ps.RequestCount, formatBytes(ps.BytesServed), ps.AvgLatencyMs)
			}
		}
		fmt.Fprintln(w, "")
	}
}

// ConcurrencyBenchmark tests different worker counts
func (r *Runner) ConcurrencyBenchmark(ctx context.Context, fileSize int64, peerCount int, workerCounts []int) ([]*Result, error) {
	var results []*Result

	// Create peer configs
	peerConfigs := make([]PeerConfig, peerCount)
	for i := 0; i < peerCount; i++ {
		peerConfigs[i] = PeerConfig{
			ID:            fmt.Sprintf("peer-%d", i+1),
			LatencyMin:    10 * time.Millisecond,
			LatencyMax:    20 * time.Millisecond,
			ThroughputBps: 50 * 1024 * 1024,
		}
	}

	for _, workers := range workerCounts {
		scenario := Scenario{
			Name:        fmt.Sprintf("workers_%d", workers),
			Description: fmt.Sprintf("Concurrency test with %d workers", workers),
			FileSize:    fileSize,
			MaxWorkers:  workers,
			PeerConfigs: peerConfigs,
			Iterations:  3,
		}

		result, err := r.Run(ctx, scenario)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}

	return results, nil
}

// StressBenchmark runs concurrent downloads
func (r *Runner) StressBenchmark(ctx context.Context, scenario Scenario, concurrentDownloads int) (*StressResult, error) {
	r.log("Running stress test: %d concurrent downloads\n", concurrentDownloads)

	// Generate test data
	testData := GenerateTestData(scenario.FileSize)
	hash := sha256.Sum256(testData)
	expectedHash := hex.EncodeToString(hash[:])

	var wg sync.WaitGroup
	results := make(chan time.Duration, concurrentDownloads)
	errors := make(chan error, concurrentDownloads)

	start := time.Now()

	for i := 0; i < concurrentDownloads; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each goroutine gets its own peers
			peerSources := make([]downloader.Source, len(scenario.PeerConfigs))
			for j, cfg := range scenario.PeerConfigs {
				cfg.ID = fmt.Sprintf("%s-%d", cfg.ID, id)
				peer := NewSimulatedPeer(cfg)
				peer.AddContent(expectedHash, testData)
				peerSources[j] = peer
			}

			dl := downloader.New(&downloader.Config{
				ChunkSize:     scenario.ChunkSize,
				MaxConcurrent: scenario.MaxWorkers,
				Scorer:        peers.NewScorer(),
				Metrics:       metrics.New(),
			})

			dlStart := time.Now()
			_, err := dl.Download(ctx, expectedHash, scenario.FileSize, peerSources, nil)
			if err != nil {
				errors <- err
				return
			}
			results <- time.Since(dlStart)
		}(i)
	}

	wg.Wait()
	close(results)
	close(errors)

	totalDuration := time.Since(start)

	// Collect results
	var durations []time.Duration
	for d := range results {
		durations = append(durations, d)
	}

	var errs []error
	for e := range errors {
		errs = append(errs, e)
	}

	return &StressResult{
		ConcurrentDownloads: concurrentDownloads,
		TotalDuration:       totalDuration,
		SuccessCount:        len(durations),
		ErrorCount:          len(errs),
		Durations:           durations,
	}, nil
}

// StressResult contains results from a stress test
type StressResult struct {
	ConcurrentDownloads int
	TotalDuration       time.Duration
	SuccessCount        int
	ErrorCount          int
	Durations           []time.Duration
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
