# debswarm

**Peer-to-peer package distribution for Debian/Ubuntu**

debswarm accelerates APT package downloads by fetching packages from nearby peers while maintaining security through cryptographic verification. It operates as a transparent HTTP proxy, requiring no changes to your normal `apt` workflow.

## Features

### Core
- **Transparent APT Integration** - Just use `apt install` as usual
- **P2P Package Sharing** - Download from and upload to other debswarm users
- **Hash Verification** - All packages verified against signed repository metadata
- **Mirror Fallback** - Automatic fallback to official mirrors if P2P fails
- **Package Seeding** - Import local .deb files to seed the network

### Performance (v0.2.0)
- **Parallel Chunked Downloads** - Large packages split into 4MB chunks downloaded simultaneously from multiple peers
- **Adaptive Timeouts** - Network timeouts automatically adjust based on observed performance
- **Peer Scoring** - Peers ranked by latency, throughput, and reliability for optimal selection
- **QUIC Transport** - Preferred over TCP for better NAT traversal and multiplexing
- **Racing Strategy** - Small files race P2P vs mirror, first to finish wins

### Monitoring
- **Prometheus Metrics** - Full observability at `http://localhost:9978/metrics`
- **JSON Stats** - Quick status check at `http://localhost:9978/stats`
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
```

## How It Works

```
┌─────────┐     ┌──────────────┐     ┌─────────────┐
│   APT   │────▶│   debswarm   │────▶│   Peers     │
│         │     │  (proxy)     │     │   (P2P)     │
└─────────┘     └──────┬───────┘     └─────────────┘
                       │                    │
                       │ fallback           │ DHT lookup
                       ▼                    ▼
                ┌─────────────┐     ┌─────────────┐
                │   Mirror    │     │  Kademlia   │
                │  (http)     │     │    DHT      │
                └─────────────┘     └─────────────┘
```

1. APT requests a package via the local proxy (localhost:9977)
2. debswarm looks up the package hash in the Kademlia DHT
3. If peers have it, download using parallel chunks from multiple peers
4. Verify SHA256 hash against signed repository metadata
5. On failure, fall back to official mirror
6. Cache the package and announce to DHT for other peers

## Architecture

```
internal/
├── cache/          # Content-addressed SQLite-backed cache
├── config/         # TOML configuration management
├── downloader/     # Parallel chunked download engine
├── index/          # Debian Packages file parser
├── metrics/        # Prometheus metrics
├── mirror/         # HTTP mirror client with retry
├── p2p/            # libp2p node with Kademlia DHT
├── peers/          # Peer scoring and selection
├── proxy/          # HTTP proxy server for APT
└── timeouts/       # Adaptive timeout management
```

## Configuration

Config file locations (in order of precedence):
1. `--config` flag
2. `/etc/debswarm/config.toml`
3. `~/.config/debswarm/config.toml`

```toml
[network]
listen_port = 4001          # P2P port
proxy_port = 9977           # APT proxy port
max_connections = 100

# Bootstrap peers (libp2p defaults)
bootstrap_peers = [
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
]

[cache]
path = "~/.cache/debswarm"
max_size = "10GB"
min_free_space = "1GB"

[transfer]
max_upload_rate = "0"       # 0 = unlimited
max_concurrent_uploads = 20
max_concurrent_downloads = 8
chunk_size = "4MB"          # For parallel downloads

[dht]
provider_ttl = "24h"
announce_interval = "12h"

[privacy]
enable_mdns = true          # Local network discovery
announce_packages = true    # Share with network

[logging]
level = "info"              # debug, info, warn, error
file = ""                   # Empty = stderr
```

## CLI Commands

```bash
# Start daemon
debswarm daemon [--proxy-port 9977] [--p2p-port 4001] [--prefer-quic]

# Show status
debswarm status

# Cache management
debswarm cache list         # List cached packages
debswarm cache stats        # Show cache statistics
debswarm cache clear        # Clear all cached packages

# Seeding packages
debswarm seed import *.deb              # Import .deb files to cache and announce
debswarm seed import -r /path/to/pool/  # Import directory recursively
debswarm seed import --announce=false   # Import without announcing to DHT
debswarm seed list                      # List seeded packages

# Configuration
debswarm config show        # Display current config
debswarm config init        # Create default config file

# Info
debswarm peers              # Show peer information
debswarm version            # Show version and features
```

## Metrics

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

# Seed specific packages without announcing (cache only)
debswarm seed import --announce=false package.deb
```

**How seeding works:**
1. Calculate SHA256 hash of each .deb file
2. Store in local cache
3. Connect to DHT and announce availability
4. Other peers can now discover and download from you

**Use cases:**
- **Bootstrap a network** - Seed popular packages before users arrive
- **Office/campus deployment** - Pre-seed packages for common software
- **CI/CD caches** - Seed build artifacts for faster deploys
- **Mirror operators** - Run dedicated seeders alongside mirrors

See [docs/bootstrap-node.md](docs/bootstrap-node.md) for running a dedicated seeder.

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
- Go 1.22+
- GCC (for SQLite)
- libsqlite3-dev

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
git tag -a v0.2.1 -m "Release v0.2.1"
git push origin v0.2.1
```

This triggers the release workflow which builds:
- Binary releases for linux/amd64, linux/arm64, linux/armv7
- Debian packages (.deb) for amd64 and arm64

## Troubleshooting

**APT not using proxy:**
```bash
# Check config exists
cat /etc/apt/apt.conf.d/90debswarm.conf

# Should show:
# Acquire::http::Proxy "http://127.0.0.1:9977";
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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

MIT License - see LICENSE file.

## Acknowledgments

- [libp2p](https://libp2p.io/) - P2P networking stack
- [Kademlia DHT](https://en.wikipedia.org/wiki/Kademlia) - Distributed hash table
- [APT](https://wiki.debian.org/Apt) - Debian package manager
