# Security Hardening Guide

This guide covers debswarm's security model and provides recommendations for hardening your deployment.

## Security Model Overview

debswarm's security is built on several layers:

1. **Cryptographic Verification**: All packages verified against SHA256 hashes from GPG-signed repository metadata
2. **Multi-Source Verification**: DHT queries confirm multiple independent peers have the same package hash
3. **Network Isolation**: HTTP endpoints bind to localhost by default
4. **Input Validation**: SSRF protection, multiaddr filtering, path sanitization
5. **P2P Security**: Optional PSK encryption, peer allowlists, eclipse attack mitigation

### Trust Model

```
Debian/Ubuntu Mirror ──GPG signs──> Release file
                                          │
                                    contains hashes
                                          │
                                          ▼
                                    Packages index
                                          │
                                    SHA256 per package
                                          │
                                          ▼
              P2P Download ──verified against──> Expected SHA256
                                          │
                                    ✓ Match: Cache and serve
                                    ✗ Mismatch: Reject, blacklist peer
```

Peers cannot serve malicious packages that pass verification. The signed Release file from official mirrors is the root of trust.

### Multi-Source Verification

After downloading a package, debswarm queries the DHT to find other peers that have the same content hash. This provides defense-in-depth against sophisticated supply chain attacks:

| Attack Scenario | Hash Verification | Multi-Source Verification |
|-----------------|-------------------|---------------------------|
| Random peer serves bad package | Caught (hash mismatch) | Caught |
| Compromised mirror (pkg only) | Caught (hash mismatch) | Caught |
| Compromised mirror (pkg + index) | **Not caught** | **Detected** (no other providers) |
| Targeted attack on specific user | **Not caught** | **Detected** (hash differs from swarm) |

**How it works:**
1. Package downloaded and verified against expected SHA256
2. Background query to DHT: "Who else has this hash?"
3. If 2+ independent providers found → package is "verified"
4. If only self found → logged as "unverified" (new/rare package)
5. Results recorded in audit log and metrics

**Configuration:** Multi-source verification is enabled by default with minimal overhead (queries only, no re-download).

---

## Default Security Posture

### Network Exposure

| Component | Default Bind | Port | Exposure |
|-----------|--------------|------|----------|
| HTTP Proxy (APT) | `127.0.0.1` | 9977 | Local only |
| Metrics/Dashboard | `127.0.0.1` | 9978 | Local only |
| P2P Node | `0.0.0.0` | 4001 | All interfaces |

The HTTP endpoints are localhost-only by default. Only the P2P port (4001) accepts external connections.

### Built-in Protections

| Protection | Description | Since |
|------------|-------------|-------|
| SSRF Mitigation | Blocks localhost, private IPs, cloud metadata endpoints | v0.4.0 |
| Eclipse Attack Defense | Filters private/reserved IPs from DHT provider results | v1.6.0 |
| Hash Verification | All P2P downloads verified against expected SHA256 | v0.2.5 |
| Multi-Source Verification | Queries DHT to confirm other providers have same hash | v1.14.0 |
| Streaming Downloads | Large files stream to disk, prevents OOM attacks | v1.7.0 |
| Security Headers | Nonce-based CSP on dashboard, `script-src 'none'` on APIs, X-Frame-Options, X-XSS-Protection | v0.5.5 (CSP nonce v1.28.0) |
| MaxHeaderBytes | 1MB limit on HTTP headers (DoS protection) | v1.0.0 |
| Response Size Limit | 500MB max from mirrors | v0.5.5 |
| CONNECT Tunnel Validation | Restricts CONNECT targets to known mirrors on ports 80/443 | v1.20.0 |

---

## Hardening Recommendations

### 1. Use Private Swarm Mode (Recommended for Production)

Private swarm mode encrypts all P2P traffic with a pre-shared key (PSK) and prevents unauthorized peers from joining.

**Generate a PSK:**
```bash
debswarm psk generate > /etc/debswarm/swarm.key
chmod 600 /etc/debswarm/swarm.key
```

**Configure all nodes:**
```toml
[p2p]
psk_path = "/etc/debswarm/swarm.key"
```

**Benefits:**
- All traffic encrypted with PSK
- Only nodes with matching PSK can connect
- DHT announcements skipped (prevents info leakage)
- Mitigates eavesdropping and MITM attacks

### 2. Restrict Peer Connections (High-Security Environments)

For maximum control, specify an explicit allowlist of peer IDs:

```toml
[privacy]
# Only connect to these specific peers
peer_allowlist = [
  "12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz1234567890abcdefg",
  "12D3KooWZyXwVuTsRqPoNmLkJiHgFeDcBaZyXwVuTsRqPoNmLk",
]
```

Get your peer ID with:
```bash
debswarm identity show
```

### 3. Disable Unnecessary Features

**If you don't need LAN discovery:**
```toml
[privacy]
enable_mdns = false
```

**If you don't need the web dashboard:**
```toml
[metrics]
port = 0  # Disables metrics server entirely
```

**If you don't need pprof profiling (recommended for production):**

The pprof endpoints (`/debug/pprof/*`) are enabled by default for debugging. In production, consider:
- Keeping metrics bound to localhost (default)
- Using a reverse proxy with authentication if external access is needed
- Monitoring for unusual pprof activity

### 4. Configure Audit Logging

Enable audit logging for compliance and incident investigation:

```toml
[logging.audit]
enabled = true
path = "/var/log/debswarm/audit.log"
max_size_mb = 100
max_backups = 10
```

Audit logs use JSON Lines format, compatible with ELK, Splunk, and jq:

```bash
# View recent downloads
tail -100 /var/log/debswarm/audit.log | jq 'select(.event_type == "download_complete")'

# Find hash verification failures (potential attacks)
grep verification_failed /var/log/debswarm/audit.log | jq .

# Find packages without multi-source verification (new/rare packages)
grep multi_source_unverified /var/log/debswarm/audit.log | jq .

# Find packages verified by multiple sources
grep multi_source_verified /var/log/debswarm/audit.log | jq .
```

### 5. Network Segmentation

**Firewall Rules (iptables example):**

```bash
# Allow P2P from trusted networks only
iptables -A INPUT -p tcp --dport 4001 -s 192.168.0.0/16 -j ACCEPT
iptables -A INPUT -p udp --dport 4001 -s 192.168.0.0/16 -j ACCEPT
iptables -A INPUT -p tcp --dport 4001 -j DROP
iptables -A INPUT -p udp --dport 4001 -j DROP
```

**For systemd (recommended):**

The default systemd unit includes security hardening:

```ini
[Service]
# Filesystem restrictions
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
NoNewPrivileges=true

# Capability restrictions
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=

# System call filtering
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
```

### 6. Rate Limiting

Configure bandwidth limits to prevent resource exhaustion:

```toml
[ratelimit]
# Global limits
max_upload_rate = "100MB/s"
max_download_rate = "100MB/s"

# Per-peer limits (prevents single peer from monopolizing)
per_peer_upload_rate = "10MB/s"
per_peer_download_rate = "10MB/s"

# Adaptive rate limiting based on peer scores
adaptive_rate_limiting = true
```

### 7. Connection Limits

Prevent connection exhaustion attacks:

```toml
[network]
max_connections = 100

[transfer]
max_concurrent_uploads = 10
max_concurrent_peer_downloads = 20
```

### 8. HTTPS CONNECT Tunnel Security (v1.20+)

The HTTP CONNECT tunnel feature allows APT to access HTTPS repositories through debswarm. Security controls are built-in:

**Allowed Targets:**
- Only ports 443 and 80 are permitted
- Only known Debian/Ubuntu mirror hostnames are allowed:
  - `deb.debian.org`, `*.debian.org`
  - `archive.ubuntu.com`, `*.ubuntu.com`
  - `security.debian.org`, `security.ubuntu.com`
  - Hosts matching `mirrors.*`, `mirror.*`, `ftp.*`

**Blocked Targets:**
- `localhost`, `127.0.0.1`, `[::1]`
- Private networks: `10.*`, `172.16-31.*`, `192.168.*`
- Link-local: `169.254.*`, `fe80::`
- Cloud metadata: `metadata.*`
- IPv6 unique local: `fd00::`

**Monitoring:**
Tunnel activity is tracked via:
- Prometheus metrics: `debswarm_connect_*` (requests, failures, active tunnels, bytes)
- Audit events: `connect_tunnel_start`, `connect_tunnel_end`, `connect_tunnel_blocked`

**If CONNECT tunneling is not needed:**
CONNECT requests to non-allowed targets are automatically blocked. There is no configuration to disable CONNECT entirely, but the strict allowlist ensures only legitimate APT traffic is tunneled.

---

## Security Checklist

### Minimum (Home/Development Use)
- [ ] Keep default localhost binding for proxy/metrics
- [ ] Keep automatic hash verification (always on)
- [ ] Use firewall to restrict P2P port if needed

### Recommended (Production)
- [ ] Enable private swarm mode (PSK)
- [ ] Enable audit logging
- [ ] Configure rate limiting
- [ ] Disable mDNS if not needed
- [ ] Run as dedicated user (default with systemd)
- [ ] Keep software updated

### High Security (Enterprise/Compliance)
- [ ] All of the above, plus:
- [ ] Use peer allowlist
- [ ] Disable external DHT (`lan_only` mode)
- [ ] Network segmentation (dedicated VLAN)
- [ ] Log aggregation and monitoring
- [ ] Regular security audits
- [ ] Incident response plan

---

## Privacy Considerations

### What Information Is Shared

**Public DHT Mode (default):**
- Package SHA256 hashes you're downloading/serving
- Your peer ID and network addresses
- Connection patterns with other peers

**Private Swarm Mode (PSK):**
- Package hashes only visible to swarm members
- DHT announcements disabled
- All traffic encrypted

**mDNS (LAN discovery):**
- Broadcasts your peer ID on local network
- Disable with `enable_mdns = false` if LAN privacy matters

### Recommendations for Privacy

```toml
# Maximum privacy configuration
[p2p]
psk_path = "/etc/debswarm/swarm.key"

[privacy]
enable_mdns = false
peer_allowlist = ["..."]  # Only known peers

[network]
connectivity_mode = "lan_only"  # No external DHT
```

---

## Incident Response

### Signs of Attack

1. **Verification failures in logs**: May indicate malicious peer
2. **Unusual bandwidth usage**: Possible abuse of your node
3. **Unknown peers in metrics**: Check peer list for anomalies
4. **Hash mismatch alerts**: Potential supply chain attack
5. **Persistent "unverified" for common packages**: If popular packages consistently show as `multi_source_unverified`, investigate - this may indicate network isolation or targeted attack

### Response Steps

1. **Isolate**: Stop debswarm, disconnect from network if needed
2. **Preserve logs**: Copy `/var/log/debswarm/` for analysis
3. **Check cache integrity**:
   ```bash
   debswarm cache verify  # Verify all cached packages
   ```
4. **Blacklist suspicious peers**: Add to blocklist if identified
5. **Rotate identity** (if compromised):
   ```bash
   debswarm identity regenerate
   ```

### Peer Blacklisting

If you identify a malicious peer:

```toml
[privacy]
# Block specific peers
peer_blocklist = [
  "12D3KooWMaliciousPeerIdHere...",
]
```

Peers are automatically blacklisted temporarily (1 hour) after hash verification failures.

---

## Reporting Security Issues

If you discover a security vulnerability in debswarm:

1. **Do not** open a public GitHub issue
2. Email: debswarm-security@example.com (TODO: set up actual address)
3. Include:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
   - Suggested fix (if any)

We aim to respond within 48 hours and provide fixes within 7 days for critical issues.

---

## Related Documentation

- [Configuration Reference](configuration.md) - All config options
- [Architecture](architecture.md) - System design overview
- [Troubleshooting](troubleshooting.md) - Common issues and solutions
