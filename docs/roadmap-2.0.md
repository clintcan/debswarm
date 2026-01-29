# Roadmap to 2.0 Release

This document tracks planned improvements for the 2.0 release of debswarm.

## Overview

v1.x focused on core P2P functionality, security, and operational stability. v2.0 will focus on enhanced observability, user experience, and advanced features.

## High Priority

| Issue | Description | Status |
|-------|-------------|--------|
| Dashboard enhancements | Real-time throughput charts, peer connection map, download history timeline | Planned |
| Cache analytics | Hit rate trends, bandwidth savings calculator, popular packages report | Planned |
| CLI improvements | Interactive TUI for monitoring, `debswarm top` command | Planned |

## Medium Priority

| Issue | Description | Status |
|-------|-------------|--------|
| Predictive pre-warming | Learn from apt history to prefetch likely-needed packages | Planned |
| Package pinning | Mark certain packages to never be evicted from cache | Planned |
| Prometheus alerting rules | Ready-to-use alert configurations for common issues | Planned |
| Configuration wizard | Interactive setup for new installations | Planned |

## Low Priority

| Issue | Description | Status |
|-------|-------------|--------|
| Peer reputation sharing | Share peer scores across the network for faster cold-start | Planned |
| TLS between peers | Encrypted P2P traffic (beyond PSK isolation) | Planned |
| Plugin system | Custom peer selection strategies, alternative DHT implementations | Planned |
| Full mirror mode | Complete repository mirroring instead of on-demand caching | Planned |
| Web API | REST API for cache management and monitoring | Planned |

## Feature Details

### Dashboard Enhancements

Upgrade the web dashboard with:
- Real-time throughput graphs (downloads/uploads over time)
- Interactive peer map showing connections and latency
- Download history with filtering and search
- Cache utilization treemap by package category

### Cache Analytics

Add analytics capabilities:
- Hit rate trends over configurable time windows
- Bandwidth savings report (P2P vs mirror traffic)
- Popular packages list with download counts
- Cache efficiency metrics (eviction rate, fill rate)

### CLI Improvements

New CLI features:
- `debswarm top` - Real-time dashboard in terminal (like htop)
- `debswarm stats --watch` - Live updating statistics
- Interactive mode with tab completion
- Colored output for better readability

### Predictive Pre-warming

Intelligent cache warming:
- Monitor apt history to learn update patterns
- Prefetch packages likely to be requested
- Time-based predictions (security updates on Tuesdays)
- Configurable aggressiveness levels

### Package Pinning

Cache management:
- `debswarm cache pin <package>` - Prevent eviction
- `debswarm cache unpin <package>` - Allow eviction
- Priority levels for pinned packages
- Automatic pinning for frequently-accessed packages

## Version History

(No releases yet)

## Status

**Planning Phase** - Gathering requirements and prioritizing features.
