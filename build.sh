#!/bin/bash
# Build script for debswarm
# Run this on a system with Go 1.22+ installed and network access

set -e

echo "=== debswarm Build Script ==="
echo ""

# Check Go version
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed"
    echo "Install with: sudo apt install golang-go"
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "Go version: $GO_VERSION"

# Download dependencies
echo ""
echo "=== Downloading dependencies ==="
go mod download
go mod tidy

# Build
echo ""
echo "=== Building ==="
mkdir -p build
go build -o build/debswarm ./cmd/debswarm

echo ""
echo "=== Build complete ==="
echo "Binary: build/debswarm"
echo ""
echo "To install:"
echo "  sudo cp build/debswarm /usr/bin/"
echo "  sudo cp debswarm.service /etc/systemd/system/"
echo "  sudo cp 90debswarm.conf /etc/apt/apt.conf.d/"
echo "  sudo systemctl enable debswarm"
echo "  sudo systemctl start debswarm"
