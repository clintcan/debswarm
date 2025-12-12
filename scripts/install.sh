#!/bin/bash
# install.sh - Quick install script for debswarm
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/debswarm/debswarm/main/scripts/install.sh | sudo bash
#
# Or with specific version:
#   curl -sSL https://raw.githubusercontent.com/debswarm/debswarm/main/scripts/install.sh | sudo bash -s -- v0.2.0

set -e

VERSION="${1:-latest}"
GITHUB_REPO="debswarm/debswarm"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/debswarm"
SYSTEMD_DIR="/etc/systemd/system"

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    armv7l)  GOARCH="armv7" ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

echo "=== debswarm installer ==="
echo "Architecture: $ARCH ($GOARCH)"

# Get latest version if not specified
if [ "$VERSION" = "latest" ]; then
    echo "Fetching latest version..."
    VERSION=$(curl -sL "https://api.github.com/repos/$GITHUB_REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
    if [ -z "$VERSION" ]; then
        echo "Failed to get latest version"
        exit 1
    fi
fi

echo "Version: $VERSION"

# Remove 'v' prefix for filename
VERSION_NUM="${VERSION#v}"

# Download URL
DOWNLOAD_URL="https://github.com/$GITHUB_REPO/releases/download/$VERSION/debswarm_${VERSION_NUM}_linux_${GOARCH}.tar.gz"

echo "Downloading from: $DOWNLOAD_URL"

# Create temp directory
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

# Download and extract
curl -sSL "$DOWNLOAD_URL" | tar -xz -C "$TMP_DIR"

# Install binary
echo "Installing binary to $INSTALL_DIR..."
install -m 755 "$TMP_DIR/debswarm" "$INSTALL_DIR/debswarm"

# Install config
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    echo "Installing default configuration..."
    mkdir -p "$CONFIG_DIR"
    if [ -f "$TMP_DIR/dist/config.example.toml" ]; then
        cp "$TMP_DIR/dist/config.example.toml" "$CONFIG_DIR/config.toml"
    else
        # Create minimal config
        cat > "$CONFIG_DIR/config.toml" << 'EOF'
[network]
listen_port = 4001
proxy_port = 9977

[cache]
path = "/var/cache/debswarm"
max_size = "10GB"

[privacy]
enable_mdns = true
announce_packages = true

[logging]
level = "info"
EOF
    fi
fi

# Install APT config
echo "Installing APT configuration..."
if [ -f "$TMP_DIR/dist/90debswarm.conf" ]; then
    cp "$TMP_DIR/dist/90debswarm.conf" /etc/apt/apt.conf.d/
else
    cat > /etc/apt/apt.conf.d/90debswarm.conf << 'EOF'
Acquire::http::Proxy "http://127.0.0.1:9977";
EOF
fi

# Install systemd service
echo "Installing systemd service..."
if [ -f "$TMP_DIR/dist/debswarm.service" ]; then
    cp "$TMP_DIR/dist/debswarm.service" "$SYSTEMD_DIR/"
else
    cat > "$SYSTEMD_DIR/debswarm.service" << 'EOF'
[Unit]
Description=debswarm P2P APT package distribution
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/debswarm daemon
Restart=on-failure
DynamicUser=yes
StateDirectory=debswarm
CacheDirectory=debswarm
ConfigurationDirectory=debswarm

[Install]
WantedBy=multi-user.target
EOF
fi

# Reload systemd
systemctl daemon-reload

echo ""
echo "=== Installation complete ==="
echo ""
echo "To start debswarm:"
echo "  sudo systemctl enable --now debswarm"
echo ""
echo "To check status:"
echo "  sudo systemctl status debswarm"
echo "  curl http://localhost:9978/stats"
echo ""
echo "To uninstall:"
echo "  sudo systemctl disable --now debswarm"
echo "  sudo rm /usr/local/bin/debswarm"
echo "  sudo rm /etc/apt/apt.conf.d/90debswarm.conf"
echo "  sudo rm /etc/systemd/system/debswarm.service"
echo "  sudo rm -rf /etc/debswarm /var/cache/debswarm"
