# Pre-warming Popular Debian/Ubuntu Packages

This guide provides curated lists of the most commonly used packages and scripts to pre-warm your debswarm cache, making them immediately available to peers on your network.

## Quick Start

Download the pre-warming script and run it:

```bash
# Download and run the popular packages script
curl -sL https://raw.githubusercontent.com/clintcan/debswarm/main/scripts/prewarm-popular.sh | sudo bash
```

Or manually download specific package sets below.

## Package Lists by Category

### Essential Base Packages

These are installed on almost every Debian/Ubuntu system:

```bash
#!/bin/bash
# essential-packages.sh

ESSENTIAL=(
    # Core utilities
    coreutils util-linux procps
    bash dash
    apt dpkg
    systemd systemd-sysv

    # Networking
    iproute2 iputils-ping netbase
    curl wget ca-certificates
    openssh-client openssh-server

    # Text processing
    grep sed gawk
    less vim nano

    # Compression
    gzip bzip2 xz-utils zip unzip tar

    # System
    sudo passwd login
    cron logrotate rsyslog
)

apt-get download "${ESSENTIAL[@]}" 2>/dev/null
rm -f *.deb
```

### Development Tools

Popular among developers:

```bash
#!/bin/bash
# dev-packages.sh

DEVELOPMENT=(
    # Build essentials
    build-essential gcc g++ make
    autoconf automake libtool pkg-config
    cmake ninja-build

    # Version control
    git git-lfs subversion

    # Languages
    python3 python3-pip python3-venv python3-dev
    nodejs npm
    default-jdk default-jre maven gradle
    golang-go
    rustc cargo
    ruby ruby-dev

    # Libraries
    libssl-dev libffi-dev libxml2-dev
    zlib1g-dev libbz2-dev libreadline-dev
    libsqlite3-dev libncurses5-dev

    # Debugging
    gdb valgrind strace ltrace

    # Editors/IDEs
    vim-nox emacs-nox
)

apt-get download "${DEVELOPMENT[@]}" 2>/dev/null
rm -f *.deb
```

### Server Packages

Most commonly deployed server software:

```bash
#!/bin/bash
# server-packages.sh

SERVER=(
    # Web servers
    nginx nginx-common nginx-full
    apache2 apache2-utils libapache2-mod-php

    # Databases
    postgresql postgresql-client postgresql-contrib
    mariadb-server mariadb-client
    redis-server redis-tools
    sqlite3

    # PHP stack
    php php-fpm php-cli php-common
    php-mysql php-pgsql php-sqlite3
    php-curl php-gd php-mbstring php-xml php-zip

    # Mail
    postfix dovecot-core dovecot-imapd

    # DNS
    bind9 bind9-utils dnsutils

    # Proxy/Load balancing
    haproxy varnish squid

    # Monitoring
    prometheus prometheus-node-exporter
    grafana
    nagios4 zabbix-server-pgsql

    # Containers
    docker.io docker-compose containerd runc
    podman buildah skopeo

    # Configuration management
    ansible puppet chef
)

apt-get download "${SERVER[@]}" 2>/dev/null
rm -f *.deb
```

### Desktop Packages

Popular desktop applications:

```bash
#!/bin/bash
# desktop-packages.sh

DESKTOP=(
    # Desktop environments
    gnome-shell gnome-session gnome-terminal
    plasma-desktop kde-standard
    xfce4 xfce4-goodies

    # Browsers
    firefox firefox-esr
    chromium chromium-browser

    # Office
    libreoffice-writer libreoffice-calc
    libreoffice-impress libreoffice-draw
    libreoffice-common libreoffice-core

    # Media
    vlc vlc-plugin-base
    gimp gimp-data
    inkscape
    audacity
    obs-studio
    ffmpeg

    # Communication
    thunderbird

    # Utilities
    gnome-disk-utility gparted
    file-roller p7zip-full
    gnome-screenshot flameshot

    # Fonts
    fonts-liberation fonts-dejavu
    fonts-noto fonts-ubuntu
    ttf-mscorefonts-installer
)

apt-get download "${DESKTOP[@]}" 2>/dev/null
rm -f *.deb
```

### Security and Networking Tools

Essential for sysadmins and security professionals:

```bash
#!/bin/bash
# security-packages.sh

SECURITY=(
    # Firewalls
    ufw iptables nftables
    fail2ban

    # TLS/Crypto
    openssl libssl3
    gnupg gnupg-agent
    certbot python3-certbot-nginx python3-certbot-apache

    # Network tools
    nmap netcat-openbsd
    tcpdump wireshark-common tshark
    mtr-tiny traceroute
    iftop nethogs vnstat

    # Security scanning
    clamav clamav-daemon
    rkhunter chkrootkit
    lynis

    # VPN
    openvpn wireguard wireguard-tools

    # SSH tools
    openssh-server openssh-client
    sshpass ssh-import-id

    # Authentication
    libpam-google-authenticator
)

apt-get download "${SECURITY[@]}" 2>/dev/null
rm -f *.deb
```

### Kernel and Boot

Frequently updated packages:

```bash
#!/bin/bash
# kernel-packages.sh

# Get latest kernel versions for current architecture
ARCH=$(dpkg --print-architecture)

# Latest generic kernels (adjust version as needed)
KERNELS=$(apt-cache search "linux-image-[0-9].*-generic" 2>/dev/null | \
    awk '{print $1}' | sort -V | tail -3)

# Kernel headers for building modules
HEADERS=$(apt-cache search "linux-headers-[0-9].*-generic" 2>/dev/null | \
    awk '{print $1}' | sort -V | tail -3)

BOOT=(
    # Bootloader
    grub-pc grub-efi-amd64 grub-common grub2-common
    shim-signed

    # Initramfs
    initramfs-tools initramfs-tools-core

    # Firmware
    linux-firmware
    firmware-linux firmware-linux-free firmware-linux-nonfree
    intel-microcode amd64-microcode
)

apt-get download $KERNELS $HEADERS "${BOOT[@]}" 2>/dev/null
rm -f *.deb
```

## Complete Pre-warming Script

This script downloads all popular packages across categories:

```bash
#!/bin/bash
# /usr/local/bin/prewarm-popular.sh
# Pre-warm debswarm with popular Debian/Ubuntu packages

set -e

LOG_PREFIX="[prewarm-popular]"
TEMP_DIR="/tmp/prewarm-popular-$$"

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $LOG_PREFIX $1"; }

cleanup() { rm -rf "$TEMP_DIR"; }
trap cleanup EXIT

# Check debswarm is running
if ! curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    log "ERROR: debswarm is not running. Start it first."
    exit 1
fi

mkdir -p "$TEMP_DIR"
cd "$TEMP_DIR"

log "Updating package lists..."
apt-get update -qq

# Function to download packages (ignores missing ones)
download_packages() {
    local category="$1"
    shift
    local packages=("$@")

    log "Downloading $category packages..."
    apt-get download "${packages[@]}" 2>/dev/null || true
    local count=$(ls -1 *.deb 2>/dev/null | wc -l)
    log "  Downloaded $count packages for $category"
    rm -f *.deb
}

# Essential packages
ESSENTIAL=(
    coreutils util-linux procps bash apt dpkg systemd
    iproute2 curl wget ca-certificates openssh-client openssh-server
    grep sed gawk less vim nano
    gzip bzip2 xz-utils zip unzip tar
    sudo passwd cron logrotate
)
download_packages "essential" "${ESSENTIAL[@]}"

# Development packages
DEVELOPMENT=(
    build-essential gcc g++ make cmake
    git python3 python3-pip python3-venv
    nodejs npm default-jdk golang-go
    libssl-dev zlib1g-dev
    gdb strace
)
download_packages "development" "${DEVELOPMENT[@]}"

# Server packages
SERVER=(
    nginx apache2
    postgresql postgresql-client mariadb-server mariadb-client
    redis-server sqlite3
    php php-fpm php-cli php-mysql php-pgsql
    docker.io docker-compose containerd
    ansible
)
download_packages "server" "${SERVER[@]}"

# Security packages
SECURITY=(
    ufw iptables fail2ban
    openssl gnupg certbot
    nmap tcpdump mtr-tiny
    openvpn wireguard wireguard-tools
    clamav
)
download_packages "security" "${SECURITY[@]}"

# Desktop packages (optional - comment out for servers)
DESKTOP=(
    firefox chromium
    libreoffice-writer libreoffice-calc
    vlc gimp ffmpeg
    fonts-liberation fonts-dejavu
)
download_packages "desktop" "${DESKTOP[@]}"

# Latest kernels
log "Downloading kernel packages..."
KERNELS=$(apt-cache search "linux-image-[0-9].*-generic" 2>/dev/null | \
    awk '{print $1}' | sort -V | tail -2)
apt-get download $KERNELS linux-firmware 2>/dev/null || true
rm -f *.deb

# Available upgrades
log "Downloading available upgrades..."
apt-get --download-only dist-upgrade -y -qq 2>/dev/null || true

# Report results
STATS=$(curl -s http://localhost:9978/stats)
CACHE_SIZE=$(echo "$STATS" | grep -o '"cache_size_bytes":[0-9]*' | cut -d: -f2)
CACHE_COUNT=$(echo "$STATS" | grep -o '"cache_count":[0-9]*' | cut -d: -f2)

log "Complete! Cache: $((CACHE_SIZE / 1024 / 1024))MB, $CACHE_COUNT packages"
```

## Using Debian Popularity Contest Data

Debian tracks package usage via the [Popularity Contest](https://popcon.debian.org/). You can use this data to download the most-installed packages:

```bash
#!/bin/bash
# prewarm-from-popcon.sh
# Download top N packages from Debian popularity contest

TOP_N=${1:-500}  # Default: top 500 packages

log() { echo "[popcon] $1"; }

# Download popcon data
log "Fetching popularity contest data..."
curl -s https://popcon.debian.org/by_inst.gz | gunzip > /tmp/popcon-by-inst.txt

# Extract package names (skip header, get top N)
log "Extracting top $TOP_N packages..."
PACKAGES=$(tail -n +12 /tmp/popcon-by-inst.txt | \
    head -n "$TOP_N" | \
    awk '{print $2}' | \
    grep -v "^-" | \
    tr '\n' ' ')

# Download packages
log "Downloading packages through debswarm..."
cd /tmp
echo "$PACKAGES" | xargs -n 50 apt-get download 2>/dev/null || true
rm -f *.deb

log "Done! Top $TOP_N packages cached."
```

## Ubuntu-Specific Popular Packages

For Ubuntu systems, include these additional packages:

```bash
#!/bin/bash
# ubuntu-packages.sh

UBUNTU_SPECIFIC=(
    # Ubuntu base
    ubuntu-minimal ubuntu-standard
    ubuntu-desktop ubuntu-desktop-minimal

    # Snap support (if needed)
    snapd

    # Ubuntu drivers
    ubuntu-drivers-common

    # PPAs support
    software-properties-common

    # Ubuntu fonts and themes
    ubuntu-wallpapers
    yaru-theme-gnome-shell yaru-theme-gtk yaru-theme-icon
    fonts-ubuntu

    # Ubuntu-specific tools
    apport whoopsie
    update-manager-core unattended-upgrades
    landscape-common
)

apt-get download "${UBUNTU_SPECIFIC[@]}" 2>/dev/null
rm -f *.deb
```

## Scheduling Popular Package Updates

Keep popular packages up-to-date with a weekly refresh:

```bash
# /etc/cron.d/debswarm-popular
# Refresh popular packages weekly on Sunday at 2 AM
0 2 * * 0 root /usr/local/bin/prewarm-popular.sh >> /var/log/debswarm-popular.log 2>&1
```

Or with systemd timer:

```ini
# /etc/systemd/system/debswarm-popular.timer
[Unit]
Description=Weekly popular packages pre-warm

[Timer]
OnCalendar=Sun 02:00:00
RandomizedDelaySec=3600
Persistent=true

[Install]
WantedBy=timers.target
```

## Storage Requirements

Approximate cache sizes for each category:

| Category | Package Count | Approximate Size |
|----------|---------------|------------------|
| Essential | ~50 | 200-300 MB |
| Development | ~60 | 500-800 MB |
| Server | ~80 | 300-500 MB |
| Desktop | ~100 | 1-2 GB |
| Security | ~40 | 100-200 MB |
| Kernels | ~10 | 500-800 MB |
| **Total** | ~340 | **3-5 GB** |

With dependencies, expect the cache to grow to **5-10 GB** for a comprehensive popular package set.

## Customizing for Your Environment

Create a custom package list based on your organization's needs:

```bash
# /etc/debswarm/popular-packages.txt
# One package per line, comments start with #

# Our standard development stack
git
docker.io
python3
nodejs

# Internal tools
our-internal-package

# Department-specific
matlab-runtime
```

Then use it:

```bash
#!/bin/bash
# prewarm-custom.sh
PACKAGES=$(grep -v '^#' /etc/debswarm/popular-packages.txt | tr '\n' ' ')
cd /tmp
echo "$PACKAGES" | xargs apt-get download 2>/dev/null || true
rm -f *.deb
```

## Monitoring Progress

Watch the cache grow during pre-warming:

```bash
# Terminal 1: Run pre-warming
sudo /usr/local/bin/prewarm-popular.sh

# Terminal 2: Watch cache stats
watch -n 5 'curl -s http://localhost:9978/stats | jq "{cache_size_mb: (.cache_size_bytes/1024/1024), cache_count, connected_peers}"'
```

Or view the dashboard at `http://localhost:9978/dashboard`.
