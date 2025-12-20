#!/bin/bash
# prewarm-mint.sh
# Pre-warm debswarm with Linux Mint specific packages
# Linux Mint doesn't have its own popcon, so this uses curated Mint packages
# plus optionally fetches from Ubuntu popcon for base packages

INCLUDE_UBUNTU_POPCON=${1:-yes}  # Set to "no" to skip Ubuntu popcon
UBUNTU_TOP_N=${2:-200}           # Number of Ubuntu packages if enabled

log() { echo "[mint-prewarm] $1"; }

# Check debswarm is running
if ! curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    log "ERROR: debswarm is not running. Start it first."
    exit 1
fi

TEMP_DIR="/tmp/mint-prewarm-$$"
mkdir -p "$TEMP_DIR"
cd "$TEMP_DIR"

cleanup() { rm -rf "$TEMP_DIR"; }
trap cleanup EXIT

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

# Cinnamon Desktop (Linux Mint default)
CINNAMON=(
    cinnamon cinnamon-common cinnamon-desktop-data
    cinnamon-control-center cinnamon-screensaver cinnamon-session
    cinnamon-settings-daemon nemo nemo-fileroller
    muffin libmuffin0
    cjs libcjs0
)
download_packages "cinnamon" "${CINNAMON[@]}"

# Linux Mint tools and apps
MINT_TOOLS=(
    mintbackup mintdesktop mintdrivers
    mintinstall mintlocale mintmenu
    mintnanny mintreport mintsources
    mintstick mintsystem mintupdate
    mintupload mintwelcome
    mint-artwork mint-backgrounds
    mint-themes mint-x-icons mint-y-icons
    webapp-manager
    hypnotix bulky sticky
    xviewer xreader xed xplayer pix
    timeshift
)
download_packages "mint-tools" "${MINT_TOOLS[@]}"

# MATE Desktop (Linux Mint MATE edition)
MATE=(
    mate-desktop mate-desktop-environment
    mate-panel mate-session-manager
    mate-settings-daemon mate-control-center
    caja caja-extensions-common
    pluma atril eom
    mate-terminal mate-system-monitor
    mate-power-manager mate-screensaver
)
download_packages "mate" "${MATE[@]}"

# Xfce Desktop (Linux Mint Xfce edition)
XFCE=(
    xfce4 xfce4-goodies xfce4-panel
    xfce4-session xfce4-settings
    xfwm4 xfdesktop4 xfce4-terminal
    thunar thunar-archive-plugin
    mousepad ristretto parole
)
download_packages "xfce" "${XFCE[@]}"

# Common Mint dependencies
MINT_DEPS=(
    python3-gi python3-xapp python3-setproctitle
    gir1.2-xapp-1.0 xapps-common
    libxapp1 libxapp-gtk3-module
    slick-greeter lightdm-settings
    gnome-system-tools system-tools-backends
)
download_packages "mint-deps" "${MINT_DEPS[@]}"

# Flatpak support (Mint uses Flatpak by default)
FLATPAK=(
    flatpak gnome-software-plugin-flatpak
)
download_packages "flatpak" "${FLATPAK[@]}"

# Optionally fetch from Ubuntu popcon
if [ "$INCLUDE_UBUNTU_POPCON" = "yes" ]; then
    log "Fetching Ubuntu popularity contest data..."
    curl -s https://popcon.ubuntu.com/by_inst.gz | gunzip > /tmp/ubuntu-popcon.txt

    if [ -s /tmp/ubuntu-popcon.txt ]; then
        log "Extracting top $UBUNTU_TOP_N Ubuntu packages..."
        UBUNTU_PACKAGES=$(tail -n +12 /tmp/ubuntu-popcon.txt | \
            head -n "$UBUNTU_TOP_N" | \
            awk '{print $2}' | \
            grep -v "^-" | \
            tr '\n' ' ')

        log "Downloading Ubuntu base packages..."
        echo "$UBUNTU_PACKAGES" | xargs -n 50 apt-get download 2>/dev/null || true
        rm -f *.deb
        rm -f /tmp/ubuntu-popcon.txt
    else
        log "WARNING: Failed to fetch Ubuntu popcon data, skipping"
    fi
fi

# Report results
STATS=$(curl -s http://localhost:9978/stats)
CACHE_SIZE=$(echo "$STATS" | grep -o '"cache_size_bytes":[0-9]*' | cut -d: -f2)
CACHE_COUNT=$(echo "$STATS" | grep -o '"cache_count":[0-9]*' | cut -d: -f2)

log "Complete! Cache: $((CACHE_SIZE / 1024 / 1024))MB, $CACHE_COUNT packages"
