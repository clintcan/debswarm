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

| Issue | Description | Feasibility | Notes |
|-------|-------------|-------------|-------|
| Cache analytics | Popular packages, bandwidth savings, hit rate reporting | Easy | `access_count` already tracked, metrics exist |
| Package pinning | Prevent eviction of specific packages | Easy | Add `pinned` column, modify eviction query |
| CLI `stats --watch` | Live updating statistics in terminal | Easy | Stats API exists, just needs refresh loop |

## Medium Priority

| Issue | Description | Feasibility | Notes |
|-------|-------------|-------------|-------|
| Prometheus alerting rules | Ready-to-use alert configurations | Trivial | Documentation only, no code changes |
| Web API expansion | REST endpoints for cache management | Easy | Expand existing `/api/` endpoints |
| Dashboard charts | Real-time throughput visualization | Medium | Requires JS library, CSP relaxation |
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

### Cache Analytics (Easy)

The cache already tracks `access_count` per package. Implementation:

```go
// Add to cache.go
func (c *Cache) PopularPackages(limit int) ([]Package, error)
func (c *Cache) BandwidthSavings() (p2pBytes, mirrorBytes int64)
func (c *Cache) HitRate() float64
```

Add CLI commands:
- `debswarm cache stats` - Show hit rate, savings
- `debswarm cache popular` - List most accessed packages

### Package Pinning (Easy)

Schema change:
```sql
ALTER TABLE packages ADD COLUMN pinned INTEGER DEFAULT 0;
```

Modify eviction to skip pinned:
```sql
WHERE pinned = 0 ORDER BY (last_accessed + access_count * 3600) ASC
```

CLI commands:
- `debswarm cache pin <package-or-hash>`
- `debswarm cache unpin <package-or-hash>`
- `debswarm cache list --pinned`

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

### Dashboard Charts (Medium)

Current CSP blocks JavaScript (`script-src 'none'`). Options:
1. Add Chart.js with nonce-based CSP
2. Use CSS-only sparklines (limited)
3. SVG-based charts server-rendered

Recommended: Nonce-based CSP with lightweight chart library.

### Predictive Pre-warming (Hard)

Would require:
1. Parse `/var/log/apt/history.log` for patterns
2. Track which packages update together
3. Background prefetch during low-usage periods
4. Configurable aggressiveness

Complexity: Pattern detection, storage for history, scheduled tasks.

## Implementation Order

Recommended sequence based on value/effort ratio:

1. **Cache analytics** - High value, easy
2. **Package pinning** - Frequently requested, easy
3. **Prometheus alerts** - Zero code, high ops value
4. **CLI stats watch** - Quick win
5. **Web API expansion** - Enables external tools
6. **Dashboard charts** - Visual impact, medium effort

## Version History

(No releases yet)

## Status

**Planning Phase** - Prioritizing features based on feasibility assessment.
