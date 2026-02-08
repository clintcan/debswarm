# Prometheus Alerting Rules for debswarm

Ready-to-use alerting rules for monitoring a debswarm deployment.

## Setup

1. Copy `alerts.yml` to your Prometheus rules directory:

   ```bash
   cp alerts.yml /etc/prometheus/rules/debswarm.yml
   ```

2. Add a scrape target for debswarm in `prometheus.yml`:

   ```yaml
   scrape_configs:
     - job_name: "debswarm"
       static_configs:
         - targets: ["localhost:9978"]
   ```

3. Ensure the rules file is loaded in `prometheus.yml`:

   ```yaml
   rule_files:
     - "rules/*.yml"
   ```

4. Reload Prometheus:

   ```bash
   systemctl reload prometheus
   # or
   kill -HUP $(pidof prometheus)
   ```

## Alerts Overview

| Alert | Severity | Fires When |
|-------|----------|------------|
| `DebswarmDown` | critical | Instance unreachable for >5m |
| `HighVerificationFailureRate` | critical | >0.1 verification failures/sec over 5m |
| `NoPeersConnected` | critical | Zero connected peers for >10m |
| `CacheNearlyFull` | warning | Cache >5 GB and >1000 packages for >15m |
| `HighMirrorFallbackRate` | warning | P2P ratio below 20% over 15m |
| `DHTRoutingTableEmpty` | warning | Empty DHT routing table for >5m |
| `HighErrorRate` | warning | >1 error/sec across all types over 5m |
| `FleetCoordinationInactive` | info | Fleet peers present but no coordination for >30m |
| `DownloadResumeFrequent` | info | >0.1 download resumes/sec over 15m |

## Customization

Adjust thresholds to match your environment:

- **CacheNearlyFull**: Change `5e9` (5 GB) to match your `cache.max_size` setting.
- **HighMirrorFallbackRate**: Lower `0.8` if you expect a higher P2P ratio.
- **NoPeersConnected**: Shorten `10m` if fast detection is needed.

## Available Metrics

debswarm exposes 40+ metrics at `http://localhost:9978/metrics`. Key metrics used by these rules:

- `debswarm_connected_peers` — current peer count
- `debswarm_routing_table_size` — DHT routing table entries
- `debswarm_cache_size_bytes` / `debswarm_cache_count` — cache usage
- `debswarm_verification_failures_total` — hash verification failures
- `debswarm_bytes_downloaded_total{source="p2p|mirror"}` — download volume by source
- `debswarm_errors_total{type="..."}` — error counts by type
- `debswarm_fleet_peers` / `debswarm_fleet_coordination_total` — fleet status
- `debswarm_downloads_resumed_total` — resumed downloads
