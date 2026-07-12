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

# After rebuild, verify package integrity
debswarm cache verify
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

# Verify all cached packages have valid checksums
debswarm cache verify

# Verify database integrity
sqlite3 ~/.cache/debswarm/state.db "PRAGMA integrity_check;"
```

### APT Integration Issues

#### Third-party repositories failing

**Symptom**: Third-party repositories (Docker, PPAs, etc.) show errors like:
- `502 Bad Gateway`
- `403 Forbidden`
- `Invalid response from proxy: HTTP/1.1 301 Moved Permanently`

**Cause**: debswarm only proxies official Debian/Ubuntu/Mint repositories by default for security (SSRF protection). Third-party repositories are blocked unless explicitly configured.

**Solution 1 (Recommended)**: Add the repository host to debswarm's allowed list:

```toml
# In /etc/debswarm/config.toml or ~/.config/debswarm/config.toml
[proxy]
allowed_hosts = [
  "download.docker.com",
  "ppa.launchpad.net",
  "apt.postgresql.org",
]
```

This allows the traffic through debswarm (with logging/metrics) while maintaining security.

**Solution 2**: Configure APT to bypass the proxy using `"DIRECT"`:

```bash
# Edit the debswarm APT configuration
sudo nano /etc/apt/apt.conf.d/90debswarm
```

Add bypass rules for your third-party repositories:

```
// Bypass proxy for third-party repositories
Acquire::http::Proxy::download.docker.com "DIRECT";
Acquire::https::Proxy::download.docker.com "DIRECT";

Acquire::http::Proxy::ppa.launchpad.net "DIRECT";
Acquire::https::Proxy::ppa.launchpad.net "DIRECT";
```

The `"DIRECT"` keyword tells APT to connect directly without using the proxy.

**Common third-party repositories requiring bypass**:

| Repository | Hostname |
|------------|----------|
| Docker | `download.docker.com` |
| Ubuntu PPAs | `ppa.launchpad.net` |
| Microsoft (VS Code, Teams) | `packages.microsoft.com` |
| Google Chrome | `dl.google.com` |
| MongoDB | `repo.mongodb.org` |
| PostgreSQL | `apt.postgresql.org` |
| Node.js | `deb.nodesource.com` |

**Natively supported repositories** (no bypass needed):
- Debian (`deb.debian.org`, `security.debian.org`, etc.)
- Ubuntu (`archive.ubuntu.com`, `security.ubuntu.com`, etc.)
- Linux Mint (`packages.linuxmint.com`)
- Any mirror matching `mirrors.*`, `mirror.*`, or `ftp.*`

**Why this design?**

debswarm restricts the proxy to known mirrors to prevent:
- SSRF (Server-Side Request Forgery) attacks
- Accidental exposure of internal services
- Unverified packages entering the P2P network

Third-party repositories don't benefit from debswarm's P2P features anyway (packages aren't hash-indexed), so bypassing them has no functional downside.

---

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

**Symptom**: HTTPS repos fail through the proxy, or work but never use the cache or P2P.

There are two ways to handle an HTTPS repository, and they behave very differently. Pick based on whether you need caching and P2P for that repo.

##### Option A — CONNECT tunnel (v1.20+): works everywhere, but no caching or P2P

APT keeps its `https://` source and debswarm tunnels the encrypted connection through untouched.

```bash
# Configure APT to use proxy for HTTPS (in addition to HTTP)
echo 'Acquire::https::Proxy "http://127.0.0.1:9977";' | \
  sudo tee -a /etc/apt/apt.conf.d/00debswarm
```

**How it works:**
1. APT sends `CONNECT deb.debian.org:443` to the proxy
2. debswarm validates the target is an allowed mirror
3. A TCP tunnel is established for the encrypted traffic
4. APT communicates directly with the mirror over TLS through the tunnel

**The catch:** the tunnel is *opaque*. debswarm sees only encrypted bytes, so packages fetched this way **cannot be cached, hash-verified, or shared over P2P**. If a repo's packages are going over a CONNECT tunnel, they are not in the swarm.

You still get partial benefit when a repo's *index* is fetched over plain HTTP: the package hashes become known, so a later `.deb` request can be served from P2P. But if the `.deb` itself is tunnelled, it isn't.

##### Option B — upstream HTTPS fetch (v1.30+): full caching and P2P

Point APT at the repo over **plain HTTP** and let debswarm open its own HTTPS connection to the mirror on your behalf. APT talks HTTP to your local proxy; debswarm talks HTTPS to the internet. Packages are cached, SHA256-verified against the signed index, and shared over P2P like any other repo.

This is **not** a MITM — no certificate is forged, and APT's GPG verification of the signed `Release`/`InRelease` is untouched and still authoritative.

```toml
# /etc/debswarm/config.toml
[proxy]
https_upstream_hosts = ["pkgs.k8s.io"]   # included by default
```

```
# /etc/apt/sources.list.d/kubernetes.list — note http://, not https://
deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] http://pkgs.k8s.io/core:/stable:/v1.30/deb/ /
```

Notes:
- The host must also be allowed (built-in, trusted via `trust_known_repos`, or listed in `allowed_hosts`). `pkgs.k8s.io` satisfies this by default.
- Only `http://` requests to listed hosts are upgraded. Matching is case-insensitive and covers subdomains.
- This is the right option for HTTPS-only repos (such as `pkgs.k8s.io`), which cannot serve plain HTTP at all.

See [Configuration: HTTPS-only repositories](configuration.md#https-only-repositories) for details.

**Security notes (both options):**
- CONNECT tunnels only allow ports 443 and 80
- Only allowed mirrors are permitted; private/internal hosts (localhost, RFC1918 addresses) are blocked
- Every cached package is verified against the SHA256 in the signed repository index

**Pre-v1.20 behavior:**
In older versions, HTTPS repositories bypass the proxy entirely. For P2P benefits with older versions, use HTTP mirrors or configure mixed sources.

### Fleet Coordination Issues

#### Fleet not reducing WAN downloads

**Symptom**: Multiple LAN nodes downloading the same package from WAN simultaneously.

**Causes**:
- Fleet coordination not enabled
- mDNS disabled (fleet requires mDNS for peer discovery)
- Peers not on the same LAN segment

**Solution**:
```bash
# Verify fleet is enabled in config
grep -A5 '\[fleet\]' /etc/debswarm/config.toml

# Ensure mDNS is enabled
grep enable_mdns /etc/debswarm/config.toml

# Enable fleet coordination:
# [fleet]
# enabled = true
# claim_timeout = "5s"
# max_wait_time = "5m"

# Check logs for fleet activity
journalctl -u debswarm -f | grep -i fleet
```

#### Fleet peer timeout

**Symptom**: "fleet wait timeout" in logs; node falls back to WAN download.

**Cause**: The peer that claimed WAN download responsibility is taking too long.

**Solution**:
```bash
# Increase max wait time in config.toml
[fleet]
max_wait_time = "10m"   # Default is 5m; increase for slow connections

# Check if the claiming peer is healthy
curl http://<peer-ip>:9978/health
```

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
