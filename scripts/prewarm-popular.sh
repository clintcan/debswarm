#!/bin/bash
# prewarm-popular.sh
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
