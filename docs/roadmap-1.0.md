# Roadmap to 1.0 Release

This document tracked the gaps and improvements completed for the production-ready 1.0 release of debswarm. **v1.0.0 was released on 2025-12-15.** All items below have been completed.

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
| **IPv6 validation** | `internal/p2p/node_test.go` | Configured in libp2p but not tested on IPv6-only systems | **Done** (v1.12.0) |
| **E2E tests** | `internal/proxy/e2e_test.go` | Integration tests for proxy, cache, index, and P2P flows | **Done** |
| **MaxConcurrentUploads enforcement** | `internal/config/config.go:43-44` | `transfer.max_concurrent_uploads` and `max_concurrent_peer_downloads` not fully enforced at daemon level | **Done** (v0.8.1) |
| **Systemd directory validation** | `cmd/debswarm/main.go` | No pre-flight check that StateDirectory exists and is writable | **Done** (v0.8.0) |

## Low Priority (Post-1.0)

| Issue | Description | Status |
|-------|-------------|--------|
| Request tracing | Add request IDs for correlating multi-hop downloads across logs | Not started |
| Per-peer rate limiting | Rate limit individual peers, not just global bandwidth | **Done** (v1.5.0) |
| Adaptive rate limiting | Adjust rates based on network conditions | **Done** (v1.5.0) |
| Automatic resume retry | Retry failed resume automatically instead of requiring daemon restart | **Done** (v1.4.0) |
| Log sanitization review | Audit user-controlled data in logs for injection risks | **Done** (v1.3.3) |

## Current Strengths (No Action Needed)

These areas are production-ready:

- **Core P2P functionality**: libp2p integration, DHT discovery, peer connections
- **Security model**: Hash verification against signed Packages index, SSRF protection, PSK support for private swarms
- **Test coverage**: 73-100% across packages, CLI smoke tests added
- **Metrics**: Comprehensive Prometheus metrics with dashboard
- **Download resume**: Chunk persistence and recovery works well
- **Peer scoring**: Weighted scoring with latency, throughput, reliability
- **Adaptive timeouts**: Self-tuning based on network conditions
- **Bandwidth limiting**: Upload/download rate limiting functional
- **Runtime profiling**: pprof endpoints at `/debug/pprof/` on metrics server
- **HTTP hardening**: MaxHeaderBytes limits on all HTTP servers

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

### E2E Tests (Done)
Implemented in `internal/proxy/e2e_test.go`:
- `TestE2E_ProxyMirrorFallback`: Tests mock mirror serving Packages index and .deb files
- `TestE2E_CacheHit`: Tests that cached packages are served without hitting the mirror
- `TestE2E_IndexAutoPopulation`: Tests Packages file parsing and SHA256/path lookups
- `TestE2E_TwoNodeP2PTransfer`: Creates two P2P nodes, seeds package on one, downloads on other
- `TestE2E_HashVerification`: Tests that packages with wrong hashes are rejected

### Log Sanitization (Done)
Implemented in `internal/sanitize/sanitize.go`:
- `sanitize.String()` escapes newlines, carriage returns, tabs, and control characters
- `sanitize.URL()`, `sanitize.Filename()`, `sanitize.Path()` for semantic clarity
- Truncates strings over 500 characters to prevent log bloat
- Applied to user-controlled data in proxy/server.go, cache.go, index.go, p2p/node.go
- Prevents log injection attacks where malicious URLs/filenames could create fake log entries

### Automatic Resume Retry (Done)
Implemented background worker to automatically retry failed downloads:
- `internal/downloader/state.go`: Added `retry_count` column and methods:
  - `GetRetryableDownloads(maxRetries, maxAge)` finds failed downloads eligible for retry
  - `MarkForRetry(hash)` resets status to pending and increments retry count
  - `IncrementRetryCount(hash)` for manual retry count updates
- `internal/proxy/server.go`: Added `retryWorker()` background goroutine
  - Runs at configurable interval (default 5 minutes)
  - Checks for failed downloads within retry limits and age constraints
  - Triggers re-download while preserving already-completed chunks
- `internal/config/config.go`: Added configuration options:
  - `transfer.retry_max_attempts`: Maximum retry attempts per download (default 3, 0 to disable)
  - `transfer.retry_interval`: How often to check for failed downloads (default "5m")
  - `transfer.retry_max_age`: Don't retry downloads older than this (default "1h")
- Clean shutdown: Retry worker respects context cancellation for graceful stop

### Per-Peer Rate Limiting (Done)
Implemented per-peer bandwidth limiting to prevent single peer monopolization:
- `internal/ratelimit/peer_limiter.go`: New `PeerLimiterManager` with lazy limiter creation
  - Tracks rate limiters per peer.ID with automatic idle cleanup (30s timeout)
  - `ReaderContext()` and `WriterContext()` return composed readers/writers
  - `ComposedLimitedReader/Writer` applies both global and per-peer limits
  - Auto-calculation mode: `global_limit / expected_peers` with configurable minimum
- `internal/config/config.go`: Added configuration options:
  - `transfer.per_peer_upload_rate`: "auto", "0" (disabled), or specific rate
  - `transfer.per_peer_download_rate`: "auto", "0" (disabled), or specific rate
  - `transfer.expected_peers`: For auto-calculation (default 10)
- `internal/p2p/node.go`: Integrated per-peer limiters into transfer handlers
- `internal/metrics/metrics.go`: Added `PeerRateLimiters` gauge and `PeerRateLimitCurrent` gauge vector

### Adaptive Rate Limiting (Done)
Implemented adaptive rate adjustment based on peer performance metrics:
- Integrated with existing peer scoring system (`internal/peers/scorer.go`)
- Moderate adjustment style: ±50% based on score (0.5x to 1.5x multiplier)
- Congestion detection: reduces rates when latency exceeds 500ms threshold
- Background adaptive loop recalculates rates every 10 seconds
- `internal/config/config.go`: Added configuration options:
  - `transfer.adaptive_rate_limiting`: Enable/disable (default: enabled when per-peer is active)
  - `transfer.adaptive_min_rate`: Floor rate (default "100KB/s")
  - `transfer.adaptive_max_boost`: Maximum multiplier (default 1.5)
- `internal/metrics/metrics.go`: Added `AdaptiveAdjustments` counter for tracking boost/reduce events

### IPv6 Validation (Done)
Implemented in `internal/p2p/node_test.go`:
- `TestNew_IPv6Addresses`: Verifies nodes listen on IPv6 addresses (TCP and QUIC)
- `TestNew_IPv6WithQUIC`: Verifies IPv6 QUIC addresses when QUIC is preferred
- `TestNode_TwoNodes_ConnectIPv6`: Tests two nodes connecting over IPv6 only
- `TestNode_Download_IPv6`: Tests full content transfer over IPv6

## Version History

- **v1.12.1** (2026-01-28): Fleet coordinator message response completion and race fixes
- **v1.12.0** (2026-01-28): Medium priority - IPv6 validation tests in CI
- **v1.11.5** (2026-01-28): Security - Resolve all gosec high-severity integer overflow warnings
- **v1.11.4** (2026-01-28): Security - GitHub Actions hardening (script injection fix, SHA-pinning)
- **v1.5.0** (2025-12-17): Low priority - Per-peer rate limiting and adaptive rate limiting
- **v1.4.0** (2025-12-17): Low priority - Automatic resume retry for failed downloads
- **v1.0.0** (2025-12-15): Initial stable release
- **v0.8.1** (2025-12-15): Medium priority - MaxConcurrentUploads/Downloads enforcement
- **v0.8.0** (2025-12-15): Medium priority - systemd directory validation
- **v0.7.0** (2025-12-15): High priority items - config validation, database recovery, SIGHUP reload, documentation
- **v0.6.2** (2025-12-15): Critical 1.0 blockers - MaxConnections, MinFreeSpace, health endpoint
- **v0.6.1** (2025-12-15): Dashboard peers table, expanded metrics
- **v0.6.0** (2025-12-15): Download resume support, security fixes
- **v0.5.x**: Core functionality, peer scoring, bandwidth limiting, benchmarking

## Status

**v1.0.0 Released** - All Critical, High Priority, and Medium Priority roadmap items are complete.

Remaining post-1.0 work:
- Request tracing (low priority)
- Fleet coordinator completion - **Done** (v1.12.1)
