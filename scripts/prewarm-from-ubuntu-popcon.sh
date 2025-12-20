#!/bin/bash
# prewarm-from-ubuntu-popcon.sh
# Download top N packages from Ubuntu popularity contest

TOP_N=${1:-500}  # Default: top 500 packages

log() { echo "[ubuntu-popcon] $1"; }

# Check debswarm is running
if ! curl -s http://localhost:9978/stats > /dev/null 2>&1; then
    log "ERROR: debswarm is not running. Start it first."
    exit 1
fi

# Download Ubuntu popcon data
log "Fetching Ubuntu popularity contest data..."
curl -s https://popcon.ubuntu.com/by_inst.gz | gunzip > /tmp/ubuntu-popcon-by-inst.txt

if [ ! -s /tmp/ubuntu-popcon-by-inst.txt ]; then
    log "ERROR: Failed to fetch Ubuntu popcon data"
    exit 1
fi

# Extract package names (skip header, get top N)
log "Extracting top $TOP_N packages..."
PACKAGES=$(tail -n +12 /tmp/ubuntu-popcon-by-inst.txt | \
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
rm -f /tmp/ubuntu-popcon-by-inst.txt

log "Done! Top $TOP_N Ubuntu packages cached."
