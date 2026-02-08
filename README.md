# Debswarm

[![CI](https://github.com/clintcan/debswarm/actions/workflows/ci.yml/badge.svg)](https://github.com/clintcan/debswarm/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/clintcan/debswarm/branch/main/graph/badge.svg)](https://codecov.io/gh/clintcan/debswarm)

**Peer-to-peer package distribution for Debian/Ubuntu**

debswarm accelerates APT package downloads by fetching packages from nearby peers while maintaining security through cryptographic verification. It operates as a transparent HTTP proxy, requiring no changes to your normal `apt` workflow.

## Features

### Core
- **Transparent APT Integration** - Just use `apt install` as usual
- **P2P Package Sharing** - Download from and upload to other debswarm users
- **Multi-Repository Support** - Works with Debian, Ubuntu, and third-party repositories simultaneously
- **HTTPS Repository Support** - HTTP CONNECT tunneling for HTTPS sources (v1.20+)
- **Hash Verification** - All packages verified against signed repository metadata
- **Fleet Coordination** - LAN download deduplication: only one node fetches from WAN, others get it via P2P
- **Mirror Fallback** - Automatic fallback to official mirrors if P2P fails
- **Package Seeding** - Import local .deb files to seed the network
- **Package Rollback** - List and fetch old package versions from cache or P2P peers

### Performance
- **Parallel Chunked Downloads** - Large packages split into 4MB chunks downloaded simultaneously from multiple peers
- **Adaptive Timeouts** - Network timeouts automatically adjust based on observed performance
- **Peer Scoring** - Peers ranked by latency, throughput, and reliability for optimal selection
- **QUIC Transport** - Preferred over TCP for better NAT traversal and multiplexing
- **Racing Strategy** - Small files race P2P vs mirror, first to finish wins
- **Benchmark Testing** - Built-in performance testing with simulated peers

### Privacy & Access Control
- **Bandwidth Limiting** - Control upload/download rates with `--max-upload-rate` and `--max-download-rate`
- **Private Swarms (PSK)** - Create isolated networks using pre-shared keys for corporate deployments
- **Peer Allowlist** - Restrict connections to specific peer IDs
- **Persistent Identity** - Stable peer IDs across restarts with Ed25519 key persistence
- **Download Resume** - Interrupted chunked downloads resume automatically from saved state

### Security
- **SSRF Protection** - Block requests to localhost, cloud metadata, private networks
- **Response Size Limits** - Prevent memory exhaustion from malicious mirrors (500MB max)
- **HTTP Security Headers** - CSP, X-Frame-Options, X-Content-Type-Options on dashboard
- **Error Disclosure Prevention** - Hide internal errors from dashboard users
- **Request Size Limits** - MaxHeaderBytes (1MB) on all HTTP servers

### Monitoring
- **Web Dashboard** - Real-time HTML dashboard at `http://localhost:9978/dashboard`
- **Prometheus Metrics** - Full observability at `http://localhost:9978/metrics`
- **JSON Stats** - Quick status check at `http://localhost:9978/stats`
- **Health Endpoint** - `/health` endpoint for orchestration and monitoring
- **Runtime Profiling** - pprof endpoints at `/debug/pprof/` for production debugging
- **Detailed Logging** - Configurable log levels for debugging

## Quick Start

```bash
# Build
make build

# Install
sudo cp build/debswarm /usr/bin/
sudo cp packaging/debswarm.service /etc/systemd/system/
sudo cp packaging/90debswarm.conf /etc/apt/apt.conf.d/

# Start
sudo systemctl enable --now debswarm

# Use APT normally
sudo apt update
sudo apt install vim

# For HTTPS repositories (v1.20+), add HTTPS proxy config:
echo 'Acquire::https::Proxy "http://127.0.0.1:9977";' | \
  sudo tee -a /etc/apt/apt.conf.d/90debswarm
```

## How It Works

```
┌─────────┐      ┌──────────────┐     ┌─────────────┐
│   APT   │────▶│   debswarm   │────▶│   Peers     │
│         │      │  (proxy)     │     │   (P2P)     │
└─────────┘      └──────┬───────┘     └─────────────┘
                        │                    │
                        │ fallback           │ DHT lookup
                        ▼                    ▼
                 ┌─────────────┐     ┌─────────────┐
                 │   Mirror    │     │  Kademlia   │
                 │  (http)     │     │    DHT      │
                 └─────────────┘     └─────────────┘
```

1. APT requests a package via the local proxy (localhost:9977)
2. If fleet coordination is enabled, consult LAN peers to avoid redundant WAN downloads
3. debswarm looks up the package hash in the Kademlia DHT
4. If peers have it, download using parallel chunks from multiple peers
5. Verify SHA256 hash against signed repository metadata
6. On failure, fall back to official mirror
7. Cache the package and announce to DHT for other peers

## Architecture

```
internal/
├── audit/          # Structured event logging for compliance
├── benchmark/      # Performance testing with simulated peers
├── cache/          # Content-addressed SQLite-backed cache with version metadata
├── config/         # TOML configuration management
├── connectivity/   # Network connectivity monitoring
├── dashboard/      # Real-time web dashboard
├── downloader/     # Parallel chunked download engine with resume support
├── fleet/          # LAN fleet coordination (download deduplication)
├── hashutil/       # Streaming hash computation utilities
├── httpclient/     # HTTP client factory with connection pooling
├── index/          # Debian Packages file parser
├── lifecycle/      # Goroutine lifecycle management
├── metrics/        # Prometheus metrics
├── mirror/         # HTTP mirror client with retry
├── p2p/            # libp2p node with Kademlia DHT, PSK support
├── peers/          # Peer scoring and selection
├── proxy/          # HTTP proxy server for APT
├── ratelimit/      # Bandwidth limiting for uploads/downloads
├── retry/          # Generic retry with exponential backoff
├── security/       # SSRF validation, URL allowlisting
└── timeouts/       # Adaptive timeout management
```

## Configuration

Config file locations (in order of precedence):
1. `--config` flag
2. `/etc/debswarm/config.toml`
3. `~/.config/debswarm/config.toml`

**Quick reference** (see [docs/configuration.md](docs/configuration.md) for full details):

```toml
[network]
listen_port = 4001              # P2P port (UDP/TCP)
proxy_port = 9977               # APT proxy port
max_connections = 100           # Max concurrent P2P connections

[cache]
path = "~/.cache/debswarm"      # Cache directory
max_size = "10GB"               # Maximum cache size
min_free_space = "1GB"          # Minimum free disk space

[transfer]
max_upload_rate = "0"           # 0 = unlimited, or "10MB/s"
max_download_rate = "0"         # 0 = unlimited, or "50MB/s"
max_concurrent_uploads = 20     # Simultaneous uploads
max_concurrent_peer_downloads = 10  # Simultaneous peer downloads
retry_max_attempts = 3          # Auto-retry failed downloads (0 = disabled)
retry_interval = "5m"           # How often to check for failed downloads
retry_max_age = "1h"            # Don't retry downloads older than this

[dht]
provider_ttl = "24h"            # DHT record lifetime
announce_interval = "12h"       # Re-announce interval

[privacy]
enable_mdns = true              # Local network discovery
announce_packages = true        # Share packages with network
psk_path = ""                   # Private swarm key file
peer_allowlist = []             # Restrict to specific peer IDs
peer_blocklist = []             # Block specific peer IDs

[metrics]
port = 9978                     # Metrics/dashboard port (0 = disabled)
bind = "127.0.0.1"              # Bind address (use 0.0.0.0 for remote access)

[logging]
level = "info"                  # debug, info, warn, error
file = ""                       # Log file path (empty = stderr)
```

For complete configuration reference including all options, formats, and examples, see **[Configuration Reference](docs/configuration.md)**.

## CLI Commands

```bash
# Start daemon
debswarm daemon [--proxy-port 9977] [--p2p-port 4001] [--prefer-quic]
debswarm daemon --max-upload-rate 10MB/s --max-download-rate 50MB/s

# Seeding server with remote monitoring
debswarm daemon --metrics-bind 0.0.0.0  # Expose dashboard on all interfaces

# Show status
debswarm status

# Cache management
debswarm cache list         # List cached packages
debswarm cache list --pinned # Show only pinned packages
debswarm cache stats        # Show cache statistics
debswarm cache stats -p 10  # Show stats with top 10 popular packages
debswarm cache popular      # Show most frequently accessed packages
debswarm cache recent       # Show most recently accessed packages
debswarm cache pin <hash>   # Pin a package (prevent eviction)
debswarm cache unpin <hash> # Unpin a package (allow eviction)
debswarm cache verify       # Verify integrity of cached packages
debswarm cache clear        # Clear all cached packages

# Package rollback
debswarm rollback list curl                    # List cached versions of a package
debswarm rollback list curl --all              # Include all architectures
debswarm rollback fetch curl 7.88.1-10         # Fetch specific version from cache
debswarm rollback fetch curl 7.88.1-10 -o /tmp/curl.deb  # Custom output path
debswarm rollback migrate                      # Populate metadata for existing cache

# Seeding packages
debswarm seed import *.deb              # Import .deb files to cache and announce
debswarm seed import -r /path/to/pool/  # Import directory recursively
debswarm seed import -r --sync /mirror/ # Sync with mirror (import new, remove old)
debswarm seed import -r -p 8 /mirror/   # Parallel import with 8 workers
debswarm seed import -r --incremental   # Only import files modified since last sync
debswarm seed import -r --watch /pool/  # Watch directory and auto-import changes
debswarm seed import --dry-run          # Preview changes without making them
debswarm seed list                      # List seeded packages

# Private swarm (PSK) management
debswarm psk generate                   # Generate new PSK file
debswarm psk generate -o /path/to.key   # Generate to specific path
debswarm psk show                       # Show PSK fingerprint from config
debswarm psk show -f /path/to/swarm.key # Show fingerprint of specific file

# Identity management
debswarm identity show                  # Show current peer ID and key location
debswarm identity regenerate            # Generate new identity (requires --force)

# Configuration
debswarm config show        # Display current config
debswarm config init        # Create default config file

# Benchmarking
debswarm benchmark                      # Run default performance benchmark
debswarm benchmark --scenario all       # Run all test scenarios
debswarm benchmark --file-size 50MB     # Test with specific file size
debswarm benchmark --peers 10           # Simulate 10 peers

# Info
debswarm peers              # Show peer information
debswarm version            # Show version and features
```

## Monitoring

### Web Dashboard

Real-time dashboard available at `http://localhost:9978/dashboard`:

- **Overview**: Uptime, P2P ratio, total requests
- **Cache**: Size, count, usage percentage
- **Network**: Peer ID, connected peers, routing table size
- **Transfers**: Active uploads/downloads, recent activity
- **Peers**: Table with scores, latency, throughput per peer

The dashboard auto-refreshes every 5 seconds.

### Prometheus Metrics

Available at `http://localhost:9978/metrics` in Prometheus format:

| Metric | Type | Description |
|--------|------|-------------|
| `debswarm_downloads_total{source}` | Counter | Downloads by source (peer/mirror) |
| `debswarm_bytes_downloaded_total{source}` | Counter | Bytes downloaded by source |
| `debswarm_bytes_uploaded_total{peer}` | Counter | Bytes uploaded per peer |
| `debswarm_cache_hits_total` | Counter | Cache hit count |
| `debswarm_cache_misses_total` | Counter | Cache miss count |
| `debswarm_verification_failures_total` | Counter | Hash verification failures |
| `debswarm_connected_peers` | Gauge | Currently connected peers |
| `debswarm_routing_table_size` | Gauge | DHT routing table size |
| `debswarm_cache_size_bytes` | Gauge | Current cache size |
| `debswarm_cache_count` | Gauge | Cached package count |
| `debswarm_active_downloads` | Gauge | In-progress downloads |
| `debswarm_active_uploads` | Gauge | In-progress uploads |
| `debswarm_chunk_download_seconds` | Histogram | Chunk download duration |
| `debswarm_dht_lookup_seconds` | Histogram | DHT lookup duration |
| `debswarm_peer_latency{peer}` | Histogram | Per-peer latency |

Quick stats JSON at `http://localhost:9978/stats`:
```json
{
  "requests_total": 150,
  "requests_p2p": 120,
  "requests_mirror": 30,
  "bytes_from_p2p": 524288000,
  "bytes_from_mirror": 104857600,
  "p2p_ratio_percent": 83.33
}
```

## Performance Optimizations

### Parallel Chunked Downloads
Files larger than 10MB are split into 4MB chunks and downloaded from multiple peers simultaneously:

```
File: linux-image-6.1.deb (80MB)
├── Chunk 0 [0-4MB]     ← Peer A (fastest)
├── Chunk 1 [4-8MB]     ← Peer B
├── Chunk 2 [8-12MB]    ← Peer A
├── ...
└── Chunk 19 [76-80MB]  ← Peer C
```

### Download Resume
If a chunked download is interrupted (daemon restart, network failure), it automatically resumes:
- Completed chunks are persisted to disk during download
- Download state tracked in SQLite database
- On restart, only missing chunks are fetched
- Partial downloads cleaned up after successful completion

### Peer Scoring
Peers are scored based on weighted factors:
- **Latency (30%)** - Lower is better
- **Throughput (30%)** - Higher is better
- **Reliability (25%)** - Success rate
- **Freshness (15%)** - Recently active peers preferred

### Adaptive Timeouts
Timeouts start at sensible defaults and adjust based on observed network conditions:
- Success → timeout decreases (faster operations expected)
- Failure → timeout increases slightly
- Timeout → timeout doubles (network may be slow)

### QUIC Preference
QUIC transport is preferred over TCP because:
- Better NAT traversal (UDP-based)
- Built-in multiplexing (no head-of-line blocking)
- Faster connection establishment (0-RTT)
- Better performance on lossy networks

## Seeding

Organizations can pre-populate the network by seeding packages from local mirrors or caches:

```bash
# Seed from APT cache (after normal apt operations)
debswarm seed import /var/cache/apt/archives/*.deb

# Seed from a local mirror
debswarm seed import --recursive /var/www/mirror/ubuntu/pool/

# Sync with mirror (import new packages, remove old versions)
debswarm seed import --recursive --sync /var/www/mirror/ubuntu/pool/

# Fast parallel import with 8 workers (great for large mirrors)
debswarm seed import --recursive --parallel 8 /var/www/mirror/ubuntu/pool/

# Incremental sync (only new/modified files since last run)
debswarm seed import --recursive --sync --incremental /var/www/mirror/pool/

# Watch mode: auto-import as debmirror adds new packages
debswarm seed import --recursive --watch /var/www/mirror/ubuntu/pool/

# Preview what would be imported without making changes
debswarm seed import --recursive --sync --dry-run /var/www/mirror/pool/
```

**How seeding works:**
1. Calculate SHA256 hash of each .deb file
2. Store in local cache (skip if already cached)
3. Connect to DHT and announce availability
4. Other peers can now discover and download from you

**Mirror sync mode (`--sync`):**
- Imports new/updated packages (different hash = new file)
- Removes cached packages not found in source directory
- Ideal for keeping cache synchronized with a local mirror
- Run periodically via cron to stay in sync

**Use cases:**
- **Bootstrap a network** - Seed popular packages before users arrive
- **Office/campus deployment** - Pre-seed packages for common software
- **CI/CD caches** - Seed build artifacts for faster deploys
- **Mirror operators** - Run dedicated seeders alongside mirrors

See [docs/bootstrap-node.md](docs/bootstrap-node.md) for running a dedicated seeder.

## Package Rollback

debswarm tracks package metadata (name, version, architecture) for all cached packages, enabling you to list and retrieve old versions:

```bash
# List all cached versions of a package
debswarm rollback list curl
# VERSION               ARCH        SIZE  LAST ACCESSED         HASH
# 7.88.1-10+deb12u5     amd64     500 KB  2024-01-15 10:30      a1b2c3d4e5f6...
# 7.88.1-10             amd64     498 KB  2024-01-10 14:22      f6e5d4c3b2a1...

# Fetch a specific version
debswarm rollback fetch curl 7.88.1-10
# Successfully fetched curl_7.88.1-10_amd64.deb (498 KB)

# Fetch to specific location
debswarm rollback fetch curl 7.88.1-10 --output /tmp/curl-old.deb

# Fetch for different architecture
debswarm rollback fetch curl 7.88.1-10 --arch arm64
```

**Use cases:**
- **Downgrade after problematic update** - Quickly restore a known-good version
- **Testing compatibility** - Install old versions to reproduce issues
- **Offline environments** - Access previously downloaded versions without network

**Note:** After upgrading debswarm, run `debswarm rollback migrate` to populate metadata for existing cached packages.

## Private Swarms

For corporate networks or isolated deployments, debswarm supports private swarms using Pre-Shared Keys (PSK):

```bash
# Generate a new PSK
debswarm psk generate -o /etc/debswarm/swarm.key

# Distribute swarm.key to all nodes in your network
# Then configure each node:
```

```toml
# /etc/debswarm/config.toml
[privacy]
psk_path = "/etc/debswarm/swarm.key"

# Optional: restrict to specific peer IDs
peer_allowlist = [
  "12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz...",
  "12D3KooWBcDeFgHiJkLmNoPqRsTuVwXyZa...",
]

# Optional: block malicious or unwanted peers
peer_blocklist = [
  "12D3KooWMaliciousPeerIdHere...",
]
```

**How it works:**
- Nodes with the same PSK form an isolated network
- Connections to/from nodes without the PSK are rejected
- Peer allowlist provides additional filtering by peer ID
- Peer blocklist blocks specific peers regardless of other settings
- PSK fingerprints can be shared safely to verify key matches

**Use cases:**
- **Corporate networks** - Keep package sharing within your organization
- **Air-gapped environments** - No connection to public DHT
- **Testing/staging** - Separate swarms for different environments

## Security Model

debswarm maintains APT's security guarantees:

1. **Release files** - Always fetched from mirrors (GPG-signed by Debian/Ubuntu)
2. **Package hashes** - Extracted from signed Packages index
3. **P2P downloads** - Verified against expected SHA256 before use
4. **Hash mismatch** - Peer blacklisted, retry with different peer or mirror
5. **No trust required** - Peers cannot serve malicious packages that pass verification

## systemd Service

The included `debswarm.service` has security hardening:

```ini
[Service]
DynamicUser=yes
ProtectSystem=strict
PrivateTmp=yes
NoNewPrivileges=yes
MemoryMax=512M
```

## Building

Requirements:
- Go 1.24+

No C compiler or external dependencies required - uses pure Go SQLite implementation.

```bash
# Simple build
make build

# Build for all architectures (amd64, arm64, armv7)
make build-all

# Run tests
make test

# Run linter
make lint

# Cross-compile for ARM64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o debswarm-arm64 ./cmd/debswarm
```

## Releases

Releases are automated via GitHub Actions. To create a release:

```bash
git tag -a v0.6.1 -m "Release v0.6.1"
git push origin v0.6.1
```

This triggers the release workflow which builds:
- Binary releases for linux/amd64, linux/arm64, linux/armv7
- Debian packages (.deb) for amd64 and arm64

## Troubleshooting

**APT not using proxy:**
```bash
# Check config exists
cat /etc/apt/apt.conf.d/90debswarm.conf

# Should show (for HTTP):
# Acquire::http::Proxy "http://127.0.0.1:9977";

# For HTTPS repositories (v1.20+), also add:
# Acquire::https::Proxy "http://127.0.0.1:9977";
```

**No peers found:**
```bash
# Check DHT status
curl http://localhost:9978/stats | jq .

# Enable debug logging
debswarm daemon --log-level debug
```

**Slow downloads:**
```bash
# Check if falling back to mirror
journalctl -u debswarm -f

# Look for "Falling back to mirror" messages
```

## Documentation

- [Configuration Reference](docs/configuration.md) - Complete guide to all configuration options
- [Security Hardening](docs/security-hardening.md) - Production security recommendations
- [Technical Comparison](docs/comparison.md) - debswarm vs apt-p2p, DebTorrent, apt-cacher-ng
- [Bootstrap Node Setup](docs/bootstrap-node.md) - Running a dedicated seeder/bootstrap node
- [Cache Pre-warming](docs/cache-prewarming.md) - Pre-populate cache for your network
- [Popular Packages](docs/popular-packages.md) - Pre-warm cache with commonly used packages
- [debmirror Integration](docs/debmirror-integration.md) - Use local mirror with debswarm P2P
- [Troubleshooting](docs/troubleshooting.md) - Common issues and solutions
- [Upgrading](docs/upgrading.md) - Version upgrade procedures

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

Apache License 2.0 - see [LICENSE](LICENSE) file.

## Acknowledgments

- [libp2p](https://libp2p.io/) - P2P networking stack
- [Kademlia DHT](https://en.wikipedia.org/wiki/Kademlia) - Distributed hash table
- [APT](https://wiki.debian.org/Apt) - Debian package manager
