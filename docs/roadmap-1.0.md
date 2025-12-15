# Roadmap to 1.0 Release

This document tracks the gaps and improvements needed before a production-ready 1.0 release of debswarm.

## Critical (Must Fix)

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **MaxConnections not enforced** | `internal/config/config.go:28` | Config option exists (`network.max_connections`) but P2P node doesn't limit connections - risk of resource exhaustion | Not started |
| **MinFreeSpace not enforced** | `internal/config/config.go:36` | Cache can fill disk completely, ignoring `cache.min_free_space` setting | Not started |
| **No health endpoint** | `internal/proxy/server.go` | Missing `/healthz` or `/ready` endpoint for monitoring/orchestration | Not started |

## High Priority

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **Config validation at startup** | `cmd/debswarm/main.go` | Invalid bootstrap peers fail silently during DHT init; should fail fast with clear errors | Not started |
| **Database recovery** | `internal/cache/cache.go` | Corrupted SQLite DB causes unclear failures; needs recovery mechanism or clear error messages | Not started |
| **SIGHUP reload support** | `cmd/debswarm/main.go` | Systemd service declares `ExecReload` but daemon doesn't handle SIGHUP for config reload | Not started |
| **Operational documentation** | `docs/` | Missing troubleshooting guide and upgrade/migration documentation | Not started |

## Medium Priority

| Issue | Location | Description | Status |
|-------|----------|-------------|--------|
| **IPv6 validation** | `internal/p2p/node.go` | Configured in libp2p but not tested on IPv6-only systems | Not started |
| **E2E tests** | `tests/` | Only unit tests with simulated peers; no real APT integration tests | Not started |
| **MaxConcurrentUploads enforcement** | `internal/config/config.go:43-44` | `transfer.max_concurrent_uploads` and `max_concurrent_peer_downloads` not fully enforced at daemon level | Not started |
| **Systemd directory validation** | `cmd/debswarm/main.go` | No pre-flight check that StateDirectory exists and is writable | Not started |

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

### MaxConnections Implementation
The `p2p.Node` should use libp2p's connection manager:
```go
import "github.com/libp2p/go-libp2p/p2p/net/connmgr"

cm, _ := connmgr.NewConnManager(
    lowWater,   // e.g., 80
    highWater,  // e.g., cfg.MaxConnections (100)
    connmgr.WithGracePeriod(time.Minute),
)
host.New(ctx, libp2p.ConnectionManager(cm))
```

### MinFreeSpace Implementation
Before cache operations in `cache.Put()`:
```go
var stat syscall.Statfs_t
syscall.Statfs(c.path, &stat)
freeBytes := stat.Bavail * uint64(stat.Bsize)
if freeBytes < minFreeSpace {
    return ErrInsufficientDiskSpace
}
```

### Health Endpoint
Add to proxy server:
```go
mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
    // Check DHT bootstrap status, cache accessibility, etc.
    if s.p2pNode.IsBootstrapped() && s.cache.IsHealthy() {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
        w.Write([]byte("not ready"))
    }
})
```

## Version History

- **v0.6.1** (2025-12-15): Dashboard peers table, expanded metrics
- **v0.6.0** (2025-12-15): Download resume support, security fixes
- **v0.5.x**: Core functionality, peer scoring, bandwidth limiting, benchmarking

## Target: v1.0.0

Once all Critical and High Priority items are resolved, the project is ready for 1.0 release.
