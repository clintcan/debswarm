# Deploying a Bootstrap Node

Bootstrap nodes are essential for internet-wide peer discovery in debswarm. They serve as initial connection points for new nodes joining the network, helping them discover other peers through the Kademlia DHT.

## What is a Bootstrap Node?

A bootstrap node is a publicly accessible debswarm instance that:
- Runs 24/7 with a stable IP address or DNS name
- Participates in the DHT routing table
- Helps new nodes find other peers
- Does NOT need to cache packages (but can)

Bootstrap nodes don't require special software - any debswarm instance can serve as one.

## Requirements

### Server Requirements
- **OS**: Linux (Ubuntu 22.04+ or Debian 12+ recommended)
- **CPU**: 1 vCPU minimum
- **RAM**: 512MB minimum (1GB recommended)
- **Storage**: 1GB for system + optional cache storage
- **Network**: Static public IP or DNS name

### Network Requirements
- **Port 4001/UDP** - QUIC transport (preferred)
- **Port 4001/TCP** - TCP fallback
- Both ports must be accessible from the internet

## Installation

### Option 1: From Release Binary

```bash
# Download latest release
curl -sSL https://github.com/clintcan/debswarm/releases/latest/download/debswarm_linux_amd64.tar.gz | tar -xz
sudo mv debswarm /usr/local/bin/
sudo chmod +x /usr/local/bin/debswarm
```

### Option 2: From .deb Package

```bash
wget https://github.com/clintcan/debswarm/releases/latest/download/debswarm_*_amd64.deb
sudo dpkg -i debswarm_*_amd64.deb
```

### Option 3: Build from Source

```bash
git clone https://github.com/clintcan/debswarm.git
cd debswarm
make build
sudo cp build/debswarm /usr/local/bin/
```

## Configuration

Create the configuration directory and file:

```bash
sudo mkdir -p /etc/debswarm
sudo nano /etc/debswarm/config.toml
```

### Bootstrap Node Configuration

```toml
# /etc/debswarm/config.toml

[network]
# Listen on all interfaces
listen_address = "0.0.0.0"
listen_port = 4001

# Connect to other bootstrap nodes for redundancy
bootstrap_peers = [
    "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
    "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
    "/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
    "/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
]

[proxy]
# Disable proxy if this is a dedicated bootstrap node
enabled = false

[cache]
# Minimal cache for bootstrap-only node
max_size = "1GB"
path = "/var/lib/debswarm/cache"

[privacy]
# Participate in DHT but don't need to announce packages
announce_packages = false
enable_mdns = false  # Not needed for internet bootstrap

[dht]
# Longer TTL since bootstrap nodes are stable
provider_ttl = "48h"

[metrics]
enabled = true
listen_address = "127.0.0.1:9978"
```

## Firewall Configuration

### Using UFW (Ubuntu/Debian)

```bash
sudo ufw allow 4001/tcp comment 'debswarm P2P TCP'
sudo ufw allow 4001/udp comment 'debswarm P2P QUIC'
sudo ufw reload
```

### Using iptables

```bash
sudo iptables -A INPUT -p tcp --dport 4001 -j ACCEPT
sudo iptables -A INPUT -p udp --dport 4001 -j ACCEPT
sudo netfilter-persistent save
```

### Using firewalld (RHEL/Fedora)

```bash
sudo firewall-cmd --permanent --add-port=4001/tcp
sudo firewall-cmd --permanent --add-port=4001/udp
sudo firewall-cmd --reload
```

## Running the Bootstrap Node

### Manual Start (Testing)

```bash
debswarm daemon --config /etc/debswarm/config.toml --log-level debug
```

### Systemd Service (Production)

Create the service file if not installed via .deb:

```bash
sudo nano /etc/systemd/system/debswarm.service
```

```ini
[Unit]
Description=debswarm P2P package distribution daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=debswarm
Group=debswarm
ExecStart=/usr/local/bin/debswarm daemon --config /etc/debswarm/config.toml
Restart=always
RestartSec=5
LimitNOFILE=65535

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/debswarm

[Install]
WantedBy=multi-user.target
```

Create the user and directories:

```bash
sudo useradd -r -s /bin/false debswarm
sudo mkdir -p /var/lib/debswarm/cache
sudo chown -R debswarm:debswarm /var/lib/debswarm
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable debswarm
sudo systemctl start debswarm
```

## Getting Your Node's Multiaddr

After starting, get your node's peer ID and multiaddr:

```bash
# Check logs for the peer ID
journalctl -u debswarm | grep "peer ID"

# Or check the metrics endpoint
curl -s http://localhost:9978/stats | jq '.peer_id'
```

Your bootstrap node's multiaddr will be in this format:

```
# With IP address
/ip4/YOUR_PUBLIC_IP/tcp/4001/p2p/YOUR_PEER_ID
/ip4/YOUR_PUBLIC_IP/udp/4001/quic-v1/p2p/YOUR_PEER_ID

# With DNS name (recommended)
/dns4/bootstrap1.example.com/tcp/4001/p2p/YOUR_PEER_ID
```

Example:
```
/ip4/203.0.113.50/tcp/4001/p2p/12D3KooWLmLiB4AenmN2g2mHbhNXbUcNiGi99sAkSk1kAQedp8uE
```

## Distributing Your Bootstrap Node

Share your multiaddr with users to add to their config:

```toml
# User's config.toml
[network]
bootstrap_peers = [
    # Your bootstrap node
    "/ip4/203.0.113.50/tcp/4001/p2p/12D3KooWLmLiB4AenmN2g2mHbhNXbUcNiGi99sAkSk1kAQedp8uE",
    # Add multiple for redundancy
    "/dns4/bootstrap2.debswarm.example.com/tcp/4001/p2p/12D3KooW...",
]
```

## Monitoring

### Check Node Health

```bash
# Service status
sudo systemctl status debswarm

# Logs
journalctl -u debswarm -f

# Metrics
curl http://localhost:9978/metrics | grep debswarm
```

### Key Metrics to Monitor

| Metric | Description | Healthy Value |
|--------|-------------|---------------|
| `debswarm_connected_peers` | Current peer connections | > 10 |
| `debswarm_routing_table_size` | DHT routing table entries | > 20 |
| `debswarm_dht_queries_total` | DHT queries served | Increasing |

### Prometheus Integration

Add to your Prometheus config:

```yaml
scrape_configs:
  - job_name: 'debswarm-bootstrap'
    static_configs:
      - targets: ['localhost:9978']
```

## High Availability Setup

For production, deploy multiple bootstrap nodes:

1. **Geographic Distribution**: Deploy in different regions
2. **DNS Round-Robin**: Use a single DNS name pointing to multiple IPs
3. **Cross-Reference**: Each bootstrap node should list others as peers

Example multi-node setup:

```toml
# bootstrap1.example.com config
[network]
bootstrap_peers = [
    "/dns4/bootstrap2.example.com/tcp/4001/p2p/PEER_ID_2",
    "/dns4/bootstrap3.example.com/tcp/4001/p2p/PEER_ID_3",
]

# bootstrap2.example.com config
[network]
bootstrap_peers = [
    "/dns4/bootstrap1.example.com/tcp/4001/p2p/PEER_ID_1",
    "/dns4/bootstrap3.example.com/tcp/4001/p2p/PEER_ID_3",
]
```

## Troubleshooting

### Node Not Reachable

1. **Check firewall**: Ensure ports 4001/tcp and 4001/udp are open
2. **Check binding**: Verify listening on `0.0.0.0`, not `127.0.0.1`
3. **Test connectivity**:
   ```bash
   # From another machine
   nc -zv YOUR_IP 4001
   ```

### Low Peer Count

1. **Check bootstrap connectivity**: Ensure initial bootstrap peers are reachable
2. **Wait**: DHT population takes time (5-10 minutes)
3. **Check logs for errors**:
   ```bash
   journalctl -u debswarm | grep -i error
   ```

### High Memory Usage

1. **Limit connections** in config (if supported)
2. **Reduce cache size**
3. **Check for memory leaks** - report to project

## Security Considerations

1. **No sensitive data**: Bootstrap nodes don't store user data
2. **Rate limiting**: Consider adding rate limiting at firewall level
3. **Updates**: Keep debswarm updated for security fixes
4. **Monitoring**: Watch for unusual traffic patterns
5. **Isolation**: Run in a container or VM for additional isolation

## Pre-Seeding Packages (Optional)

Bootstrap nodes can also act as seeders to accelerate package distribution. Use the seed command to import packages:

```bash
# Seed from a local mirror
debswarm seed import --recursive /var/www/mirror/ubuntu/pool/

# Sync with mirror (import new, remove old packages)
debswarm seed import --recursive --sync /var/www/mirror/ubuntu/pool/

# Seed from APT cache
debswarm seed import /var/cache/apt/archives/*.deb

# Seed popular packages
apt-get download linux-image-generic nginx postgresql
debswarm seed import *.deb
```

### Automated Mirror Sync

To keep your seeder synchronized with a local mirror, set up a cron job:

```bash
# /etc/cron.d/debswarm-sync
0 */6 * * * root /usr/bin/debswarm seed import --recursive --sync /var/www/mirror/ubuntu/pool/ --announce=false >> /var/log/debswarm-sync.log 2>&1
```

This runs every 6 hours and:
- Imports new/updated packages
- Removes packages no longer in the mirror
- Skips DHT announcement (daemon handles that)

Update the config to enable announcements:

```toml
[privacy]
announce_packages = true  # Enable DHT announcements

[cache]
max_size = "100GB"  # Increase for seeding
```

See the main README for more details on the seed command.

## Docker Deployment (Alternative)

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY debswarm /usr/local/bin/
COPY config.toml /etc/debswarm/
EXPOSE 4001/tcp 4001/udp 9978/tcp
ENTRYPOINT ["debswarm", "daemon", "--config", "/etc/debswarm/config.toml"]
```

```bash
docker build -t debswarm-bootstrap .
docker run -d \
  --name debswarm \
  -p 4001:4001/tcp \
  -p 4001:4001/udp \
  -v debswarm-data:/var/lib/debswarm \
  debswarm-bootstrap
```
