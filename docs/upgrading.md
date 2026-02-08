# Upgrading Guide

This guide covers upgrading debswarm between versions and migrating data.

## General Upgrade Process

### Using Package Manager (Recommended)

```bash
# Stop the service
sudo systemctl stop debswarm

# Upgrade package
sudo apt-get update
sudo apt-get install debswarm

# Start the service
sudo systemctl start debswarm

# Verify upgrade
debswarm version
systemctl status debswarm
```

### Manual Binary Upgrade

```bash
# Stop the service
sudo systemctl stop debswarm

# Backup current binary
sudo cp /usr/bin/debswarm /usr/bin/debswarm.backup

# Install new binary
sudo cp debswarm-new /usr/bin/debswarm
sudo chmod +x /usr/bin/debswarm

# Start the service
sudo systemctl start debswarm
```

## Version-Specific Notes

### Upgrading to v1.28.x

**From v1.27.x:**

v1.28.x adds real-time dashboard charts with nonce-based CSP. Fully backwards compatible, no configuration changes required.

1. **Dashboard Charts**: The dashboard now includes 4 live-updating canvas charts (throughput, request rate, P2P ratio, connected peers) that poll `/dashboard/api/stats` every 5 seconds.

2. **CSP Change**: The dashboard page now uses `script-src 'nonce-...'` instead of `script-src 'none'` to allow inline chart JavaScript. API endpoints retain `script-src 'none'`. If you have a reverse proxy that overrides CSP headers, ensure it passes through the dashboard's CSP or uses nonce forwarding.

3. **`<noscript>` Fallback**: When JavaScript is disabled, the dashboard falls back to the original 5-second meta-refresh behavior. Chart canvases are hidden via CSS.

4. **Routing Fix**: `/dashboard/api/stats` now correctly routes through `http.StripPrefix`. Previously this path did not resolve to the dashboard's internal API handler.

   **Action**: None required. New features available immediately after upgrade.

### Upgrading to v1.25.x

**From v1.24.x:**

v1.25.x wires up fleet coordination for LAN download deduplication. When multiple nodes on the same LAN need the same package, only one fetches from WAN; the others download from that peer via P2P. Fully backwards compatible.

1. **Fleet Coordination**: The fleet protocol (`/debswarm/fleet/1.0.0`) is now active when `fleet.enabled = true`. Peers coordinate via mDNS to avoid redundant WAN downloads.

2. **Configuration**: Fleet coordination requires mDNS to be enabled (`privacy.enable_mdns = true`). See `[fleet]` configuration section.

   **Action**: To enable fleet coordination, add to your config:
   ```toml
   [fleet]
   enabled = true
   claim_timeout = "5s"
   max_wait_time = "5m"
   ```

3. **No breaking changes**: All existing behavior is preserved. Fleet coordination is opt-in via `fleet.enabled`.

### Upgrading to v1.4.x

**From v1.3.x:**

v1.4.x adds automatic retry for failed downloads and enhanced mDNS logging. Fully backwards compatible.

1. **Automatic Retry**: Failed P2P downloads are now automatically retried. Configure with:
   ```toml
   [transfer]
   retry_max_attempts = 3   # 0 to disable
   retry_interval = "5m"
   retry_max_age = "1h"
   ```

2. **Enhanced mDNS Logging**: More detailed logging for local peer discovery troubleshooting.

   **Action**: None required. New features available immediately.

### Upgrading to v1.3.x

**From v1.2.x:**

v1.3.x adds keepalive pings to prevent peer disconnection and log sanitization for security. Backwards compatible.

1. **Keepalive Pings**: Periodic pings prevent idle connections from being pruned.

2. **Log Sanitization**: User-controlled data is now sanitized in logs to prevent log injection.

   **Action**: None required.

### Upgrading to v1.2.x

**From v1.1.x:**

v1.2.x improves systemd compatibility. Backwards compatible.

1. **Systemd Integration**: Automatic detection of `CACHE_DIRECTORY` and `STATE_DIRECTORY` environment variables from systemd.

   **Action**: None required. Works automatically with systemd `CacheDirectory=` and `StateDirectory=` directives.

### Upgrading to v1.1.0

**From v1.0.x:**

v1.1.0 is a significant change that switches from CGO SQLite to pure Go SQLite.

1. **Pure Go SQLite**: The SQLite driver changed from `mattn/go-sqlite3` (CGO) to `modernc.org/sqlite` (pure Go).
   - **No longer requires GCC or C compiler to build**
   - Cross-compilation now works without CGO toolchain
   - Database format is compatible; no migration needed

   **Action**: If building from source, you no longer need `libsqlite3-dev` or GCC installed.

2. **Build Requirements Changed**: Update any CI/CD pipelines or build scripts to remove CGO dependencies.

### Upgrading to v1.0.0

**From v0.6.x:**

v1.0.0 includes several improvements that are backwards compatible:

1. **Config Validation**: The daemon now validates configuration at startup and fails fast on errors. Previously invalid configs (like malformed bootstrap peers) would silently fail during runtime.

   **Action**: Run `debswarm daemon` in foreground first to catch any config errors:
   ```bash
   debswarm daemon --log-level debug
   ```

2. **Database Recovery**: SQLite corruption is now detected and handled automatically. Corrupted databases are backed up and fresh ones created.

   **Action**: None required. Check logs for any recovery messages after upgrade.

3. **SIGHUP Reload**: Configuration can now be reloaded without restart via `systemctl reload debswarm`.

   **Action**: None required. New feature available immediately.

### Upgrading to v0.6.x

**From v0.5.x:**

1. **Cache Structure**: Cache now includes partial download directories for resume support.

   **Action**: None required. New directories created automatically.

2. **Database Schema**: New tables added for download state persistence.

   **Action**: Schema migrations run automatically on startup.

3. **MinFreeSpace**: New config option `cache.min_free_space` prevents disk exhaustion.

   **Action**: Recommended to add to config:
   ```toml
   [cache]
   min_free_space = "1GB"
   ```

### Upgrading to v0.5.x

**From v0.4.x:**

1. **Peer Scoring**: New peer scoring system with persistence.

   **Action**: None required. Peer scores start fresh after upgrade.

2. **Rate Limiting**: New rate limiting configuration options.

   **Action**: Optional - configure if needed:
   ```toml
   [transfer]
   max_upload_rate = "10MB/s"
   max_download_rate = "50MB/s"
   ```

## Data Migration

### Cache Migration

The cache format is stable across versions. No migration needed.

To verify cache integrity after upgrade:
```bash
debswarm cache stats
```

### Configuration Migration

Configuration format is backwards compatible. New options use sensible defaults.

To see all current settings including new defaults:
```bash
debswarm config show
```

### Identity Migration

Peer identity keys are stable across versions. Your peer ID remains the same.

To verify identity:
```bash
debswarm identity show
```

## Rollback Procedure

If issues occur after upgrade:

```bash
# Stop the service
sudo systemctl stop debswarm

# Restore backup binary (if manual install)
sudo cp /usr/bin/debswarm.backup /usr/bin/debswarm

# Or downgrade package
sudo apt-get install debswarm=0.6.1-1  # specific version

# Start the service
sudo systemctl start debswarm
```

### Database Rollback

If database schema changes cause issues:

```bash
# Stop service
sudo systemctl stop debswarm

# Backup current database
cp ~/.cache/debswarm/state.db ~/.cache/debswarm/state.db.new

# Restore old database (if available)
cp ~/.cache/debswarm/state.db.backup ~/.cache/debswarm/state.db

# Start service
sudo systemctl start debswarm
```

**Note**: Package files on disk are separate from the database. Even if database is reset, packages remain cached. Run `debswarm cache rebuild` to restore metadata from files.

## Multi-Node Upgrades

For clusters or swarms with multiple nodes:

### Rolling Upgrade (Recommended)

1. Upgrade nodes one at a time
2. Verify each node is healthy before proceeding
3. Allow DHT to stabilize between upgrades (~5 minutes)

```bash
# On each node, one at a time:
sudo systemctl stop debswarm
# ... upgrade ...
sudo systemctl start debswarm

# Verify health
curl http://127.0.0.1:9978/health
```

### Coordinated Upgrade

For major version upgrades with breaking changes:

1. Schedule maintenance window
2. Stop all nodes
3. Upgrade all nodes
4. Start bootstrap/seed nodes first
5. Start remaining nodes

## Compatibility Matrix

| Version | Config Format | Cache Format | Database Schema | Protocol | SQLite Driver |
|---------|--------------|--------------|-----------------|----------|---------------|
| v1.28.x | v1           | v1           | v4              | v1.0.0   | Pure Go       |
| v1.25.x | v1           | v1           | v4              | v1.0.0   | Pure Go       |
| v1.4.x  | v1           | v1           | v4              | v1.0.0   | Pure Go       |
| v1.3.x  | v1           | v1           | v3              | v1.0.0   | Pure Go       |
| v1.2.x  | v1           | v1           | v3              | v1.0.0   | Pure Go       |
| v1.1.x  | v1           | v1           | v3              | v1.0.0   | Pure Go       |
| v1.0.x  | v1           | v1           | v3              | v1.0.0   | CGO           |
| v0.6.x  | v1           | v1           | v3              | v1.0.0   | CGO           |
| v0.5.x  | v1           | v1           | v2              | v1.0.0   | CGO           |
| v0.4.x  | v1           | v1           | v1              | v1.0.0   | CGO           |

All v0.x and v1.x versions use compatible P2P protocols and can interoperate.

**Note**: v1.1.0+ uses pure Go SQLite which is database-format compatible with CGO SQLite. No migration needed.

## Pre-Upgrade Checklist

- [ ] Backup configuration: `cp /etc/debswarm/config.toml ~/debswarm-config.backup`
- [ ] Backup identity: `cp ~/.local/share/debswarm/identity.key ~/debswarm-identity.backup`
- [ ] Note current version: `debswarm version`
- [ ] Check disk space: `df -h ~/.cache/debswarm`
- [ ] Review changelog for breaking changes
- [ ] Plan for brief service interruption

## Post-Upgrade Verification

```bash
# Check version
debswarm version

# Check service status
systemctl status debswarm

# Check health endpoint
curl http://127.0.0.1:9978/health

# Check peer connectivity
curl http://127.0.0.1:9978/stats | jq '.connected_peers'

# Check cache integrity
debswarm cache stats

# Test APT integration
sudo apt-get update
```
