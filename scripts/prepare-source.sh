#!/bin/bash
# prepare-source.sh - Prepare debswarm for Debian source package build
#
# This script prepares the source tree for building a Debian package.
# Run this on a system with network access before building.

set -e

VERSION="0.2.0"
PACKAGE="debswarm"

echo "=== Preparing $PACKAGE $VERSION for Debian packaging ==="

# Check for required tools
for cmd in go dpkg-buildpackage; do
    if ! command -v $cmd &> /dev/null; then
        echo "Error: $cmd is required but not installed."
        exit 1
    fi
done

# Ensure we're in the right directory
if [ ! -f "go.mod" ]; then
    echo "Error: Run this script from the debswarm source directory"
    exit 1
fi

# Step 1: Download and vendor dependencies
echo ""
echo "Step 1: Downloading Go dependencies..."
go mod download
go mod tidy

echo ""
echo "Step 2: Creating vendor directory..."
go mod vendor

echo ""
echo "Step 3: Verifying build works..."
mkdir -p build
go build -mod=vendor -o build/debswarm ./cmd/debswarm
echo "Build successful!"
rm -rf build

# Step 4: Create orig tarball
echo ""
echo "Step 4: Creating orig tarball..."
cd ..
ORIG_TAR="${PACKAGE}_${VERSION}.orig.tar.gz"
tar --exclude='.git' \
    --exclude='.github' \
    --exclude='build' \
    --exclude='.gocache' \
    -czvf "$ORIG_TAR" "$PACKAGE"
echo "Created: $ORIG_TAR"

cd "$PACKAGE"

echo ""
echo "=== Source preparation complete ==="
echo ""
echo "To build the source package:"
echo "  dpkg-buildpackage -S -us -uc"
echo ""
echo "To build the binary package:"
echo "  dpkg-buildpackage -b -us -uc"
echo ""
echo "Or use sbuild/pbuilder for clean builds:"
echo "  sbuild -d unstable"
echo ""
