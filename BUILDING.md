# Building debswarm Debian Package

This document describes how to build the debswarm Debian package from source.

## Prerequisites

### Build Dependencies

```bash
# On Debian/Ubuntu
sudo apt-get install -y \
    build-essential \
    debhelper \
    devscripts \
    golang-go \
    libsqlite3-dev \
    pkg-config \
    git
```

### Go Version

debswarm requires Go 1.24 or later. Check your version:

```bash
go version
```

If you need a newer version:

```bash
# Ubuntu/Debian - use official Go
wget https://go.dev/dl/go1.24.6.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.24.6.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

## Building

### Quick Build (Binary Only)

```bash
# Clone or extract source
tar -xzf debswarm_0.2.0.orig.tar.gz
cd debswarm

# Prepare (downloads dependencies)
chmod +x prepare-source.sh
./prepare-source.sh

# Build binary package
dpkg-buildpackage -b -us -uc
```

The `.deb` file will be created in the parent directory.

### Source Package Build

```bash
# Prepare source
./prepare-source.sh

# Build source package (for upload to PPA/repo)
dpkg-buildpackage -S -us -uc

# This creates:
#   ../debswarm_0.2.0-1.dsc
#   ../debswarm_0.2.0-1.debian.tar.xz
#   ../debswarm_0.2.0.orig.tar.gz
```

### Clean Build with sbuild

For a clean, reproducible build:

```bash
# Install sbuild
sudo apt-get install sbuild
sudo sbuild-adduser $USER
newgrp sbuild

# Create chroot (one time)
sudo sbuild-createchroot --include=golang-go,libsqlite3-dev \
    unstable /srv/chroot/unstable-amd64 http://deb.debian.org/debian

# Build
sbuild -d unstable ../debswarm_0.2.0-1.dsc
```

### Build with pbuilder

```bash
# Install pbuilder
sudo apt-get install pbuilder

# Create base (one time)
sudo pbuilder create --distribution unstable \
    --extrapackages "golang-go libsqlite3-dev"

# Build
sudo pbuilder build ../debswarm_0.2.0-1.dsc
```

## Installation

```bash
# Install the built package
sudo dpkg -i ../debswarm_0.2.0-1_amd64.deb

# Fix any dependency issues
sudo apt-get install -f
```

## Verification

```bash
# Check package contents
dpkg -L debswarm

# Verify service
systemctl status debswarm

# Check it's working
curl http://localhost:9978/stats
```

## Development Builds

For development/testing without full Debian packaging:

```bash
# Simple build
go build -o debswarm ./cmd/debswarm

# Run locally
./debswarm daemon --log-level debug

# Test with APT
sudo ./debswarm daemon &
sudo apt-get -o Acquire::http::Proxy="http://127.0.0.1:9977" update
```

## Cross-Compilation

Build for different architectures:

```bash
# ARM64
GOOS=linux GOARCH=arm64 go build -o debswarm-arm64 ./cmd/debswarm

# ARMv7 (Raspberry Pi)
GOOS=linux GOARCH=arm GOARM=7 go build -o debswarm-armv7 ./cmd/debswarm
```

## Troubleshooting

### "go: module lookup disabled"

You need network access to download dependencies:

```bash
# Download dependencies first
go mod download
go mod vendor
```

### CGO errors

Ensure you have the SQLite development libraries:

```bash
sudo apt-get install libsqlite3-dev
```

### Permission denied on /var/cache/debswarm

The service runs as a dynamic user. The directories are created automatically by systemd. If running manually:

```bash
sudo mkdir -p /var/cache/debswarm /var/lib/debswarm
sudo chown $USER /var/cache/debswarm /var/lib/debswarm
```

## Package Contents

After installation, the package provides:

| Path | Description |
|------|-------------|
| `/usr/bin/debswarm` | Main binary |
| `/etc/apt/apt.conf.d/90debswarm.conf` | APT proxy configuration |
| `/etc/debswarm/config.toml` | Daemon configuration |
| `/lib/systemd/system/debswarm.service` | systemd service |
| `/usr/share/doc/debswarm/` | Documentation |

## Uploading to PPA (Ubuntu)

```bash
# Sign the source package
debsign ../debswarm_0.2.0-1_source.changes

# Upload to PPA
dput ppa:yourname/debswarm ../debswarm_0.2.0-1_source.changes
```

## Creating APT Repository

```bash
# Install reprepro
sudo apt-get install reprepro

# Set up repository structure
mkdir -p repo/conf
cat > repo/conf/distributions << EOF
Codename: stable
Components: main
Architectures: amd64 arm64
EOF

# Add package
reprepro -b repo includedeb stable ../debswarm_0.2.0-1_amd64.deb
```
