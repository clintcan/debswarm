# Troubleshooting Guide

This guide covers common issues and their solutions when running debswarm.

## Quick Diagnostics

Run these commands to gather diagnostic information:

```bash
# Check daemon status
systemctl status debswarm

# View recent logs
journalctl -u debswarm -n 100

# Check metrics endpoint
curl http://127.0.0.1:9978/stats

# Verify APT proxy configuration
grep -r "Acquire::http::Proxy" /etc/apt/
```

## Common Issues

### Daemon Won't Start

#### "invalid configuration" error

**Symptom**: Daemon exits immediately with configuration validation error.

**Cause**: Invalid settings in config file (bad multiaddr, invalid port, etc.)

**Solution**:
```bash
# Check your config file syntax
debswarm config show

# Common fixes:
# - Verify bootstrap peer addresses are valid multiaddrs
# - Ensure ports are between 1-65535
# - Check that psk and psk_path aren't both set
```

#### "failed to initialize cache" error

**Symptom**: Daemon can't create or access cache directory.

**Solution**:
```bash
# Check cache directory permissions
ls -la ~/.cache/debswarm/

# Fix permissions if needed
chmod 750 ~/.cache/debswarm/
chown $USER:$USER ~/.cache/debswarm/

# For systemd, check StateDirectory
systemctl cat debswarm | grep StateDirectory
```

#### "database corrupted" error

**Symptom**: SQLite database corruption detected at startup.

**Solution**:
The daemon automatically backs up corrupted databases and creates fresh ones. Package files on disk are preserved.

```bash
# Check for backup files
ls -la ~/.cache/debswarm/*.corrupted.*

# Rebuild metadata from existing package files (if available)
debswarm cache rebuild
```

#### Port already in use

**Symptom**: "address already in use" error.

**Solution**:
```bash
# Find what's using the port
sudo lsof -i :9977  # proxy port
sudo lsof -i :4001  # P2P port

# Either stop the conflicting process or change ports
debswarm daemon --proxy-port 9978 --p2p-port 4002
```

### No Peers Found

#### DHT bootstrap failing

**Symptom**: `routingTableSize: 0` in logs, no peers connecting.

**Causes**:
- Firewall blocking P2P port (default 4001)
- Bootstrap peers unreachable
- Network isolation (NAT without hole punching)

**Solution**:
```bash
# Check firewall
sudo ufw status
sudo iptables -L -n | grep 4001

# Open P2P port
sudo ufw allow 4001/tcp
sudo ufw allow 4001/udp

# Verify bootstrap connectivity
curl -v telnet://bootstrap.libp2p.io:4001

# Enable mDNS for local peer discovery
# In config.toml:
[privacy]
enable_mdns = true
```

#### Private swarm misconfiguration

**Symptom**: Peers visible but connections rejected.

**Solution**:
```bash
# Verify PSK fingerprints match on all nodes
debswarm psk show

# Check peer allowlist if configured
grep peer_allowlist /etc/debswarm/config.toml
```

### Slow Downloads

#### Downloads falling back to mirrors

**Symptom**: Packages always downloading from mirrors, not peers.

**Causes**:
- No peers have the package
- Peers too slow (mirror racing wins)
- DHT lookups timing out

**Solution**:
```bash
# Check if peers have packages
curl http://127.0.0.1:9978/stats | jq '.peers'

# Monitor DHT queries
journalctl -u debswarm -f | grep -i dht

# Pre-warm cache from local mirror
debswarm seed import --recursive /var/cache/apt/archives/
```

#### Rate limiting too aggressive

**Symptom**: Transfers artificially slow.

**Solution**:
```bash
# Check current limits
debswarm config show | grep rate

# Increase or disable limits in config.toml
[transfer]
max_upload_rate = "0"      # unlimited
max_download_rate = "0"    # unlimited
```

### Cache Issues

#### Cache filling up disk

**Symptom**: Disk space exhausted despite `min_free_space` setting.

**Solution**:
```bash
# Check cache size vs limits
debswarm cache stats

# Manually clear old packages
debswarm cache clear

# Verify min_free_space is set
grep min_free_space /etc/debswarm/config.toml

# Recommended setting:
[cache]
max_size = "10GB"
min_free_space = "2GB"
```

#### Cache not being used

**Symptom**: Same packages downloaded repeatedly.

**Causes**:
- Cache path mismatch between config and runtime
- Permission issues
- Database corruption

**Solution**:
```bash
# Verify cache location
debswarm config show | grep path

# Check what's actually cached
debswarm cache list

# Verify database integrity
sqlite3 ~/.cache/debswarm/state.db "PRAGMA integrity_check;"
```

### APT Integration Issues

#### APT not using proxy

**Symptom**: APT downloads directly from mirrors, bypassing debswarm.

**Solution**:
```bash
# Set APT proxy configuration
echo 'Acquire::http::Proxy "http://127.0.0.1:9977";' | \
  sudo tee /etc/apt/apt.conf.d/00debswarm

# Verify it's set
apt-config dump | grep -i proxy

# Test with verbose output
sudo apt-get update -o Debug::Acquire::http=true
```

#### HTTPS repositories not working

**Symptom**: HTTPS repos fail through proxy.

**Cause**: debswarm only proxies HTTP; HTTPS goes direct by design.

**Solution**: This is expected behavior. HTTPS repositories bypass the proxy for security. For P2P benefits, use HTTP mirrors or configure mixed sources.

### Systemd Service Issues

#### Service won't reload

**Symptom**: `systemctl reload debswarm` has no effect.

**Solution**:
The daemon now handles SIGHUP for config reload:
```bash
# Reload configuration
systemctl reload debswarm

# Check logs for reload confirmation
journalctl -u debswarm -n 20 | grep -i reload

# Note: Port changes require full restart
systemctl restart debswarm
```

#### Service keeps restarting

**Symptom**: Service in restart loop.

**Solution**:
```bash
# Check failure reason
systemctl status debswarm
journalctl -u debswarm --since "5 minutes ago"

# Common causes:
# - Config validation failures (fix config)
# - Port conflicts (change ports or stop conflicting service)
# - Permission issues (check User/Group in service file)
```

## Collecting Debug Information

When reporting issues, include:

```bash
# Version info
debswarm version

# Configuration (sanitized - remove PSK!)
debswarm config show

# Recent logs
journalctl -u debswarm --since "1 hour ago" > debswarm-logs.txt

# System info
uname -a
cat /etc/os-release

# Network info
ip addr show
ss -tlnp | grep -E '(9977|9978|4001)'

# Metrics snapshot
curl http://127.0.0.1:9978/stats > debswarm-stats.json
curl http://127.0.0.1:9978/metrics > debswarm-metrics.txt
```

## Getting Help

- GitHub Issues: https://github.com/debswarm/debswarm/issues
- Check existing issues before creating new ones
- Include debug information when reporting problems
