#!/bin/bash
# prewarm-from-popcon.sh
# Download top N packages from Debian popularity contest

TOP_N=${1:-5000}  # Default: top 5000 packages

log() { echo "[popcon] $1"; }

# Check debswarm is running
if ! curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    log "ERROR: debswarm is not running. Start it first."
    exit 1
fi

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

# Cleanup
rm -f /tmp/popcon-by-inst.txt

log "Done! Top $TOP_N packages cached."
