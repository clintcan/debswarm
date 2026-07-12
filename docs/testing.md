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

## Docker Soak Testing

Run the real daemon on Linux under sustained, real APT traffic — the highest-fidelity pre-release check. A soak exercises paths unit and integration tests cannot: a real pipelining APT client, memory/goroutine stability over time, and P2P transfer between multiple nodes. (A pre-1.30 soak is what surfaced the APT-pipelining index hang fixed by switching the proxy to `ReadHeaderTimeout`.)

### Build the image

Reuse the cross-compiled Linux binary (`make build-all` produces `build/debswarm-linux-amd64`) in a slim image — `ca-certificates` is required for HTTPS upstream/mirror fetches, `curl` for in-container monitoring:

```dockerfile
# Dockerfile (build context: a dir containing the linux binary as ./debswarm)
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY debswarm /usr/bin/debswarm
RUN chmod +x /usr/bin/debswarm
ENTRYPOINT ["debswarm"]
```

```bash
cp build/debswarm-linux-amd64 ./soak/debswarm
docker build -t debswarm:soak ./soak
docker network create debswarm-soak
```

### Single-node soak (proxy, cache, upstream fetch)

Run the daemon and drive it with a real APT client in the same container (APT reaches the proxy over `localhost`; the proxy binds `127.0.0.1`). Monitor via `docker exec` — no host-port publishing needed.

```bash
docker run -d --name node1 --network debswarm-soak debswarm:soak \
  daemon --metrics-bind 0.0.0.0 -l info

# Point APT at the local proxy, then update + download in a loop
docker exec node1 sh -c 'echo "Acquire::http::Proxy \"http://127.0.0.1:9977\";" > /etc/apt/apt.conf.d/00proxy'
docker exec -d node1 sh -c \
  'for i in $(seq 1 80); do apt-get clean; \
     apt-get install -y --download-only --reinstall hello nano wget curl >/dev/null 2>&1; \
   done'

# Sample stats + memory over time (watch for upward memory drift = leak)
docker stats --no-stream --format '{{.MemUsage}}' node1
docker exec node1 curl -s http://127.0.0.1:9978/stats   # requests_*, cache_hits, cache_count
```

A healthy run shows `cache_hits` climbing (cache-serve path), stable (non-growing) memory, and no error logs.

### Two-node P2P/fleet soak

Verifies packages actually transfer over P2P. This needs **fleet enabled** (LAN discovery + dedup; off by default — the public DHT alone is unreliable between NAT'd nodes). Bake a config into the image so the daemon auto-detects it at `/etc/debswarm/config.toml`:

```bash
printf '[fleet]\nenabled = true\n[privacy]\nenable_mdns = true\nannounce_packages = true\n' > ./soak/config.toml
# Dockerfile.fleet: FROM debswarm:soak / COPY config.toml /etc/debswarm/config.toml
docker build -f ./soak/Dockerfile.fleet -t debswarm:soak-fleet ./soak

docker run -d --name node1 --network debswarm-soak debswarm:soak-fleet daemon --metrics-bind 0.0.0.0
docker run -d --name node2 --network debswarm-soak debswarm:soak-fleet daemon --metrics-bind 0.0.0.0
# The two daemons discover each other via mDNS (works on a Docker bridge network).

# Populate node1's cache, then request the same packages on node2:
# node2 should pull them from node1 over P2P — check requests_p2p / bytes_from_p2p:
docker exec node2 curl -s http://127.0.0.1:9978/stats   # expect requests_p2p > 0, "fleet":{"PeerCount":1}
```

### Notes and gotchas

- **Do not pass `--config /etc/...`**: rely on auto-detection of `/etc/debswarm/config.toml`. On Git Bash / MSYS (Windows), a leading-slash argument like `/etc/debswarm/config.toml` is silently rewritten to a Windows path (e.g. `C:/Program Files/Git/etc/...`) before Docker sees it; use `MSYS_NO_PATHCONV=1`, a `//etc/...` prefix, or auto-detection.
- **Host-port publishing may fail on Windows** (`bind: An attempt was made to access a socket in a way forbidden`) because Docker Desktop reserves port ranges — monitor via `docker exec` + `curl` instead of `-p`.
- APT pipelines by default; test with default settings (do **not** set `Acquire::http::Pipeline-Depth=0`) so the real client behavior is exercised.
- Packages are only cached/P2P-shared once their SHA256 is known from a parsed index, so run `apt-get update` through the proxy before expecting cache/P2P activity.

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
