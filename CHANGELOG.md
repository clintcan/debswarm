# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.17.2] - 2026-01-31

### Fixed
- **APT Acquire-By-Hash support**: Fixed cache not filling when APT uses by-hash URLs (default since Debian 9/Ubuntu 16.04). URLs like `/binary-amd64/by-hash/SHA256/xxx` were not recognized as Packages files, causing the hash index to remain empty and preventing package caching.

## [1.17.1] - 2026-01-31

### Fixed
- **Cache not filling**: Fixed race condition where index parsing was asynchronous, causing package hash lookups to fail when APT requests arrived before parsing completed
- **Benchmark 0-byte file size**: Fixed benchmark command running with 0-byte files when `--file-size` flag was not provided
- **Benchmark hash mismatch**: Fixed hash verification for chunked downloads by reading from `FilePath` when `Data` is nil

## [1.17.0] - 2026-01-29

### Added
- **Fuzz testing for parsing functions**: Native Go fuzz tests for robustness
  - `FuzzParseDebFilename` in `internal/cache/parser_fuzz_test.go`
  - `FuzzParsePackagesFile`, `FuzzExtractRepoFromURL`, `FuzzExtractPathFromURL` in `internal/index/index_fuzz_test.go`
  - `FuzzIsValid`, `FuzzGenerate` in `internal/requestid/requestid_fuzz_test.go`
- **Load testing CLI commands**: Performance testing utilities
  - `debswarm benchmark stress` - Concurrent download stress testing
  - `debswarm benchmark concurrency` - Find optimal worker count
  - `debswarm benchmark proxy` - HTTP proxy load testing with latency percentiles (P50/P95/P99)
  - New `internal/benchmark/proxy_loadtest.go` for proxy load testing

### Documentation
- New `docs/testing.md` guide covering fuzz testing, benchmarking, and load testing

## [1.16.0] - 2026-01-29

### Added
- **Request tracing with correlation IDs**: End-to-end request tracking for debugging multi-hop P2P downloads
  - New `requestid` package for ID generation and context utilities
  - Generate time-sortable 24-char hex IDs (8 bytes timestamp + 4 bytes random)
  - Propagate request ID through context to all handlers
  - Include `requestID` field in all log messages for a request
  - Add `RequestID` field to audit events with `WithRequestID()` chaining method
  - Return `X-Request-ID` header in HTTP responses
  - Preserve valid incoming `X-Request-ID` headers from clients

## [1.15.0] - 2026-01-29

### Added
- **Package rollback commands**: List and fetch old package versions from cache or P2P peers
  - `debswarm rollback list <package>` - Show all cached versions of a package
  - `debswarm rollback fetch <package> <version>` - Download specific version from cache
  - `debswarm rollback migrate` - Populate metadata for existing cache entries
  - Cache schema extended with `package_name`, `package_version`, `architecture` columns
  - New `ParseDebFilename()` utility to extract metadata from Debian package filenames
  - Useful for downgrading after problematic updates or testing compatibility

### Fixed
- **Double-close in proxy shutdown**: Fixed verifier being closed twice during proxy server shutdown
- **Metrics formatting**: Corrected gofmt alignment in metrics.go

### Changed
- **Audit events**: Added dedicated `ProviderCount` field to audit Event struct (previously embedded in details)

### Documentation
- Added multi-source verification section to security hardening guide

## [1.14.0] - 2026-01-28

### Added
- **Multi-source verification**: Asynchronous verification of downloaded packages by querying the DHT for other providers
  - Near-zero bandwidth overhead - only queries DHT for provider list, doesn't re-download data
  - Non-blocking verification runs after successful download and caching
  - Configurable minimum providers for "verified" status (default: 2)
  - New audit events: `multi_source_verified`, `multi_source_unverified`
  - New metrics: `debswarm_verification_results`, `debswarm_verification_providers`, `debswarm_verification_duration`
  - Part of v2.0 Security & Resilience roadmap

## [1.13.0] - 2026-01-28

### Added
- **Configurable NAT traversal**: New configuration options for circuit relay and hole punching
  - `network.enable_relay`: Use circuit relays to reach NAT'd peers (default: true)
  - `network.enable_hole_punching`: Enable direct NAT hole punching (default: true)
  - Both features were already enabled but are now configurable and documented
  - Client-only relay mode - uses public relays but doesn't act as one

### Documentation
- Added NAT Traversal section to configuration.md explaining relay and hole punching options

## [1.12.1] - 2026-01-28

### Fixed
- **Fleet coordination message responses**: Complete implementation of WantPackage handler responses
  - Add `MessageSender` interface for coordinator to send responses back to peers
  - Implement `MsgHavePackage` response when we have the requested package cached
  - Implement `MsgFetching` response when we're currently downloading from WAN
- **Data race in handleWantPackage**: Fixed race condition reading `state.Fetcher` outside lock
- **Lock contention in StartProgressBroadcaster**: Fixed network I/O while holding coordinator lock
  - Now collects progress data under lock, broadcasts outside lock
  - Prevents lock starvation and potential deadlocks during slow network conditions

### Added
- Tests for `handleWantPackage` response handling (`TestHandleWantPackageWithCache`, `TestHandleWantPackageWhileFetching`)

## [1.12.0] - 2026-01-28

### Added
- **IPv6 validation in CI**: Added comprehensive IPv6 connectivity tests to validate P2P functionality
  - `TestNew_IPv6Addresses`: Verifies nodes listen on IPv6 addresses (TCP and QUIC)
  - `TestNew_IPv6WithQUIC`: Verifies IPv6 QUIC addresses when QUIC is preferred
  - `TestNode_TwoNodes_ConnectIPv6`: Tests two nodes connecting over IPv6 only
  - `TestNode_Download_IPv6`: Tests full content transfer over IPv6
- Completes all Medium Priority roadmap items

## [1.11.5] - 2026-01-28

### Security
- **Integer overflow fixes**: Resolve all gosec high-severity integer overflow warnings (G115)
  - Add overflow validation for uint64/int64 conversions in P2P transfer protocol
  - Add bounds checking before int-to-uint16 conversion in fleet messages
  - Add explicit bitmask for int64-to-uint32 truncation in fleet coordinator
  - Add nosec annotations for intentional conversions (benchmark math/rand, diskspace)

## [1.11.4] - 2026-01-28

### Security
- **GitHub Actions hardening**: Fix security vulnerabilities in CI/CD workflows
  - Fix script injection vulnerability in release.yml workflow_dispatch input
  - Add high-severity check to gosec scanner (fail CI on HIGH findings)
  - SHA-pin all GitHub Actions to prevent supply chain attacks
    - actions/checkout@v4.3.0
    - actions/setup-go@v5.6.0
    - actions/upload-artifact@v6.0.0
    - codecov/codecov-action@v5.5.2
    - golangci/golangci-lint-action@v7.0.1
    - goreleaser/goreleaser-action@v6.4.0

## [1.11.3] - 2025-12-31

### Added
- **Performance benchmarks**: Added benchmark tests for downloader buffer operations
- **GoDoc examples**: Added example_test.go files for internal libraries
  - `internal/retry/` - Examples for Do(), NonRetryable(), backoff strategies
  - `internal/lifecycle/` - Examples for Manager, Go(), GoN(), RunTicker()
  - `internal/hashutil/` - Examples for HashingWriter, HashingReader, Verify()
  - `internal/httpclient/` - Examples for New(), Default(), WithTimeout()

### Changed
- **Performance**: Added buffer pooling for chunk assembly in downloader
  - Reuses 4MB buffers via sync.Pool instead of allocating per chunk
  - 55,000x faster buffer operations, zero allocations in hot path
  - Reduces GC pressure during large file downloads
- **Error handling**: Standardized error message patterns across codebase
  - Lowercase error messages per Go conventions (e.g., "http 404" not "HTTP 404")
  - Fixed error wrapping in downloader to use %w for proper error chain support

## [1.11.2] - 2025-12-29

### Changed
- **Internal refactoring**: Extracted common patterns into reusable libraries
  - `internal/retry/` - Generic retry with exponential/linear/constant backoff (Go generics)
  - `internal/lifecycle/` - Goroutine lifecycle management with context + waitgroup
  - `internal/hashutil/` - Streaming hash computation (HashingWriter/HashingReader)
  - `internal/httpclient/` - HTTP client factory with connection pooling and sensible defaults
- Refactored `mirror/fetcher.go` to use `retry.Do()` instead of inline retry loops
- Refactored `downloader/downloader.go` to use `retry.Do()` for chunk retries
- Refactored `ratelimit/peer_limiter.go` to use `lifecycle.Manager` for goroutine management
- Refactored `proxy/server.go` announcement worker to use `lifecycle.Manager`
- Refactored `cache/cache.go` to use `hashutil.HashingWriter` for hash computation
- Refactored `mirror/fetcher.go`, `index/index.go`, `connectivity/monitor.go` to use `httpclient`

## [1.11.1] - 2025-12-23

### Fixed
- Updated `packaging/config.system.toml` with missing configuration sections
  - Added connectivity_mode comment
  - Added per-peer rate limiting comments
  - Added audit logging section (v1.8+)
  - Added scheduler section (v1.9+)
  - Added fleet coordination section (v1.9+)

## [1.11.0] - 2025-12-23

### Added
- **Parallel Imports**: New `--parallel N` flag for `seed import` command
  - Process multiple .deb files concurrently (up to 32 workers)
  - Dramatically faster imports for large mirrors (8x+ speedup typical)
- **Dry-Run Mode**: New `--dry-run` flag to preview changes without making them
  - Shows what would be imported, skipped, and removed
  - Essential for validating sync operations before execution
- **Incremental Sync**: New `--incremental` flag for faster daily syncs
  - Only processes files modified since last successful sync
  - Tracks sync state per source path
  - Reduces sync time from hours to seconds for large mirrors
- **Watch Mode**: New `--watch` flag for continuous monitoring
  - Automatically imports new .deb files as they appear
  - Uses filesystem notifications (fsnotify) for efficiency
  - Debounces rapid changes to batch imports
  - Eliminates need for cron-based polling
- **Progress Bar**: New `--progress` flag for large imports
  - Shows visual progress bar with statistics
  - Displays imported, skipped, and failed counts in real-time

### Changed
- `seed import` now uses worker pool pattern for better resource utilization
- Improved error handling with graceful degradation in watch mode

### Dependencies
- Added `github.com/fsnotify/fsnotify` v1.9.0 for watch mode

## [1.10.0] - 2025-12-23

### Added
- **Cache Verification Command**: `debswarm cache verify` to check integrity of all cached packages
  - Computes SHA256 hash of each cached file and compares against expected value
  - Reports missing, corrupted, and verified packages
  - Useful for incident response and cache integrity auditing
- **Peer Blocklist**: New `privacy.peer_blocklist` configuration option
  - Block specific peer IDs from connecting
  - Blocklist is checked before allowlist (blocked peers always rejected)
  - Useful for blocking malicious or misbehaving peers
  - New gater methods: `BlockPeer()`, `UnblockPeer()`, `ListBlockedPeers()`

### Fixed
- **Documentation**: Corrected security-hardening.md claims that didn't match implementation
  - Removed non-existent CSP header from security headers list
  - Removed non-existent granular audit logging fields (`log_downloads`, `log_uploads`, etc.)

### Configuration
New option in `config.toml`:
```toml
[privacy]
# Block specific peers (connections always rejected)
peer_blocklist = [
  "12D3KooWMaliciousPeerIdHere...",
]
```

## [1.9.0] - 2025-12-21

### Added
- **Scheduled Sync Windows**: Time-based download scheduling with rate limiting
  - Configure sync windows for off-peak downloading (e.g., nights, weekends)
  - Rate limiting outside windows (default 100KB/s) instead of blocking
  - Security updates always get full speed regardless of schedule
  - Timezone-aware scheduling with flexible day patterns (weekday, weekend, specific days)
  - New `internal/scheduler/` package with full test coverage
- **Fleet Coordination**: LAN fleet coordination for download deduplication
  - Peers coordinate to avoid redundant WAN downloads of the same package
  - Election-based fetcher selection using random nonces
  - Progress broadcasting across fleet peers
  - Automatic fallback if coordination fails
  - New `internal/fleet/` package with protocol handler
- **New Prometheus metrics**:
  - `debswarm_scheduler_window_active` - 1 if currently in sync window
  - `debswarm_scheduler_current_rate_bytes` - Current rate limit in bytes/sec
  - `debswarm_scheduler_urgent_downloads_total` - Security updates at full speed
  - `debswarm_fleet_peers` - Number of fleet peers
  - `debswarm_fleet_wan_avoided_total` - Downloads served from fleet vs WAN
  - `debswarm_fleet_bytes_avoided_total` - Bytes saved by fleet coordination
  - `debswarm_fleet_in_flight` - Current in-flight fleet downloads
- **P2P node enhancements**: `GetMDNSPeers()` and `Host()` methods for fleet integration

### Changed
- Updated golangci-lint config for v2 format
- CI now uses golangci-lint-action v7 with golangci-lint v2.7.2

### Fixed
- Fix errcheck lint issues with explicit error discarding in defer statements

### Configuration
New options in `config.toml`:
```toml
[scheduler]
enabled = true
timezone = "America/New_York"
outside_window_rate = "100KB/s"
inside_window_rate = "unlimited"
urgent_always_full_speed = true

[[scheduler.windows]]
days = ["weekday"]
start_time = "22:00"
end_time = "06:00"

[[scheduler.windows]]
days = ["saturday", "sunday"]
start_time = "00:00"
end_time = "23:59"

[fleet]
enabled = true
claim_timeout = "5s"
max_wait_time = "5m"
allow_concurrent = 1
refresh_interval = "1s"
```

## [1.8.0] - 2025-12-18

### Added
- **LAN Peer Priority**: mDNS-discovered peers now receive a scoring boost for proximity
  - New `WeightProximity` factor (15%) in peer scoring algorithm
  - mDNS peers get proximity score of 1.0, DHT peers get 0.3
  - Unknown mDNS peers start at score 0.65 (vs 0.5 for DHT peers)
  - Peers discovered via mDNS are automatically marked for priority selection
- **Offline-First Mode**: Automatic detection and graceful fallback when internet is unavailable
  - Three connectivity modes: `online` (full), `lan_only` (mDNS peers only), `offline` (cache only)
  - Configurable `connectivity_mode`: "auto" (default), "lan_only", or "online_only"
  - Background connectivity monitoring with configurable check interval
  - Health endpoint now includes `connectivity_mode` field
  - New `internal/connectivity/` package with full test coverage
- **Audit Log Export**: Structured JSON audit logging for compliance and monitoring
  - Events logged: download complete/failed, upload complete, cache hits, verification failures, peer blacklisting
  - JSON Lines format for easy parsing by log analysis tools (jq, ELK, Splunk)
  - Automatic file rotation with configurable max size and backup count
  - New `internal/audit/` package with Logger interface and NoopLogger for disabled state
  - Configurable via `[logging.audit]` section in config.toml

### Changed
- Peer scoring weights adjusted: Latency 25%, Throughput 25%, Reliability 20%, Freshness 15%, Proximity 15%
  - (Previously: Latency 30%, Throughput 30%, Reliability 25%, Freshness 15%)

### Configuration
New options in `config.toml`:
```toml
[network]
connectivity_mode = "auto"           # "auto", "lan_only", "online_only"
connectivity_check_interval = "30s"
# connectivity_check_url = "https://deb.debian.org"

[logging.audit]
enabled = false
path = "/var/log/debswarm/audit.json"
max_size_mb = 100
max_backups = 5
```

## [1.7.0] - 2025-12-18

### Changed
- **Streaming downloads**: Large file downloads (≥10MB) now stream directly to disk instead of buffering in memory
  - Eliminates memory exhaustion for large packages (500MB+ files no longer allocate 500MB RAM)
  - Chunks written to assembly file on disk, verified by streaming hash computation
  - Memory usage for chunked downloads reduced from ~file_size to ~32MB (chunks in flight only)
  - Racing strategy for small files (<10MB) unchanged for best latency
- **Score cache TTL**: Increased peer score cache from 1 minute to 5 minutes to reduce CPU overhead

### Added
- `cache.PutFile()` method for atomic file moves from pre-verified temp files

## [1.6.0] - 2025-12-18

### Security
- **Eclipse attack mitigation**: Block connections to/from private/reserved IP addresses in multiaddrs
  - Prevents attackers from announcing private IPs in DHT provider records
  - Filters multiaddrs in `InterceptAccept` and `InterceptAddrDial`
- **DHT info leakage prevention**: Skip DHT announcements in private swarm mode (when peer allowlist is active)
- **Range request validation**: Validate byte range bounds in transfer requests to prevent invalid ranges
- **Provider address filtering**: Filter blocked addresses from DHT provider results before connecting

### Added
- `internal/security/multiaddr.go`: Multiaddr validation functions for blocking private/reserved IPs
- `IsBlockedMultiaddr()` and `FilterBlockedAddrs()` functions

## [1.5.1] - 2025-12-17

### Fixed
- Fix golangci-lint gofmt errors in rate limiting code
- Fix ineffassign error in peer limiter test

## [1.5.0] - 2025-12-17

### Added
- **Per-peer rate limiting**: Rate limit individual peers to prevent bandwidth monopolization
  - Configurable `per_peer_upload_rate` and `per_peer_download_rate`
  - Auto mode divides global limit by expected number of peers
  - Both global and per-peer limits enforced simultaneously (stricter wins)
  - Idle peer limiters automatically cleaned up after 30 seconds
- **Adaptive rate limiting**: Automatic rate adjustment based on peer performance
  - Integrates with peer scoring system (latency, throughput, reliability)
  - High-performing peers get boosted rates (up to 1.5x)
  - Poorly-performing peers get reduced rates (down to configurable minimum)
  - Congestion detection reduces rates when latency exceeds 500ms
  - Enabled by default when per-peer limiting is active
- New configuration options in `[transfer]` section:
  - `per_peer_upload_rate`: "auto", "0" (disabled), or specific rate like "5MB/s"
  - `per_peer_download_rate`: "auto", "0" (disabled), or specific rate
  - `expected_peers`: Number of peers for auto-calculation (default: 10)
  - `adaptive_rate_limiting`: Enable/disable adaptive adjustment
  - `adaptive_min_rate`: Floor rate for adaptive reduction (default: "100KB/s")
  - `adaptive_max_boost`: Maximum boost factor (default: 1.5)
- New Prometheus metrics:
  - `debswarm_peer_rate_limiters`: Number of active per-peer limiters
  - `debswarm_peer_rate_limit_bytes_per_second`: Current rate per peer
  - `debswarm_adaptive_adjustments_total`: Count of rate adjustments by type

## [1.4.2] - 2025-12-17

### Added
- Enhanced mDNS discovery logging to help debug local peer discovery
  - Log listen addresses when mDNS starts
  - Log when mDNS is explicitly disabled
  - Log discovered peer addresses (not just peer ID)
  - Log successful mDNS peer connections at Info level

## [1.4.1] - 2025-12-17

### Fixed
- Fix `identity show` to use same data directory resolution as daemon
- Fix `status` command to use configured metrics bind/port instead of hardcoded values
- Fix `peers` command hardcoded metrics URL
- Fix `config show` to display all configuration fields (was missing many sections)

### Added
- Comprehensive configuration documentation (`docs/configuration.md`)

### Changed
- Config show now displays resolved `data_directory` path
- Rate limits show "unlimited" instead of empty string
- Fixed documentation: grep pattern for peerID in bootstrap-node.md
- Fixed documentation: removed incorrect CGO/GCC build requirements

## [1.4.0] - 2025-12-17

### Added
- **Automatic retry for failed downloads**: Failed P2P downloads are automatically retried on subsequent APT requests
  - Configurable `retry_max_attempts` (default: 3)
  - Configurable `retry_interval` with exponential backoff (default: 5m)
  - Configurable `retry_max_age` to expire old failures (default: 1h)

## [1.3.3] - 2025-12-17

### Security
- **Log sanitization**: Sanitize peer IDs and file paths in log output to prevent log injection attacks

## [1.3.2] - 2025-12-16

### Added
- `--cache-path` flag for seed command to override default cache path when importing packages

## [1.3.1] - 2025-12-16

### Fixed
- Fix contextcheck lint error in keepalive goroutine

## [1.3.0] - 2025-12-16

### Added
- **Keepalive pings**: Periodic pings (every 5 minutes) to all connected peers prevent idle connections from being pruned by the connection manager
- **Longer grace period**: Connection manager grace period increased from 1 to 10 minutes

### Fixed
- Connected peers no longer drop to 0 after periods of inactivity

## [1.2.6] - 2025-12-16

Re-release of v1.2.5 (CI asset conflict).

## [1.2.5] - 2025-12-16

### Fixed
- **Proxy URL extraction**: Fix handling of APT proxy requests to correctly extract target URL from `r.URL.Host`
- **Systemd service**: Remove all `*Directory=` directives to avoid STATE_DIRECTORY errors
- **Systemd service**: Switch from `DynamicUser=yes` to static `debswarm` user for reliable directory permissions
- **Debian package**: postinst now creates `debswarm` system user/group and sets directory ownership
- **Data directory**: Auto-detect `/var/lib/debswarm` for system installs instead of deriving from cache path

## [1.2.1] - 2025-12-16

### Fixed
- Systemd `CACHE_DIRECTORY` now correctly overrides config file path setting

## [1.2.0] - 2025-12-16

### Added
- **Systemd compatibility**: Automatic detection of `CACHE_DIRECTORY` and `STATE_DIRECTORY` environment variables
  - Running under systemd with `CacheDirectory=` and `StateDirectory=` now works out-of-box
  - No manual config file changes needed for systemd deployments

### Fixed
- Fix directory validation to not check parent directory writability (fixes systemd `ProtectSystem=strict`)

## [1.1.1] - 2025-12-16

### Fixed
- Fix import ordering in cache.go for golangci-lint

## [1.1.0] - 2025-12-16

### Changed
- **Pure Go SQLite**: Replaced `mattn/go-sqlite3` with `modernc.org/sqlite`
  - No longer requires CGO or a C compiler to build
  - Enables cross-compilation without CGO toolchain
  - Works out-of-box on Windows without MinGW/TDM-GCC

## [1.0.1] - 2025-12-15

### Fixed
- **TOML config parsing**: Fixed DHT duration fields (`provider_ttl`, `announce_interval`) to parse correctly from TOML strings like "24h"

## [1.0.0] - 2025-12-15

### Added
- **Runtime profiling**: pprof endpoints at `/debug/pprof/` on metrics server for production debugging
- **E2E integration tests**: Full end-to-end tests for proxy, cache, index, and P2P transfer flows
  - `TestE2E_ProxyMirrorFallback`: Tests mirror serving and proxy handler
  - `TestE2E_CacheHit`: Verifies cache serves packages without mirror hits
  - `TestE2E_IndexAutoPopulation`: Tests Packages file parsing
  - `TestE2E_TwoNodeP2PTransfer`: Two-node P2P transfer with DHT discovery
  - `TestE2E_HashVerification`: Validates hash verification rejects bad packages
- **CLI smoke tests**: Test coverage for CLI commands (version, config, psk, etc.)

### Security
- **MaxHeaderBytes**: Added 1MB limit to all HTTP servers to prevent header-based DoS

### Changed
- All critical, high, and medium priority items for 1.0 are now complete
- Project is production-ready for general use

## [0.8.2] - 2025-12-15

### Added
- Enforce `transfer.max_concurrent_uploads` config option in P2P upload handler
- Enforce `transfer.max_concurrent_peer_downloads` config option in downloader

### Fixed
- Fix debian package build (remove redundant debian/install entries)
- Fix golangci-lint errors (shadow, contextcheck, unconvert, rowserrcheck)
- Fix goimports import ordering across codebase

### Changed
- CI now tests deb package building on every push

## [0.8.0] - 2025-12-15

### Added
- Pre-flight directory validation for systemd compatibility
  - Validates cache and data directories exist or can be created
  - Tests write permissions before daemon startup
  - Catches StateDirectory/CacheDirectory issues early with clear error messages

## [0.7.0] - 2025-12-15

### Added
- Config file validation on startup with detailed error messages
- Crash recovery for corrupted partial download state
- SIGHUP handler for config reload without restart
- Troubleshooting guide in documentation

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

[Unreleased]: https://github.com/clintcan/debswarm/compare/v1.11.5...HEAD
[1.11.5]: https://github.com/clintcan/debswarm/compare/v1.11.4...v1.11.5
[1.11.4]: https://github.com/clintcan/debswarm/compare/v1.11.3...v1.11.4
[1.11.3]: https://github.com/clintcan/debswarm/compare/v1.11.2...v1.11.3
[1.11.2]: https://github.com/clintcan/debswarm/compare/v1.11.1...v1.11.2
[1.11.1]: https://github.com/clintcan/debswarm/compare/v1.11.0...v1.11.1
[1.11.0]: https://github.com/clintcan/debswarm/compare/v1.10.0...v1.11.0
[1.10.0]: https://github.com/clintcan/debswarm/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/clintcan/debswarm/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/clintcan/debswarm/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/clintcan/debswarm/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/clintcan/debswarm/compare/v1.5.1...v1.6.0
[1.5.1]: https://github.com/clintcan/debswarm/compare/v1.5.0...v1.5.1
[1.5.0]: https://github.com/clintcan/debswarm/compare/v1.4.2...v1.5.0
[1.4.2]: https://github.com/clintcan/debswarm/compare/v1.4.1...v1.4.2
[1.4.1]: https://github.com/clintcan/debswarm/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/clintcan/debswarm/compare/v1.3.3...v1.4.0
[1.3.3]: https://github.com/clintcan/debswarm/compare/v1.3.2...v1.3.3
[1.3.2]: https://github.com/clintcan/debswarm/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/clintcan/debswarm/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/clintcan/debswarm/compare/v1.2.6...v1.3.0
[1.2.6]: https://github.com/clintcan/debswarm/compare/v1.2.5...v1.2.6
[1.2.5]: https://github.com/clintcan/debswarm/compare/v1.2.1...v1.2.5
[1.2.1]: https://github.com/clintcan/debswarm/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/clintcan/debswarm/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/clintcan/debswarm/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/clintcan/debswarm/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/clintcan/debswarm/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/clintcan/debswarm/compare/v0.8.2...v1.0.0
[0.8.2]: https://github.com/clintcan/debswarm/compare/v0.8.0...v0.8.2
[0.8.0]: https://github.com/clintcan/debswarm/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/clintcan/debswarm/compare/v0.6.2...v0.7.0
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
