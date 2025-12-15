# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.2] - 2025-12-15

### Added
- **Health endpoint**: `/health` endpoint on metrics server returns JSON with system health status
  - Checks P2P node, DHT, and cache availability
  - Returns 200 OK when healthy, 503 when not
- **MaxConnections enforcement**: P2P node now enforces `network.max_connections` config using libp2p connection manager
- **MinFreeSpace enforcement**: Cache now respects `cache.min_free_space` config, preventing disk exhaustion

### Changed
- Updated roadmap with completed critical items for 1.0 release

## [0.6.1] - 2025-12-15

### Added
- **Peers table in dashboard**: Connected peers now displayed with score, latency, throughput, and bytes transferred
- **Score color coding**: Visual indicators for peer quality (excellent/good/fair/poor/blacklisted)
- **Verification failures display**: Hash verification failures shown in dashboard overview

### Changed
- New Prometheus metrics:
  - `debswarm_downloads_resumed_total`: Count of resumed downloads
  - `debswarm_chunks_recovered_total`: Chunks recovered from disk
  - `debswarm_errors_total{type}`: Error breakdown by type (timeout, connection, verification)
  - `debswarm_peers_joined_total` / `debswarm_peers_left_total`: Peer churn tracking
  - `debswarm_upload_bytes_per_second` / `debswarm_download_bytes_per_second`: Bandwidth rates

## [0.6.0] - 2025-12-15

### Added
- **Download resume support**: Interrupted chunked downloads can now resume from where they left off
  - Chunks persisted to disk during download
  - Download state tracked in SQLite database
  - Automatic recovery on daemon restart
- **HTTP Range request support**: Mirror fetcher now supports byte-range requests for partial content
- **Configurable chunked download threshold**: `MinChunkedSize` can be configured for testing

### Changed
- Improved test coverage across all packages (73-100% coverage)
- Enhanced CI/CD workflows with better caching and security scanning

### Security
- Fixed unhandled errors in cleanup paths (gosec G104)
- Restricted directory permissions from 0755 to 0750
- Restricted file permissions from 0644 to 0600 for sensitive files
- Proper error handling for Close() and Remove() operations

## [0.5.6] - 2025-12-15

### Fixed
- Fixed DHT lifecycle issues: context leak and channel drain
- Improved DHT shutdown handling

### Changed
- Added comprehensive test coverage for cache, peers, and downloader packages
- Updated documentation for v0.5.x releases

## [0.5.5] - 2025-12-14

### Security
- Added HTTP security headers to dashboard and metrics endpoints (X-Content-Type-Options, X-Frame-Options, Cache-Control, X-XSS-Protection)
- Added Content-Security-Policy for dashboard
- Added response size limit (500MB) to mirror fetcher to prevent memory exhaustion

### Changed
- Extracted SSRF validation to shared `internal/security` package
- Removed redundant `min()` function (use Go 1.21+ built-in)

## [0.5.3] - 2025-12-14

### Security
- **SSRF vulnerability fix**: Block requests to localhost, cloud metadata services, private networks
- Validate URLs match Debian/Ubuntu repository patterns
- Fixed information disclosure in dashboard error messages
- Added documentation for metrics endpoint exposure risks

### Added
- Test coverage for SSRF URL validation

## [0.5.2] - 2025-12-14

### Changed
- Updated libp2p to v0.46.0
- Updated go-sqlite3 to v1.14.32
- Fixed debian cross-compilation for arm64
- Made version dynamic in debian/rules

## [0.5.1] - 2025-12-14

### Changed
- Updated libp2p to v0.45.0 and kad-dht to v0.36.0
- Updated cobra to v1.10.2
- Updated GoReleaser config to v2 format

### Fixed
- Fixed identity key loading for libp2p v0.45+ compatibility (use generic unmarshal)

### Infrastructure
- CI now uses Go 1.24.6

## [0.5.0] - 2025-12-14

### Added
- **Benchmark command**: New `debswarm benchmark` command with simulated peers for performance testing
- New `internal/benchmark` package for reproducible download performance testing

### Changed
- **Go 1.24 required**: Updated minimum Go version from 1.22 to 1.24.6

### Fixed
- Fixed race condition in cache reader tracking (TOCTOU bug)
- Fixed goroutine leak on chunk download failure
- Fixed blacklist flag inconsistency after expiration
- Fixed stream deadline error handling in P2P transfers
- Improved error context in download retry loops
- Added context propagation to rate limiter for proper cancellation
- Added proper goroutine cleanup in announcement worker

## [0.4.0] - 2025-12-13

### Added
- **Multi-repository support**: Proper isolation of package indexes from different repositories (deb.debian.org, archive.ubuntu.com, third-party repos)
- **Auto-indexing**: Packages files are automatically parsed when APT fetches them, enabling P2P for all configured repos
- **Persistent identity**: Stable peer IDs across daemon restarts via Ed25519 key persistence
- **Identity CLI**: New `debswarm identity show` and `debswarm identity regenerate` commands
- **Config security warnings**: Warnings for world-readable config files containing inline PSK
- **Remote monitoring**: New `--metrics-bind` flag to expose dashboard/metrics on non-localhost (for seeding servers)

### Changed
- Daemon now persists identity key to `<data-dir>/identity.key` for stable peer IDs
- Version command now includes "Persistent identity" in feature list

### Security
- **SSRF mitigation**: Proxy now validates URLs to only allow legitimate Debian/Ubuntu mirror requests, blocking access to private networks and cloud metadata endpoints
- **Metrics server hardening**: Added ReadTimeout (10s), WriteTimeout (30s), IdleTimeout (60s) to prevent slowloris attacks
- **CSS injection fix**: Dashboard source values sanitized to prevent CSS class name injection
- Identity keys stored with 0600 permissions
- Config file permission check warns about world-readable files with secrets
- Identity key format includes version header for forward compatibility

## [0.3.0] - 2025-12-13

### Added
- **Bandwidth limiting**: Control upload/download rates with `--max-upload-rate` and `--max-download-rate` CLI flags or config
- **Web dashboard**: Real-time HTML dashboard at `http://localhost:9978/dashboard` showing stats, peers, and transfers
- **Private swarms (PSK)**: Pre-shared key support for isolated networks via `psk_path` config option
- **Peer allowlist**: Restrict connections to specific peer IDs via `peer_allowlist` config option
- **PSK management CLI**: New `debswarm psk generate` and `debswarm psk show` commands
- **Mirror sync mode**: New `--sync` flag for `seed import` removes cached packages not in source directory
- **Download resume infrastructure**: SQLite schema for tracking download state and partial files
- **Rate limit package**: New `internal/ratelimit` package with token bucket rate limiting
- **Dashboard package**: New `internal/dashboard` package with embedded HTML template
- **Connection gater**: `internal/p2p/gater.go` for peer allowlist enforcement

### Changed
- P2P node now supports PSK and connection gating options
- Dashboard auto-refreshes every 5 seconds
- Version command now lists all features

### Security
- PSK files created with 0600 permissions (owner read/write only)
- PSK values never logged, only fingerprints
- Inline PSK config generates a warning recommending file-based PSK

## [0.2.0] - 2025-12-12

### Added
- **Parallel chunked downloads**: Large files (>10MB) are now split into 4MB chunks and downloaded simultaneously from multiple peers
- **Adaptive timeout system**: Network timeouts automatically adjust based on observed performance
- **Peer scoring and selection**: Peers are ranked by latency, throughput, and reliability for optimal selection
- **QUIC transport preference**: QUIC is now preferred over TCP for better NAT traversal and performance
- **Prometheus metrics endpoint**: Full observability at `http://localhost:9978/metrics`
- **JSON stats endpoint**: Quick status check at `http://localhost:9978/stats`
- **Racing strategy**: Small files race P2P vs mirror simultaneously
- **Debian packaging**: Full debian/ directory for building .deb packages
- **GitHub Actions workflows**: CI, release, and Debian package building
- **GoReleaser configuration**: Automated release builds

### Changed
- Improved NAT traversal with QUIC as primary transport
- Better peer selection algorithm with diversity for exploration
- Enhanced logging with structured fields
- Updated systemd service with stricter security settings

### Fixed
- Connection handling edge cases in P2P node
- Cache eviction scoring for better LRU behavior
- Index parsing for edge cases in Packages files

## [0.1.0] - 2025-12-01

### Added
- Initial release
- HTTP proxy for APT integration
- P2P package distribution via libp2p
- Kademlia DHT for peer discovery
- SHA256 verification of all downloads
- Automatic mirror fallback
- mDNS local network discovery
- SQLite-backed content-addressed cache
- TOML configuration file support
- systemd service with security hardening
- CLI with daemon, status, cache, and config commands

### Security
- All P2P downloads verified against signed repository metadata
- Peer blacklisting on hash mismatch
- No trust placed in peers
- Sandboxed systemd service

[Unreleased]: https://github.com/clintcan/debswarm/compare/v0.6.2...HEAD
[0.6.2]: https://github.com/clintcan/debswarm/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/clintcan/debswarm/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/clintcan/debswarm/compare/v0.5.6...v0.6.0
[0.5.6]: https://github.com/clintcan/debswarm/compare/v0.5.5...v0.5.6
[0.5.5]: https://github.com/clintcan/debswarm/compare/v0.5.3...v0.5.5
[0.5.3]: https://github.com/clintcan/debswarm/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/clintcan/debswarm/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/clintcan/debswarm/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/clintcan/debswarm/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/clintcan/debswarm/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/clintcan/debswarm/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/clintcan/debswarm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/clintcan/debswarm/releases/tag/v0.1.0
