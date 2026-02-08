# Roadmap to 2.0 Release

This document tracks planned improvements for the 2.0 release of debswarm.

## Overview

v1.x focused on core P2P functionality, security, and operational stability. v2.0 will focus on enhanced observability, user experience, and advanced features.

## Feasibility Assessment

Each feature is rated:
- **Easy**: Uses existing infrastructure, minimal new code
- **Medium**: Requires new components but well-understood
- **Hard**: Significant complexity, research needed

## High Priority

| Issue | Description | Feasibility | Status |
|-------|-------------|-------------|--------|
| Cache analytics | Popular packages, bandwidth savings, hit rate reporting | Easy | **Done v1.22.0** |
| Package pinning | Prevent eviction of specific packages | Easy | **Done v1.23.0** |
| CLI `stats --watch` | Live updating statistics in terminal | Easy | **Done v1.26.0** |

## Medium Priority

| Issue | Description | Feasibility | Notes |
|-------|-------------|-------------|-------|
| Prometheus alerting rules | Ready-to-use alert configurations | Trivial | **Done v1.26.0** |
| Web API expansion | REST endpoints for cache management | Easy | **Done v1.27.0** |
| Dashboard charts | Real-time throughput visualization | Medium | **Done v1.28.0** |
| Configuration wizard | Interactive setup for new installations | Easy | Add survey/promptui dependency |

## Low Priority

| Issue | Description | Feasibility | Notes |
|-------|-------------|-------------|-------|
| `debswarm top` TUI | Interactive terminal dashboard | Medium | Requires TUI library (bubbletea/tview) |
| Peer map visualization | Geographic peer display | Medium | Needs IP geolocation data source |
| Predictive pre-warming | Learn from apt history to prefetch | Hard | Requires apt log parsing, pattern detection |
| Peer reputation sharing | Exchange peer scores across network | Hard | New protocol needed, trust model complex |
| Full mirror mode | Complete repository mirroring | Hard | Large scope, storage implications |

## Removed Items

| Issue | Reason |
|-------|--------|
| TLS between peers | Already enabled - libp2p uses TLS/Noise by default |
| Plugin system | Go plugins don't work on Windows; config-based selection preferred |

## Feature Details

### Cache Analytics (Done - v1.22.0)

Implemented in v1.22.0:

```go
// Added to cache.go
func (c *Cache) Stats() (*CacheStats, error)           // Total accesses, bandwidth saved
func (c *Cache) PopularPackages(limit int) ([]*Package, error)  // By access count
func (c *Cache) RecentPackages(limit int) ([]*Package, error)   // By last access
```

CLI commands:
- `debswarm cache stats` - Show hit rate, savings, access stats
- `debswarm cache stats -p N` - Include top N popular packages
- `debswarm cache popular` - List most accessed packages
- `debswarm cache recent` - List most recently accessed packages

### Package Pinning (Done - v1.23.0)

Implemented in v1.23.0:

```go
// Added to cache.go
func (c *Cache) Pin(sha256Hash string) error
func (c *Cache) Unpin(sha256Hash string) error
func (c *Cache) IsPinned(sha256Hash string) bool
func (c *Cache) ListPinned() ([]*Package, error)
func (c *Cache) PinnedCount() int
```

CLI commands:
- `debswarm cache pin <hash>` - Pin a package
- `debswarm cache unpin <hash>` - Unpin a package
- `debswarm cache unpin --all` - Unpin all packages
- `debswarm cache list --pinned` - Show only pinned packages

### CLI Stats Watch (Easy)

```bash
debswarm stats --watch          # Refresh every 2s
debswarm stats --watch --json   # Machine-readable
```

Uses existing `/api/stats` endpoint with terminal refresh.

### Prometheus Alerting Rules (Trivial)

Create `dist/prometheus/alerts.yml`:
```yaml
groups:
  - name: debswarm
    rules:
      - alert: HighVerificationFailureRate
        expr: rate(debswarm_verification_failures_total[5m]) > 0.1
      - alert: NoPeersConnected
        expr: debswarm_connected_peers == 0
      - alert: CacheNearlyFull
        expr: debswarm_cache_size_bytes / debswarm_cache_max_bytes > 0.9
```

### Dashboard Charts (Done - v1.28.0)

Implemented in v1.28.0 with nonce-based CSP and custom canvas renderer (no external library):

- **4 live-updating charts**: Throughput (P2P vs mirror), request rate, P2P ratio, connected peers
- **Nonce-based CSP**: Per-request `crypto/rand` nonce for `script-src`; API endpoints keep `script-src 'none'`
- **5-minute rolling window**: 60 data points at 5s intervals, counter-diff rate derivation
- **Custom canvas renderer**: ~150 lines inline JS, HiDPI support, responsive 2x2 grid
- **`<noscript>` fallback**: Restores meta-refresh and hides charts when JS disabled
- **Live DOM updates**: All stat values update without full page reload

### Predictive Pre-warming (Hard)

Would require:
1. Parse `/var/log/apt/history.log` for patterns
2. Track which packages update together
3. Background prefetch during low-usage periods
4. Configurable aggressiveness

Complexity: Pattern detection, storage for history, scheduled tasks.

## Implementation Order

Recommended sequence based on value/effort ratio:

1. ~~**Cache analytics** - High value, easy~~ **Done v1.22.0**
2. ~~**Package pinning** - Frequently requested, easy~~ **Done v1.23.0**
3. ~~**Prometheus alerts** - Zero code, high ops value~~ **Done v1.26.0**
4. ~~**CLI stats watch** - Quick win~~ **Done v1.26.0**
5. ~~**Web API expansion** - Enables external tools~~ **Done v1.27.0**
6. ~~**Dashboard charts** - Visual impact, medium effort~~ **Done v1.28.0**

## Version History

- **v1.28.0** - Dashboard charts: 4 real-time canvas charts with nonce-based CSP
- **v1.27.0** - Web API expansion: REST endpoints for cache management (`/api/cache/...`)
- **v1.26.0** - Prometheus alerting rules (`packaging/prometheus/alerts.yml`), CLI `stats --watch` command
- **v1.23.0** - Package pinning: `cache pin`, `cache unpin`, `cache list --pinned`
- **v1.22.0** - Cache analytics: `cache stats`, `cache popular`, `cache recent` commands

## Status

**All medium priority items complete.** Dashboard charts (v1.28.0) was the last medium-priority feature. Remaining items are low priority or require significant research.
