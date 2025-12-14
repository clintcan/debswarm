# Using debmirror with debswarm

This guide explains how to set up a local Debian/Ubuntu mirror using debmirror and integrate it with debswarm for P2P distribution.

## Overview

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Official  │────▶│  debmirror  │────▶│  debswarm   │
│   Mirror    │     │  (local)    │     │   (P2P)     │
└─────────────┘     └──────┬──────┘     └──────┬──────┘
                          │                    │
                          ▼                    ▼
                   ┌─────────────┐     ┌─────────────┐
                   │ Local APT   │     │   Network   │
                   │  clients    │     │   Peers     │
                   └─────────────┘     └─────────────┘
```

**Benefits:**
- **Local mirror**: Fast LAN access to all packages
- **P2P distribution**: Share packages with remote peers via debswarm
- **Bandwidth efficiency**: Download from upstream once, serve many
- **Offline capability**: Mirror works without internet after initial sync

## Prerequisites

Install debmirror:

```bash
# Debian/Ubuntu
sudo apt-get install debmirror gnupg
```

Ensure debswarm is installed and running.

## Step 1: Set Up GPG Keyrings

debmirror requires GPG keys to verify repository signatures.

### For Debian:

```bash
# Create keyring directory
sudo mkdir -p /usr/share/keyrings

# Debian archive keys are usually already present
# If not, install them:
sudo apt-get install debian-archive-keyring
```

### For Ubuntu:

```bash
# Ubuntu keys
sudo apt-get install ubuntu-keyring
```

## Step 2: Create Mirror Directory

```bash
# Create mirror storage location
sudo mkdir -p /var/www/mirror
sudo chown $USER:$USER /var/www/mirror

# Or use a dedicated partition for large mirrors
# sudo mount /dev/sdb1 /var/www/mirror
```

## Step 3: Configure debmirror

### Minimal Debian Mirror (main only, amd64)

```bash
#!/bin/bash
# /usr/local/bin/sync-debian-mirror.sh

debmirror \
    --verbose \
    --method=https \
    --host=deb.debian.org \
    --root=debian \
    --arch=amd64 \
    --dist=bookworm,bookworm-updates,bookworm-security \
    --section=main \
    --nosource \
    --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
    --progress \
    /var/www/mirror/debian
```

### Full Debian Mirror (all sections)

```bash
#!/bin/bash
# /usr/local/bin/sync-debian-full.sh

debmirror \
    --verbose \
    --method=https \
    --host=deb.debian.org \
    --root=debian \
    --arch=amd64,arm64 \
    --dist=bookworm,bookworm-updates,bookworm-security \
    --section=main,contrib,non-free,non-free-firmware \
    --nosource \
    --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
    --progress \
    /var/www/mirror/debian
```

### Ubuntu Mirror

```bash
#!/bin/bash
# /usr/local/bin/sync-ubuntu-mirror.sh

debmirror \
    --verbose \
    --method=https \
    --host=archive.ubuntu.com \
    --root=ubuntu \
    --arch=amd64 \
    --dist=noble,noble-updates,noble-security \
    --section=main,restricted,universe,multiverse \
    --nosource \
    --keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg \
    --progress \
    /var/www/mirror/ubuntu
```

### Partial Mirror (specific packages only)

For a smaller mirror, use `--include` and `--exclude`:

```bash
#!/bin/bash
# /usr/local/bin/sync-partial-mirror.sh

debmirror \
    --verbose \
    --method=https \
    --host=deb.debian.org \
    --root=debian \
    --arch=amd64 \
    --dist=bookworm \
    --section=main \
    --nosource \
    --include='linux-image.*' \
    --include='nginx.*' \
    --include='postgresql.*' \
    --include='docker.*' \
    --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
    --progress \
    /var/www/mirror/debian-partial
```

## Step 4: Run Initial Mirror Sync

```bash
# Make script executable
sudo chmod +x /usr/local/bin/sync-debian-mirror.sh

# Run initial sync (this takes a long time for full mirrors)
sudo /usr/local/bin/sync-debian-mirror.sh
```

**Storage requirements:**
| Mirror Type | Approximate Size |
|-------------|------------------|
| Debian main (amd64) | ~60 GB |
| Debian full (amd64) | ~150 GB |
| Debian full (amd64+arm64) | ~250 GB |
| Ubuntu main (amd64) | ~80 GB |
| Ubuntu full (amd64) | ~200 GB |

## Step 5: Integrate with debswarm

After the mirror sync completes, import packages into debswarm:

```bash
# Import all packages from mirror into debswarm cache
debswarm seed import --recursive /var/www/mirror/debian/pool/

# Or for Ubuntu
debswarm seed import --recursive /var/www/mirror/ubuntu/pool/
```

### Sync Mode (Recommended)

Use `--sync` to keep debswarm cache aligned with the mirror:

```bash
# Sync: imports new packages, removes packages no longer in mirror
debswarm seed import --recursive --sync /var/www/mirror/debian/pool/
```

This ensures:
- New packages are imported
- Updated packages (new versions) are imported
- Packages removed from mirror are removed from debswarm cache

## Step 6: Automate Everything

Create a combined sync script:

```bash
#!/bin/bash
# /usr/local/bin/mirror-and-seed.sh
# Sync debmirror then update debswarm cache

set -e

LOG_FILE="/var/log/mirror-sync.log"
MIRROR_PATH="/var/www/mirror/debian"
POOL_PATH="$MIRROR_PATH/pool"

log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $1" | tee -a "$LOG_FILE"
}

log "Starting mirror sync..."

# Step 1: Sync from upstream
debmirror \
    --verbose \
    --method=https \
    --host=deb.debian.org \
    --root=debian \
    --arch=amd64 \
    --dist=bookworm,bookworm-updates,bookworm-security \
    --section=main \
    --nosource \
    --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
    "$MIRROR_PATH" 2>&1 | tee -a "$LOG_FILE"

log "Mirror sync complete. Updating debswarm cache..."

# Step 2: Sync debswarm cache with mirror
if systemctl is-active --quiet debswarm; then
    debswarm seed import --recursive --sync "$POOL_PATH" 2>&1 | tee -a "$LOG_FILE"
    log "debswarm cache updated."
else
    log "WARNING: debswarm is not running. Skipping cache update."
fi

# Step 3: Report stats
if curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    STATS=$(curl -s http://localhost:9978/stats)
    CACHE_SIZE=$(echo "$STATS" | grep -o '"cache_size_bytes":[0-9]*' | cut -d: -f2)
    CACHE_COUNT=$(echo "$STATS" | grep -o '"cache_count":[0-9]*' | cut -d: -f2)
    log "Cache stats: $((CACHE_SIZE / 1024 / 1024 / 1024))GB, $CACHE_COUNT packages"
fi

log "All done!"
```

### Schedule with Cron

```bash
# /etc/cron.d/mirror-sync
# Sync mirror and debswarm daily at 2 AM
0 2 * * * root /usr/local/bin/mirror-and-seed.sh >> /var/log/mirror-sync.log 2>&1
```

### Schedule with systemd Timer

```ini
# /etc/systemd/system/mirror-sync.service
[Unit]
Description=Sync debmirror and debswarm
After=network-online.target debswarm.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/mirror-and-seed.sh
User=root
Nice=10
IOSchedulingClass=idle
```

```ini
# /etc/systemd/system/mirror-sync.timer
[Unit]
Description=Daily mirror sync

[Timer]
OnCalendar=*-*-* 02:00:00
RandomizedDelaySec=1800
Persistent=true

[Install]
WantedBy=timers.target
```

Enable:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now mirror-sync.timer
```

## Step 7: Configure Local APT Clients

Point local machines to your mirror:

```bash
# /etc/apt/sources.list.d/local-mirror.list

# Local mirror (fastest for LAN clients)
deb http://mirror.local/debian bookworm main
deb http://mirror.local/debian bookworm-updates main
deb http://mirror.local/debian bookworm-security main
```

Or use debswarm as proxy (recommended - gets P2P benefits):

```bash
# /etc/apt/apt.conf.d/90debswarm
Acquire::http::Proxy "http://127.0.0.1:9977";
```

## Serving the Mirror via HTTP

To serve the mirror to LAN clients directly:

### Using nginx:

```nginx
# /etc/nginx/sites-available/mirror
server {
    listen 80;
    server_name mirror.local;

    root /var/www/mirror;
    autoindex on;

    location / {
        try_files $uri $uri/ =404;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/mirror /etc/nginx/sites-enabled/
sudo systemctl reload nginx
```

### Using Apache:

```apache
# /etc/apache2/sites-available/mirror.conf
<VirtualHost *:80>
    ServerName mirror.local
    DocumentRoot /var/www/mirror

    <Directory /var/www/mirror>
        Options Indexes FollowSymLinks
        AllowOverride None
        Require all granted
    </Directory>
</VirtualHost>
```

```bash
sudo a2ensite mirror
sudo systemctl reload apache2
```

## Architecture Options

### Option A: debswarm Proxy + Local Mirror

```
Internet ──▶ debmirror ──▶ Local Mirror (/var/www/mirror)
                              │
                              ▼
                         debswarm seed import
                              │
                              ▼
APT clients ──▶ debswarm proxy ──▶ Cache ──▶ P2P Network
                    │
                    └──▶ Local Mirror (fallback)
```

Configure debswarm to use local mirror as upstream:

```toml
# /etc/debswarm/config.toml
[mirror]
# Use local mirror instead of internet
upstream_mirrors = [
    "http://localhost/debian",
    "http://deb.debian.org/debian",  # fallback
]
```

### Option B: Direct Mirror Access + debswarm for P2P

```
Local clients ──▶ Local Mirror (http://mirror.local)
                       │
                       └──▶ debswarm seed import ──▶ P2P Network
                                                          │
Remote clients ─────────────────────────────────────────▶─┘
```

Local clients use mirror directly, debswarm shares to remote peers.

### Option C: Hybrid (Recommended)

```
All clients ──▶ debswarm proxy ──▶ 1. Check cache
                                   2. Check P2P peers
                                   3. Check local mirror
                                   4. Check internet mirror
```

This gives maximum flexibility and performance.

## Monitoring

### Mirror Status

```bash
# Check mirror size
du -sh /var/www/mirror/debian

# Check last sync
ls -la /var/www/mirror/debian/dists/bookworm/Release

# Verify mirror integrity
debmirror --check-gpg --dry-run ...
```

### debswarm Cache Status

```bash
# Quick stats
curl http://localhost:9978/stats | jq .

# Cache details
debswarm cache stats

# Web dashboard
open http://localhost:9978/dashboard
```

## Troubleshooting

### debmirror GPG errors

```bash
# Update keyrings
sudo apt-get update
sudo apt-get install --reinstall debian-archive-keyring

# Or import keys manually
gpg --keyserver keyserver.ubuntu.com --recv-keys <KEY_ID>
gpg --export <KEY_ID> | sudo tee /usr/share/keyrings/custom.gpg
```

### Mirror sync fails mid-way

debmirror supports resuming:

```bash
# Just run again - it resumes from where it stopped
/usr/local/bin/sync-debian-mirror.sh
```

### debswarm not importing packages

```bash
# Check debswarm is running
systemctl status debswarm

# Check seed import manually
debswarm seed import --verbose /var/www/mirror/debian/pool/main/a/apt/*.deb

# Check cache
debswarm cache list | head
```

### Disk space issues

```bash
# Check available space
df -h /var/www/mirror

# Clean old package versions (debmirror handles this automatically)
# Or reduce mirror scope (fewer sections/architectures)
```

## Summary

| Step | Command |
|------|---------|
| Install | `apt-get install debmirror` |
| Initial sync | `debmirror --host=deb.debian.org ...` |
| Import to debswarm | `debswarm seed import -r --sync /var/www/mirror/pool/` |
| Schedule | Add to cron or systemd timer |
| Serve HTTP | Configure nginx/apache (optional) |

This setup provides:
- Complete local package repository
- P2P distribution via debswarm
- Automatic sync with upstream
- Efficient bandwidth usage for your network
