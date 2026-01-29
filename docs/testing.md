# Testing Guide

This document covers testing strategies for debswarm, including unit tests, fuzz testing, and performance benchmarks.

## Running Tests

```bash
# Run all tests with race detector
make test

# Run tests with coverage report
make test-coverage           # Generates coverage.html

# Run single package tests
go test -v ./internal/cache/

# Run linter
make lint                    # Uses golangci-lint
```

## Fuzz Testing

The codebase includes fuzz tests for parsing functions to catch edge cases and potential panics. Go's native fuzzing (Go 1.18+) is used.

### Running Fuzz Tests

```bash
# Run fuzz test for Debian filename parser
go test -fuzz=FuzzParseDebFilename ./internal/cache/ -fuzztime=30s

# Run fuzz test for request ID validation
go test -fuzz=FuzzIsValid ./internal/requestid/ -fuzztime=30s

# Run fuzz test for Packages file parser
go test -fuzz=FuzzParsePackagesFile ./internal/index/ -fuzztime=30s

# Run fuzz test for URL extraction
go test -fuzz=FuzzExtractRepoFromURL ./internal/index/ -fuzztime=30s
go test -fuzz=FuzzExtractPathFromURL ./internal/index/ -fuzztime=30s

# Run fuzz test for request ID generation
go test -fuzz=FuzzGenerate ./internal/requestid/ -fuzztime=30s
```

### Fuzz Test Locations

| Package | File | Tests |
|---------|------|-------|
| `internal/cache` | `parser_fuzz_test.go` | `FuzzParseDebFilename` |
| `internal/index` | `index_fuzz_test.go` | `FuzzParsePackagesFile`, `FuzzExtractRepoFromURL`, `FuzzExtractPathFromURL` |
| `internal/requestid` | `requestid_fuzz_test.go` | `FuzzIsValid`, `FuzzGenerate` |

### Corpus Management

Fuzz corpus is stored in `testdata/fuzz/` directories within each package. Interesting inputs discovered during fuzzing are automatically saved and should be committed to preserve regression coverage.

```bash
# View corpus for a specific fuzz test
ls internal/cache/testdata/fuzz/FuzzParseDebFilename/
```

## Benchmarking

The `debswarm benchmark` command provides performance testing with simulated peers.

### List Available Scenarios

```bash
debswarm benchmark list
```

This shows predefined scenarios with varying file sizes, peer counts, and network conditions.

### Run Benchmarks

```bash
# Run all default benchmark scenarios
debswarm benchmark

# Run a specific scenario
debswarm benchmark --scenario parallel_fast_peers

# Custom benchmark with specific parameters
debswarm benchmark --file-size 200MB --peers 4 --workers 8 --iterations 5
```

### Benchmark Parameters

| Flag | Default | Description |
|------|---------|-------------|
| `--file-size` | varies | File size to test (e.g., `100MB`) |
| `--peers` | 3 | Number of simulated peers |
| `--workers` | 4 | Number of parallel chunk workers |
| `--iterations` | 3 | Number of iterations per test |
| `--scenario` | all | Run specific scenario or `all` |

## Stress Testing

Test concurrent download handling to find bottlenecks and race conditions.

```bash
# Run 10 concurrent downloads of 10MB files (default)
debswarm benchmark stress

# Custom stress test
debswarm benchmark stress --concurrency 50 --file-size 50MB --peers 8
```

### Stress Test Parameters

| Flag | Default | Description |
|------|---------|-------------|
| `--file-size` | `10MB` | File size per download |
| `-n`, `--concurrency` | 10 | Number of concurrent downloads |
| `--peers` | 4 | Number of simulated peers |

### Output

The stress test reports:
- Total duration
- Success/failure counts
- Average, min, max download times
- Aggregate throughput (MB/s)

## Concurrency Tuning

Find the optimal number of parallel chunk workers for your environment.

```bash
# Test worker counts 1, 2, 4, 8 (default max)
debswarm benchmark concurrency

# Test up to 16 workers with larger files
debswarm benchmark concurrency --file-size 200MB --max-workers 16 --peers 8
```

### Concurrency Test Parameters

| Flag | Default | Description |
|------|---------|-------------|
| `--file-size` | `100MB` | File size to test |
| `--peers` | 4 | Number of simulated peers |
| `--max-workers` | 8 | Maximum worker count to test |

### Output

Results show throughput at each concurrency level, helping identify the point of diminishing returns.

## Proxy Load Testing

Test a running debswarm proxy with real HTTP traffic.

### Setup

```bash
# Terminal 1: Start the daemon
debswarm daemon --log-level debug
```

### Run Load Test

```bash
# Terminal 2: Run load test against the proxy
debswarm benchmark proxy \
  --url http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb

# Custom load test parameters
debswarm benchmark proxy \
  --url http://archive.ubuntu.com/ubuntu/pool/main/c/curl/curl_7.88.1_amd64.deb \
  --concurrency 50 \
  --duration 30s \
  --proxy localhost:9977
```

### Proxy Load Test Parameters

| Flag | Default | Description |
|------|---------|-------------|
| `--url` | (required) | Target URL to fetch through proxy |
| `--proxy` | `127.0.0.1:9977` | Proxy address |
| `-n`, `--concurrency` | 10 | Number of concurrent requests |
| `--duration` | `10s` | Test duration |

### Output

The proxy load test reports:
- **Requests/sec**: Request throughput
- **Throughput**: Data transfer rate (MB/s)
- **Latency percentiles**: P50, P95, P99 response times
- **Status codes**: Distribution of HTTP status codes
- **Errors**: Breakdown by error type (timeout, connection refused, etc.)

## Interpreting Results

### Benchmark Scenarios

- **parallel_fast_peers**: Tests ideal conditions with fast, reliable peers
- **mixed_peer_quality**: Tests peer selection with varying peer performance
- **slow_peers_only**: Tests fallback behavior with degraded peers
- **high_latency**: Tests timeout handling with high-latency peers

### Key Metrics

| Metric | Good Value | Indicates |
|--------|------------|-----------|
| Throughput | >50 MB/s | Efficient chunk parallelization |
| P95 latency | <2x P50 | Consistent performance |
| Error rate | <1% | Robust error handling |
| Success rate | >99% | Reliable downloads |

### Common Issues

- **Low throughput with many workers**: Network or peer bandwidth saturation
- **High P99 latency**: Stragglers or slow peers affecting tail latency
- **Connection refused errors**: Proxy not running or wrong address
- **Timeout errors**: Increase `--duration` or check network connectivity
