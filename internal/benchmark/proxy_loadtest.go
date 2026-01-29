// Package benchmark provides proxy load testing utilities
package benchmark

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyLoadTest runs load tests against the HTTP proxy
type ProxyLoadTest struct {
	ProxyAddr   string
	TargetURL   string
	Concurrency int
	Duration    time.Duration
	Client      *http.Client
}

// ProxyLoadResult contains load test results
type ProxyLoadResult struct {
	TotalRequests  int64
	SuccessCount   int64
	ErrorCount     int64
	TotalBytes     int64
	Duration       time.Duration
	RequestsPerSec float64
	AvgLatency     time.Duration
	MinLatency     time.Duration
	MaxLatency     time.Duration
	P50Latency     time.Duration
	P95Latency     time.Duration
	P99Latency     time.Duration
	ThroughputMBps float64
	ErrorsByType   map[string]int64
	StatusCodeDist map[int]int64
}

// Run executes the load test
func (lt *ProxyLoadTest) Run(ctx context.Context) (*ProxyLoadResult, error) {
	if lt.Client == nil {
		lt.Client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        lt.Concurrency * 2,
				MaxIdleConnsPerHost: lt.Concurrency * 2,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}

	result := &ProxyLoadResult{
		ErrorsByType:   make(map[string]int64),
		StatusCodeDist: make(map[int]int64),
		MinLatency:     time.Hour, // Will be replaced
	}

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		latencies  []time.Duration
		stopSignal = make(chan struct{})
	)

	// Create test context with timeout
	testCtx, cancel := context.WithTimeout(ctx, lt.Duration)
	defer cancel()

	startTime := time.Now()

	// Spawn workers
	for i := 0; i < lt.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-testCtx.Done():
					return
				case <-stopSignal:
					return
				default:
				}

				reqStart := time.Now()
				statusCode, bytesRead, err := lt.doRequest(testCtx)
				latency := time.Since(reqStart)

				atomic.AddInt64(&result.TotalRequests, 1)

				mu.Lock()
				if err != nil {
					atomic.AddInt64(&result.ErrorCount, 1)
					errType := classifyError(err)
					result.ErrorsByType[errType]++
				} else {
					atomic.AddInt64(&result.SuccessCount, 1)
					atomic.AddInt64(&result.TotalBytes, bytesRead)
					result.StatusCodeDist[statusCode]++
					latencies = append(latencies, latency)

					if latency < result.MinLatency {
						result.MinLatency = latency
					}
					if latency > result.MaxLatency {
						result.MaxLatency = latency
					}
				}
				mu.Unlock()
			}
		}()
	}

	// Wait for duration or context cancellation
	select {
	case <-testCtx.Done():
	case <-ctx.Done():
		close(stopSignal)
	}

	wg.Wait()
	result.Duration = time.Since(startTime)

	// Calculate statistics
	if len(latencies) > 0 {
		// Sort for percentiles
		sortDurations(latencies)

		var total time.Duration
		for _, l := range latencies {
			total += l
		}
		result.AvgLatency = total / time.Duration(len(latencies))
		result.P50Latency = latencies[len(latencies)*50/100]
		result.P95Latency = latencies[len(latencies)*95/100]
		result.P99Latency = latencies[len(latencies)*99/100]
	}

	if result.Duration > 0 {
		result.RequestsPerSec = float64(result.TotalRequests) / result.Duration.Seconds()
		result.ThroughputMBps = float64(result.TotalBytes) / result.Duration.Seconds() / (1024 * 1024)
	}

	return result, nil
}

func (lt *ProxyLoadTest) doRequest(ctx context.Context) (statusCode int, bytesRead int64, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", lt.TargetURL, nil)
	if err != nil {
		return 0, 0, err
	}

	resp, err := lt.Client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	// Read and discard body
	n, _ := io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, n, nil
}

func classifyError(err error) string {
	errStr := err.Error()
	switch {
	case contains(errStr, "timeout"):
		return "timeout"
	case contains(errStr, "connection refused"):
		return "connection_refused"
	case contains(errStr, "EOF"):
		return "eof"
	case contains(errStr, "context canceled"):
		return "canceled"
	default:
		return "other"
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// sortDurations sorts durations in place (simple insertion sort for small slices)
func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		j := i
		for j > 0 && d[j-1] > d[j] {
			d[j-1], d[j] = d[j], d[j-1]
			j--
		}
	}
}

// PrintProxyLoadResult prints load test results
func PrintProxyLoadResult(w io.Writer, r *ProxyLoadResult) {
	fmt.Fprintf(w, "\nProxy Load Test Results\n")
	fmt.Fprintf(w, "══════════════════════════════════════\n")
	fmt.Fprintf(w, "  Duration:           %v\n", r.Duration.Round(time.Millisecond))
	fmt.Fprintf(w, "  Total requests:     %d\n", r.TotalRequests)
	fmt.Fprintf(w, "  Successful:         %d (%.1f%%)\n", r.SuccessCount,
		float64(r.SuccessCount)/float64(max(r.TotalRequests, 1))*100)
	fmt.Fprintf(w, "  Failed:             %d\n", r.ErrorCount)
	fmt.Fprintf(w, "  Requests/sec:       %.2f\n", r.RequestsPerSec)
	fmt.Fprintf(w, "  Throughput:         %.2f MB/s\n", r.ThroughputMBps)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  Latency:\n")
	fmt.Fprintf(w, "    Avg:              %v\n", r.AvgLatency.Round(time.Microsecond))
	fmt.Fprintf(w, "    Min:              %v\n", r.MinLatency.Round(time.Microsecond))
	fmt.Fprintf(w, "    Max:              %v\n", r.MaxLatency.Round(time.Microsecond))
	fmt.Fprintf(w, "    P50:              %v\n", r.P50Latency.Round(time.Microsecond))
	fmt.Fprintf(w, "    P95:              %v\n", r.P95Latency.Round(time.Microsecond))
	fmt.Fprintf(w, "    P99:              %v\n", r.P99Latency.Round(time.Microsecond))

	if len(r.StatusCodeDist) > 0 {
		fmt.Fprintf(w, "\n  Status codes:\n")
		for code, count := range r.StatusCodeDist {
			fmt.Fprintf(w, "    %d: %d\n", code, count)
		}
	}

	if len(r.ErrorsByType) > 0 {
		fmt.Fprintf(w, "\n  Errors by type:\n")
		for errType, count := range r.ErrorsByType {
			fmt.Fprintf(w, "    %s: %d\n", errType, count)
		}
	}
}
