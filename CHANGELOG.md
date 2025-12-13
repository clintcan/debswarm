# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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

[Unreleased]: https://github.com/clintcan/debswarm/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/clintcan/debswarm/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/clintcan/debswarm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/clintcan/debswarm/releases/tag/v0.1.0
