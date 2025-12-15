# Roadmap to 1.0 Release

This document tracks the gaps and improvements needed before a production-ready 1.0 release of debswarm.

## Critical (Must Fix)

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **MaxConnections not enforced** | `internal/p2p/node.go` | Config option exists (`network.max_connections`) but P2P node doesn't limit connections - risk of resource exhaustion | **Done** (v0.6.2) |
| **MinFreeSpace not enforced** | `internal/cache/cache.go` | Cache can fill disk completely, ignoring `cache.min_free_space` setting | **Done** (v0.6.2) |
| **No health endpoint** | `internal/proxy/server.go` | Missing `/health` endpoint for monitoring/orchestration | **Done** (v0.6.2) |

## High Priority

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **Config validation at startup** | `cmd/debswarm/main.go` | Invalid bootstrap peers fail silently during DHT init; should fail fast with clear errors | **Done** (v0.7.0) |
| **Database recovery** | `internal/cache/cache.go` | Corrupted SQLite DB causes unclear failures; needs recovery mechanism or clear error messages | **Done** (v0.7.0) |
| **SIGHUP reload support** | `cmd/debswarm/main.go` | Systemd service declares `ExecReload` but daemon doesn't handle SIGHUP for config reload | **Done** (v0.7.0) |
| **Operational documentation** | `docs/` | Missing troubleshooting guide and upgrade/migration documentation | **Done** (v0.7.0) |

## Medium Priority

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **IPv6 validation** | `internal/p2p/node.go` | Configured in libp2p but not tested on IPv6-only systems | Not started |
| **E2E tests** | `tests/` | Only unit tests with simulated peers; no real APT integration tests | Not started |
| **MaxConcurrentUploads enforcement** | `internal/config/config.go:43-44` | `transfer.max_concurrent_uploads` and `max_concurrent_peer_downloads` not fully enforced at daemon level | **Done** (v0.8.1) |
| **Systemd directory validation** | `cmd/debswarm/main.go` | No pre-flight check that StateDirectory exists and is writable | **Done** (v0.8.0) |

## Low Priority (Post-1.0)

| Issue | Description |
|-------|-------------|
| Request tracing | Add request IDs for correlating multi-hop downloads across logs |
| Per-peer rate limiting | Rate limit individual peers, not just global bandwidth |
| Adaptive rate limiting | Adjust rates based on network conditions |
| Automatic resume retry | Retry failed resume automatically instead of requiring daemon restart |
| Log sanitization review | Audit user-controlled data in logs for injection risks |

## Current Strengths (No Action Needed)

These areas are production-ready:

- **Core P2P functionality**: libp2p integration, DHT discovery, peer connections
- **Security model**: Hash verification against signed Packages index, SSRF protection, PSK support for private swarms
- **Test coverage**: 73-100% across packages
- **Metrics**: Comprehensive Prometheus metrics with dashboard
- **Download resume**: Chunk persistence and recovery works well
- **Peer scoring**: Weighted scoring with latency, throughput, reliability
- **Adaptive timeouts**: Self-tuning based on network conditions
- **Bandwidth limiting**: Upload/download rate limiting functional

## Implementation Notes

### MaxConnections Implementation (Done)
Implemented in `internal/p2p/node.go` using libp2p's connection manager:
- Low water mark at 80% of max connections
- High water mark at max connections (default 100)
- 1 minute grace period before pruning

### MinFreeSpace Implementation (Done)
Implemented in `internal/cache/cache.go`:
- `NewWithMinFreeSpace()` constructor accepts minFreeSpace parameter
- `ensureSpace()` checks disk free space using `syscall.Statfs`
- Returns `ErrInsufficientDiskSpace` if write would violate minimum

### Health Endpoint (Done)
Implemented at `/health` endpoint on metrics server:
- Returns JSON with status, checks, connected_peers, routing_table_size
- Checks P2P node initialization, DHT status, cache availability
- Returns 200 OK when healthy, 503 Service Unavailable when not

### Config Validation at Startup (Done)
Implemented in `internal/config/config.go`:
- `Validate()` method checks all configuration fields
- Validates bootstrap peer multiaddrs using go-multiaddr parser
- Validates port ranges, size formats, rate limits
- Returns `ValidationErrors` with all failures for clear error reporting
- Called in `runDaemon()` before P2P initialization

### Database Recovery (Done)
Implemented in `internal/cache/cache.go`:
- `openDatabaseWithRecovery()` runs SQLite integrity check on open
- `isDatabaseCorrupted()` uses `PRAGMA integrity_check`
- `recoverDatabase()` backs up corrupted DB with timestamp and creates fresh one
- `CheckIntegrity()` method for on-demand integrity verification
- Package files on disk preserved; only metadata needs rebuilding

### SIGHUP Reload Support (Done)
Implemented in `cmd/debswarm/main.go`:
- Added SIGHUP to signal handler alongside SIGINT/SIGTERM
- `reloadConfig()` function loads and validates new configuration
- Reloads rate limits and checks database integrity
- Logs what was reloaded and what requires full restart
- Compatible with systemd `ExecReload=/bin/kill -HUP $MAINPID`

### Operational Documentation (Done)
Created in `docs/`:
- `troubleshooting.md`: Common issues, diagnostics, debug info collection
- `upgrading.md`: Version upgrade procedures, migration notes, rollback

### Systemd Directory Validation (Done)
Implemented in `cmd/debswarm/main.go`:
- `validateDirectories()` runs pre-flight checks before daemon startup
- Verifies cache directory parent exists
- Checks cache and data directories are writable (or can be created)
- Creates test file to verify write permissions
- Fails fast with clear error messages if directories are unusable
- Called after config validation, before component initialization

### MaxConcurrentUploads Enforcement (Done)
Implemented across multiple files:
- `internal/p2p/node.go`: Added `MaxConcurrentUploads` to Config, wired to `canAcceptUpload()`
- `internal/proxy/server.go`: Added `MaxConcurrentPeerDownloads` to Config, passed to downloader
- `cmd/debswarm/main.go`: Wired `cfg.Transfer.MaxConcurrentUploads` and `cfg.Transfer.MaxConcurrentPeerDownloads`
- Both options use sensible defaults (20 uploads, 10 downloads) when not configured

## Version History

- **v0.8.1** (2025-12-15): Medium priority - MaxConcurrentUploads/Downloads enforcement
- **v0.8.0** (2025-12-15): Medium priority - systemd directory validation
- **v0.7.0** (2025-12-15): High priority items - config validation, database recovery, SIGHUP reload, documentation
- **v0.6.2** (2025-12-15): Critical 1.0 blockers - MaxConnections, MinFreeSpace, health endpoint
- **v0.6.1** (2025-12-15): Dashboard peers table, expanded metrics
- **v0.6.0** (2025-12-15): Download resume support, security fixes
- **v0.5.x**: Core functionality, peer scoring, bandwidth limiting, benchmarking

## Target: v1.0.0

All Critical and High Priority items are now resolved. Medium Priority items remain for full production readiness.
