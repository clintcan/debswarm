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

| Version | Config Format | Cache Format | Database Schema | Protocol |
|---------|--------------|--------------|-----------------|----------|
| v1.0.x  | v1           | v1           | v3              | v1.0.0   |
| v0.6.x  | v1           | v1           | v3              | v1.0.0   |
| v0.5.x  | v1           | v1           | v2              | v1.0.0   |
| v0.4.x  | v1           | v1           | v1              | v1.0.0   |

All v0.x and v1.x versions use compatible P2P protocols and can interoperate.

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
