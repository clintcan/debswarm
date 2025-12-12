# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Nothing yet

### Changed
- Nothing yet

### Fixed
- Nothing yet

## [0.2.0] - 2024-12-12

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

## [0.1.0] - 2024-12-01

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

[Unreleased]: https://github.com/debswarm/debswarm/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/debswarm/debswarm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/debswarm/debswarm/releases/tag/v0.1.0
