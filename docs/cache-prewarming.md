# Pre-warming the debswarm Cache

This guide explains how to pre-populate your debswarm cache with packages by downloading them through the proxy. This is useful for seeding the network so other peers can benefit from your cached packages.

## Overview

There are two ways to populate debswarm's cache:

1. **Import existing .deb files** - Use `debswarm seed import` (see [Seeding](#seeding-from-existing-files) section)
2. **Download through APT** - Download packages via the proxy to cache them (this guide)

The APT download method is simpler as it doesn't require maintaining a local mirror.

## Prerequisites

- debswarm installed and running as a daemon
- APT configured to use debswarm proxy
- Sufficient disk space for cached packages

Verify debswarm is running:

```bash
systemctl status debswarm
# or
curl http://localhost:9978/stats
```

Verify APT is using the proxy:

```bash
cat /etc/apt/apt.conf.d/90debswarm.conf
# Should show: Acquire::http::Proxy "http://127.0.0.1:9977";
```

## Method 1: Cache Installed Packages

Download all packages that are currently installed on your system:

```bash
# Create a working directory
mkdir -p /tmp/apt-prewarm && cd /tmp/apt-prewarm

# Download all installed packages (through debswarm proxy)
sudo apt-get update
dpkg --get-selections | grep -v deinstall | awk '{print $1}' | \
    xargs -n 50 apt-get download 2>/dev/null

# Clean up downloaded files (already cached by debswarm)
rm -f *.deb
cd && rm -rf /tmp/apt-prewarm
```

This downloads every installed package through the debswarm proxy, caching them for the network.

## Method 2: Cache Specific Package Sets

### Popular Desktop Packages

```bash
#!/bin/bash
# prewarm-desktop.sh - Cache common desktop packages

PACKAGES=(
    # Browsers
    firefox chromium-browser

    # Office
    libreoffice-writer libreoffice-calc libreoffice-impress

    # Media
    vlc gimp inkscape audacity

    # Development
    git curl wget vim nano build-essential

    # Utilities
    htop tmux zip unzip
)

cd /tmp
apt-get download "${PACKAGES[@]}" 2>/dev/null || true
rm -f *.deb
```

### Server Packages

```bash
#!/bin/bash
# prewarm-server.sh - Cache common server packages

PACKAGES=(
    # Web servers
    nginx apache2

    # Databases
    postgresql mysql-server redis-server

    # Containers
    docker.io docker-compose containerd

    # Monitoring
    prometheus grafana

    # Languages/runtimes
    python3 python3-pip nodejs npm golang

    # Tools
    certbot fail2ban ufw
)

cd /tmp
apt-get download "${PACKAGES[@]}" 2>/dev/null || true
rm -f *.deb
```

### Kernel and Security Updates

```bash
#!/bin/bash
# prewarm-security.sh - Cache kernel and security packages

# Get latest kernel versions
KERNELS=$(apt-cache search linux-image | grep -E "linux-image-[0-9]" | \
    sort -V | tail -5 | awk '{print $1}')

# Security packages
SECURITY_PKGS="openssl libssl3 ca-certificates gnupg"

cd /tmp
apt-get download $KERNELS $SECURITY_PKGS 2>/dev/null || true
rm -f *.deb
```

## Method 3: Mirror Repository Updates

Automatically cache all packages that would be downloaded during `apt upgrade`:

```bash
#!/bin/bash
# prewarm-upgrades.sh - Cache available upgrades

set -e

# Update package lists
apt-get update

# Download (but don't install) all upgradeable packages
apt-get --download-only dist-upgrade -y

# Packages are now in /var/cache/apt/archives/ AND debswarm cache
echo "Upgrade packages cached successfully"
```

## Scheduled Cache Warming

### Using Cron

Create a cron job to automatically warm the cache on a schedule:

```bash
sudo nano /etc/cron.d/debswarm-prewarm
```

```cron
# Pre-warm debswarm cache daily at 3 AM
0 3 * * * root /usr/local/bin/debswarm-prewarm.sh >> /var/log/debswarm-prewarm.log 2>&1

# Cache available upgrades every 6 hours
0 */6 * * * root apt-get update && apt-get --download-only dist-upgrade -y >> /var/log/debswarm-prewarm.log 2>&1
```

### Using systemd Timer

Create the service unit:

```bash
sudo nano /etc/systemd/system/debswarm-prewarm.service
```

```ini
[Unit]
Description=Pre-warm debswarm package cache
After=network-online.target debswarm.service
Wants=network-online.target
Requires=debswarm.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/debswarm-prewarm.sh
User=root
StandardOutput=journal
StandardError=journal
```

Create the timer unit:

```bash
sudo nano /etc/systemd/system/debswarm-prewarm.timer
```

```ini
[Unit]
Description=Pre-warm debswarm cache periodically

[Timer]
OnCalendar=*-*-* 03:00:00
RandomizedDelaySec=1800
Persistent=true

[Install]
WantedBy=timers.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now debswarm-prewarm.timer

# Check timer status
systemctl list-timers debswarm-prewarm.timer
```

## Complete Pre-warming Script

Here's a comprehensive script that combines all methods:

```bash
#!/bin/bash
# /usr/local/bin/debswarm-prewarm.sh
# Pre-warm debswarm cache with commonly needed packages

set -e

LOG_PREFIX="[debswarm-prewarm]"
TEMP_DIR="/tmp/debswarm-prewarm-$$"

log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $LOG_PREFIX $1"
}

cleanup() {
    rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

# Ensure debswarm is running
if ! curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    log "ERROR: debswarm is not running"
    exit 1
fi

mkdir -p "$TEMP_DIR"
cd "$TEMP_DIR"

# Update package lists
log "Updating package lists..."
apt-get update -qq

# 1. Cache available upgrades
log "Caching available upgrades..."
apt-get --download-only dist-upgrade -y -qq 2>/dev/null || true

# 2. Cache installed packages (optional, uncomment if desired)
# log "Caching installed packages..."
# dpkg --get-selections | grep -v deinstall | awk '{print $1}' | \
#     xargs -n 50 apt-get download 2>/dev/null || true
# rm -f *.deb

# 3. Cache custom package list (edit as needed)
CUSTOM_PACKAGES=(
    # Add packages commonly used in your environment
    # curl wget git vim
)

if [ ${#CUSTOM_PACKAGES[@]} -gt 0 ]; then
    log "Caching custom packages..."
    apt-get download "${CUSTOM_PACKAGES[@]}" 2>/dev/null || true
    rm -f *.deb
fi

# Report stats
STATS=$(curl -s http://localhost:9978/stats)
CACHE_SIZE=$(echo "$STATS" | grep -o '"cache_size_bytes":[0-9]*' | cut -d: -f2)
CACHE_COUNT=$(echo "$STATS" | grep -o '"cache_count":[0-9]*' | cut -d: -f2)

log "Cache warming complete. Size: $((CACHE_SIZE / 1024 / 1024))MB, Packages: $CACHE_COUNT"
```

Make it executable:

```bash
sudo chmod +x /usr/local/bin/debswarm-prewarm.sh
```

## Syncing After Repository Updates

To automatically warm the cache whenever repositories are updated, hook into APT:

```bash
sudo nano /etc/apt/apt.conf.d/99debswarm-posthook
```

```
// Download upgrades after apt update
APT::Update::Post-Invoke-Success {
    "apt-get --download-only dist-upgrade -y -qq 2>/dev/null || true";
};
```

This automatically downloads all available upgrades (caching them through debswarm) whenever `apt update` is run.

## Seeding from Existing Files

If you already have .deb files (e.g., from a local mirror), use the `seed` command instead:

```bash
# Import specific .deb files
debswarm seed import package1.deb package2.deb

# Import from APT cache
debswarm seed import /var/cache/apt/archives/*.deb

# Import entire mirror recursively
debswarm seed import --recursive /var/www/mirror/ubuntu/pool/

# Sync with mirror (add new, remove deleted packages)
debswarm seed import --recursive --sync /var/www/mirror/ubuntu/pool/
```

See [bootstrap-node.md](bootstrap-node.md) for setting up a dedicated seeder with mirror sync.

## Monitoring Cache Status

Check what's in the cache:

```bash
# Quick stats
curl http://localhost:9978/stats | jq .

# Detailed cache info
debswarm cache stats

# List cached packages
debswarm cache list

# Web dashboard
open http://localhost:9978/dashboard
```

## Best Practices

1. **Schedule during low-usage hours** - Run cache warming at night to avoid bandwidth contention

2. **Start with upgrades** - Caching `dist-upgrade` packages has the highest impact since they're what users actually need

3. **Monitor disk space** - Set appropriate `max_size` in config to prevent cache from filling disk

4. **Use bandwidth limits** - If running on a production server, limit download rates:
   ```bash
   debswarm daemon --max-download-rate 10MB/s
   ```

5. **Enable announcements** - Ensure packages are announced to the DHT:
   ```toml
   [privacy]
   announce_packages = true
   ```

## Troubleshooting

### Packages not being cached

1. Verify APT is using the proxy:
   ```bash
   apt-get -o Debug::Acquire::http=true update 2>&1 | grep -i proxy
   ```

2. Check debswarm logs:
   ```bash
   journalctl -u debswarm -f
   ```

### Cache not growing

1. Ensure debswarm is running before APT operations
2. Check cache path permissions
3. Verify disk space is available

### Downloads timing out

Increase timeout or reduce concurrent downloads:
```toml
[transfer]
max_concurrent_peer_downloads = 5
```

## Summary

| Method | Use Case | Command |
|--------|----------|---------|
| Cache upgrades | Keep network ready for updates | `apt-get --download-only dist-upgrade` |
| Cache installed | Clone machine setup | `dpkg --get-selections \| xargs apt-get download` |
| Cache specific | Pre-load known packages | `apt-get download package1 package2` |
| Seed from files | Import existing .deb files | `debswarm seed import *.deb` |
| Mirror sync | Keep in sync with local mirror | `debswarm seed import -r --sync /mirror/` |
