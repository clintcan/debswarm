# Configuration Reference

This document provides a complete reference for all debswarm configuration options.

## Configuration File Locations

debswarm searches for configuration files in the following order (first found wins):

1. Path specified via `--config` flag
2. `/etc/debswarm/config.toml` (system-wide)
3. `~/.config/debswarm/config.toml` (user-specific)

If no configuration file is found, debswarm uses sensible defaults.

### Interactive Wizard (v1.29+)

For new installations, the configuration wizard provides a guided setup:

```bash
debswarm config wizard                      # interactive prompts
debswarm config wizard --output /tmp/c.toml # custom output path
```

The wizard offers 3 deployment profiles (Home, Seeding server, Private swarm), then walks through cache size, bandwidth limits, ports, mDNS, fleet coordination, and log level with inline validation.

## Environment Variables

The following environment variables override configuration file settings:

| Variable | Description |
|----------|-------------|
| `CACHE_DIRECTORY` | Cache directory path (used by systemd `CacheDirectory=`) |
| `STATE_DIRECTORY` | Data directory for identity keys (used by systemd `StateDirectory=`) |

## Configuration Sections

### [network]

Network settings for P2P communication and the HTTP proxy.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `listen_port` | integer | `4001` | P2P listen port for incoming connections. Uses both UDP (QUIC) and TCP. |
| `proxy_port` | integer | `9977` | HTTP proxy port for APT requests. APT connects to `http://127.0.0.1:<port>`. |
| `max_connections` | integer | `100` | Maximum number of concurrent P2P connections. Prevents resource exhaustion. |
| `bootstrap_peers` | string[] | libp2p defaults | List of bootstrap peer multiaddrs for DHT initialization. |
| `connectivity_mode` | string | `"auto"` | Connectivity mode: `"auto"`, `"lan_only"`, or `"online_only"`. |
| `connectivity_check_interval` | string | `"30s"` | How often to check connectivity in auto mode. |
| `connectivity_check_url` | string | `"https://deb.debian.org"` | URL for connectivity checks. |
| `enable_relay` | boolean | `true` | Enable circuit relay to reach NAT'd peers via relay nodes. (v1.13+) |
| `enable_hole_punching` | boolean | `true` | Enable direct NAT hole punching for peer-to-peer connections. (v1.13+) |

**Example:**
```toml
[network]
listen_port = 4001
proxy_port = 9977
max_connections = 100

# Connectivity detection mode (v1.8+)
connectivity_mode = "auto"           # "auto", "lan_only", "online_only"
connectivity_check_interval = "30s"
# connectivity_check_url = "https://deb.debian.org"

# NAT traversal (v1.13+)
enable_relay = true         # Use circuit relays to reach NAT'd peers
enable_hole_punching = true # Enable direct NAT hole punching

# Bootstrap peers (libp2p public nodes)
bootstrap_peers = [
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
]
```

**Connectivity Modes (v1.8+):**
| Mode | Description |
|------|-------------|
| `auto` | Automatically detect connectivity. Uses DHT + mirrors when online, falls back to mDNS peers only when internet is unavailable. |
| `lan_only` | Only use mDNS-discovered peers. Never try DHT or remote mirrors. Useful for air-gapped networks. |
| `online_only` | Require internet connectivity. Fail requests if mirrors are unreachable (no LAN-only fallback). |

**NAT Traversal (v1.13+):**

debswarm uses libp2p's NAT traversal techniques to connect peers behind NAT/firewalls:

| Option | Description |
|--------|-------------|
| `enable_relay` | Allows connecting to NAT'd peers via public relay nodes. The relay forwards traffic when direct connection fails. This is client-only - debswarm uses relays but doesn't act as one. |
| `enable_hole_punching` | Attempts to establish direct connections through NAT using coordinated hole punching. More efficient than relays when successful. |

Both are enabled by default. Disable if you're on a restricted network that blocks these techniques.

**Notes:**
- The `listen_port` should be accessible through your firewall for incoming P2P connections
- QUIC (UDP) is preferred over TCP for better NAT traversal
- Custom bootstrap peers can be added for private networks or to improve connectivity
- Multiaddr format: `/ip4/<ip>/tcp/<port>/p2p/<peerID>` or `/dnsaddr/<domain>/p2p/<peerID>`

**HTTPS Proxy Configuration (v1.20+):**

debswarm supports HTTP CONNECT tunneling for HTTPS repositories. To use APT with HTTPS repos through debswarm:

```bash
# Configure APT to use proxy for both HTTP and HTTPS
cat <<EOF | sudo tee /etc/apt/apt.conf.d/00debswarm
Acquire::http::Proxy "http://127.0.0.1:9977";
Acquire::https::Proxy "http://127.0.0.1:9977";
EOF
```

When APT requests an HTTPS URL, the proxy creates a TCP tunnel to the target server, allowing encrypted traffic to pass through. This enables APT to update package lists from HTTPS sources while debswarm indexes the metadata for P2P package discovery.

**Tunnel Security:**
- Only ports 443 and 80 are allowed
- Only known Debian/Ubuntu/Mint mirrors are permitted by default
- Additional hosts can be configured via `[proxy] allowed_hosts`
- Private/internal addresses are blocked (SSRF protection)

**Configuring Additional Repository Hosts:**

Third-party Debian-style repositories (Docker, PPAs, PostgreSQL, etc.) can be allowed via configuration:

```toml
[proxy]
# Additional Debian-style repository hosts to allow through the proxy
# These must still use /dists/ or /pool/ URL patterns
allowed_hosts = [
  "download.docker.com",
  "ppa.launchpad.net",
  "apt.postgresql.org",
  "deb.nodesource.com",
]
```

Alternatively, you can bypass the proxy entirely for these hosts in APT:

```
// Add to /etc/apt/apt.conf.d/90debswarm
Acquire::http::Proxy::download.docker.com "DIRECT";
Acquire::https::Proxy::download.docker.com "DIRECT";
```

See [Troubleshooting: Third-party repositories](troubleshooting.md#third-party-repositories-failing) for more details.

---

### [proxy]

Settings for the HTTP proxy behavior.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `allowed_hosts` | string[] | `[]` | Additional repository hostnames to allow through the proxy. These hosts must still use Debian-style URL patterns (`/dists/`, `/pool/`). |

**Example:**
```toml
[proxy]
# Allow third-party Debian-style repositories through the proxy
allowed_hosts = [
  "download.docker.com",
  "ppa.launchpad.net",
  "apt.postgresql.org",
  "deb.nodesource.com",
  "packages.microsoft.com",
]
```

**Built-in Allowed Hosts:**

The following hosts are always allowed (no configuration needed):
- `deb.debian.org`, `*.debian.org`
- `archive.ubuntu.com`, `*.ubuntu.com`
- `packages.linuxmint.com`, `*.linuxmint.com`
- `mirrors.*`, `mirror.*`, `ftp.*`

**Security Notes:**
- Configured hosts must still use Debian-style URL patterns (`/dists/`, `/pool/`, etc.)
- Private/internal hosts (localhost, 10.x.x.x, 192.168.x.x, etc.) are always blocked
- Only ports 443 and 80 are allowed for HTTPS CONNECT tunnels

---

### [cache]

Settings for the local package cache.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | `~/.cache/debswarm` | Directory for cached packages and database. |
| `max_size` | string | `"10GB"` | Maximum total size of cached packages. Supports KB, MB, GB, TB suffixes. |
| `min_free_space` | string | `"1GB"` | Minimum free disk space to maintain. Cache writes fail if this limit would be violated. |

**Example:**
```toml
[cache]
path = "/var/cache/debswarm"
max_size = "50GB"
min_free_space = "2GB"
```

**Size Format:**
- Supports suffixes: `KB`, `K`, `MB`, `M`, `GB`, `G`, `TB`, `T`
- Examples: `"10GB"`, `"500MB"`, `"1TB"`
- Uses binary units (1 GB = 1024 MB = 1,073,741,824 bytes)

**Notes:**
- When running as a systemd service, the `CACHE_DIRECTORY` environment variable overrides this setting
- The cache uses content-addressed storage with SHA256 hashes
- LRU eviction with popularity boost removes old packages when space is needed
- Package metadata is stored in SQLite; package files are stored on disk

---

### [transfer]

Settings for upload/download behavior and rate limiting.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_upload_rate` | string | `"0"` | Maximum upload bandwidth. `"0"` or `"unlimited"` = no limit. |
| `max_download_rate` | string | `"0"` | Maximum download bandwidth. `"0"` or `"unlimited"` = no limit. |
| `per_peer_upload_rate` | string | `"auto"` | Per-peer upload rate limit. `"auto"` = global/expected_peers. |
| `per_peer_download_rate` | string | `"auto"` | Per-peer download rate limit. `"auto"` = global/expected_peers. |
| `expected_peers` | integer | `10` | Expected number of peers for auto-calculating per-peer limits. |
| `adaptive_rate_limiting` | boolean | auto | Enable adaptive rate adjustment. Default: enabled when per-peer is active. |
| `adaptive_min_rate` | string | `"100KB/s"` | Minimum rate floor for adaptive reduction. |
| `adaptive_max_boost` | float | `1.5` | Maximum boost factor for high-performing peers (1.5 = 50% boost). |
| `max_concurrent_uploads` | integer | `20` | Maximum simultaneous uploads to other peers. |
| `max_concurrent_peer_downloads` | integer | `10` | Maximum simultaneous chunk downloads from peers. |
| `retry_max_attempts` | integer | `3` | Maximum retry attempts for failed downloads. `0` = disabled. |
| `retry_interval` | string | `"5m"` | How often to check for failed downloads to retry. |
| `retry_max_age` | string | `"1h"` | Maximum age of failed downloads to retry. Older failures are ignored. |

**Example:**
```toml
[transfer]
max_upload_rate = "10MB/s"
max_download_rate = "50MB/s"

# Per-peer rate limiting
per_peer_upload_rate = "auto"       # = 10MB/s / 10 peers = 1MB/s per peer
per_peer_download_rate = "auto"
expected_peers = 10

# Adaptive rate limiting (enabled by default)
# adaptive_rate_limiting = true
adaptive_min_rate = "100KB/s"
adaptive_max_boost = 1.5

max_concurrent_uploads = 20
max_concurrent_peer_downloads = 10

# Automatic retry for failed downloads
retry_max_attempts = 3
retry_interval = "5m"
retry_max_age = "1h"
```

**Rate Format:**
- Supports suffixes: `KB/s`, `MB/s`, `GB/s` (or without `/s`)
- Examples: `"10MB/s"`, `"500KB"`, `"1GB/s"`
- Special values: `"0"`, `""`, `"unlimited"` = no rate limit

**Duration Format:**
- Go duration format: `"5m"` (5 minutes), `"1h"` (1 hour), `"30s"` (30 seconds)
- Combinations: `"1h30m"` (1 hour 30 minutes)

**Per-Peer Rate Limiting:**
- Prevents any single peer from monopolizing your bandwidth
- `"auto"` divides global limit by `expected_peers`
- `"0"` disables per-peer limiting (only global limits apply)
- Specific rate like `"5MB/s"` sets a fixed per-peer limit
- Both global and per-peer limits are enforced (stricter limit wins)

**Adaptive Rate Limiting:**
- Automatically adjusts per-peer rates based on peer performance
- High-performing peers (good latency, throughput, reliability) get boosted rates
- Poorly-performing peers get reduced rates (down to `adaptive_min_rate`)
- Congestion detection: rates reduced when latency exceeds 500ms
- Adjustment range: 0.5x to 1.5x of base rate (±50%)
- Recalculates every 10 seconds based on peer scoring metrics

**Notes:**
- Global rate limits apply to total bandwidth across all peers
- Per-peer limits ensure fair bandwidth distribution
- Concurrent limits prevent overwhelming peers or your network
- The retry worker runs in the background and picks up failed downloads automatically
- Already-completed chunks are preserved when retrying failed downloads

---

### [dht]

Settings for the Kademlia Distributed Hash Table (DHT).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider_ttl` | string | `"24h"` | How long provider records (package announcements) remain in the DHT. |
| `announce_interval` | string | `"12h"` | How often to re-announce cached packages to the DHT. |

**Example:**
```toml
[dht]
provider_ttl = "24h"
announce_interval = "12h"
```

**Notes:**
- Provider records tell other peers that you have a specific package
- `announce_interval` should be less than `provider_ttl` to ensure continuous availability
- Shorter intervals increase DHT traffic but improve discoverability
- On startup, all cached packages are announced to the DHT

---

### [privacy]

Settings for network privacy and access control.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enable_mdns` | boolean | `true` | Enable mDNS for local network peer discovery. |
| `announce_packages` | boolean | `true` | Announce cached packages to the DHT (allow uploads to other peers). |
| `psk_path` | string | `""` | Path to Pre-Shared Key file for private swarm. |
| `psk` | string | `""` | Inline Pre-Shared Key (hex format). Mutually exclusive with `psk_path`. |
| `peer_allowlist` | string[] | `[]` | List of allowed peer IDs. Empty = allow all peers. |
| `peer_blocklist` | string[] | `[]` | List of blocked peer IDs. Connections from these peers are always rejected. |

**Example:**
```toml
[privacy]
enable_mdns = true
announce_packages = true

# Private swarm configuration (choose one method)
psk_path = "/etc/debswarm/swarm.key"
# psk = "0123456789abcdef..."  # Not recommended - use psk_path instead

# Restrict to specific peers (optional)
peer_allowlist = [
  "12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz...",
  "12D3KooWBcDeFgHiJkLmNoPqRsTuVwXyZa...",
]

# Block specific peers (optional)
peer_blocklist = [
  "12D3KooWMaliciousPeerIdHere...",
]
```

**Private Swarm (PSK):**
- Generate a PSK with: `debswarm psk generate -o /etc/debswarm/swarm.key`
- All nodes in the private swarm must use the same PSK
- Nodes without the PSK cannot connect to your swarm
- PSK provides network isolation, not encryption (libp2p connections are already encrypted)

**Peer Allowlist:**
- Provides additional filtering beyond PSK
- Peer IDs can be found with: `debswarm identity show`
- Empty list means all peers are allowed (subject to PSK if configured)

**Peer Blocklist:**
- Blocks specific peers regardless of other settings
- Useful for blocking malicious or misbehaving peers
- Blocklist is checked before allowlist (blocked peers are always rejected)

**Notes:**
- Set `announce_packages = false` to run in download-only mode (no sharing)
- Disable mDNS (`enable_mdns = false`) if you don't want LAN discovery
- Using inline PSK (`psk`) is not recommended as config files may be world-readable

---

### [metrics]

Settings for the metrics and dashboard server.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `port` | integer | `9978` | Port for metrics, dashboard, and health endpoints. `0` = disabled. |
| `bind` | string | `"127.0.0.1"` | Bind address for the metrics server. |

**Example:**
```toml
[metrics]
port = 9978
bind = "127.0.0.1"
```

**Endpoints:**
| Endpoint | Description |
|----------|-------------|
| `/dashboard` | Real-time HTML dashboard |
| `/metrics` | Prometheus metrics |
| `/stats` | Quick JSON status |
| `/health` | Health check endpoint (returns 200 OK or 503) |
| `/debug/pprof/` | Runtime profiling (pprof) |

**Security Warning:**
The metrics endpoint exposes operational information including:
- Cache statistics and hit rates
- Connected peer counts and IDs
- Download/upload statistics
- Network performance data
- Runtime profiling data

**Recommendations:**
- Keep `bind = "127.0.0.1"` unless you need remote access
- If exposing externally (`bind = "0.0.0.0"`), use a reverse proxy with authentication
- For seeding servers, you may want to expose the dashboard for monitoring

---

### [logging]

Settings for log output.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `"info"` | Log verbosity level. Options: `debug`, `info`, `warn`, `error`. |
| `file` | string | `""` | Log file path. Empty = log to stderr. |

**Example:**
```toml
[logging]
level = "info"
file = "/var/log/debswarm/debswarm.log"
```

**Log Levels:**
| Level | Description |
|-------|-------------|
| `debug` | Detailed debugging information (very verbose) |
| `info` | Normal operational messages |
| `warn` | Warnings that don't prevent operation |
| `error` | Errors that may affect functionality |

**Notes:**
- When running as a systemd service, logs go to journald regardless of file setting
- Use `journalctl -u debswarm -f` to view logs in real-time
- Debug level is useful for troubleshooting but generates significant output

---

### [logging.audit]

Settings for structured audit logging (v1.8+).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable structured audit logging. |
| `path` | string | `""` | Path for JSON audit log file. |
| `max_size_mb` | integer | `100` | Maximum file size before rotation (MB). |
| `max_backups` | integer | `5` | Number of rotated backup files to keep. |

**Example:**
```toml
[logging.audit]
enabled = true
path = "/var/log/debswarm/audit.json"
max_size_mb = 100
max_backups = 5
```

**Events Logged:**
| Event Type | Description |
|------------|-------------|
| `download_complete` | Package download succeeded (includes source, bytes, duration) |
| `download_failed` | Package download failed (includes error message) |
| `upload_complete` | Package served to another peer |
| `cache_hit` | Package served from local cache |
| `verification_failed` | Hash mismatch detected (peer blacklisted) |
| `peer_blacklisted` | Peer added to blacklist |

**Log Format:**
The audit log uses JSON Lines format (one JSON object per line), compatible with tools like `jq`, ELK stack, and Splunk.

**Example audit log entry:**
```json
{"timestamp":"2025-12-18T10:30:45Z","event_type":"download_complete","package_hash":"abc123...","package_name":"pool/main/c/curl/curl_7.88.1.deb","package_size":1567890,"source":"peer","duration_ms":1234,"bytes_p2p":1500000,"bytes_mirror":67890}
```

**Notes:**
- The directory will be created if it doesn't exist
- Rotation creates backup files with `.1`, `.2`, etc. suffixes
- Oldest backups are deleted when `max_backups` is exceeded

---

### [scheduler]

Settings for scheduled sync windows (v1.9+). Allows rate limiting based on time of day.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable scheduled sync windows. |
| `timezone` | string | system | IANA timezone (e.g., `"America/New_York"`). |
| `outside_window_rate` | string | `"100KB/s"` | Rate limit outside sync windows. |
| `inside_window_rate` | string | `"unlimited"` | Rate limit inside sync windows. |
| `urgent_always_full_speed` | boolean | `true` | Security updates bypass rate limits. |
| `windows` | array | `[]` | List of sync window definitions. |

**Window Definition:**
| Field | Type | Description |
|-------|------|-------------|
| `days` | string[] | Days of week: `"monday"` through `"sunday"`, or `"weekday"`, `"weekend"` |
| `start_time` | string | Start time in 24h format: `"22:00"` |
| `end_time` | string | End time in 24h format: `"06:00"` |

**Example:**
```toml
[scheduler]
enabled = true
timezone = "America/New_York"
outside_window_rate = "100KB/s"
inside_window_rate = "unlimited"
urgent_always_full_speed = true

[[scheduler.windows]]
days = ["weekday"]
start_time = "22:00"
end_time = "06:00"

[[scheduler.windows]]
days = ["saturday", "sunday"]
start_time = "00:00"
end_time = "23:59"
```

**Notes:**
- Windows can span midnight (e.g., 22:00 to 06:00)
- Security updates (from `-security` repos) always get full speed by default
- Rate limiting applies to both P2P downloads and mirror fetches
- Useful for reducing bandwidth usage during business hours

---

### [fleet]

Settings for LAN fleet coordination (v1.9+, fully wired in v1.25+). Prevents redundant WAN downloads across peers.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable fleet coordination. |
| `claim_timeout` | string | `"5s"` | Time to wait for a peer to claim WAN download responsibility. |
| `max_wait_time` | string | `"5m"` | Maximum time to wait for a peer to complete WAN download. |
| `allow_concurrent` | integer | `1` | Number of concurrent WAN fetchers allowed per package. |
| `refresh_interval` | string | `"1s"` | Progress broadcast interval. |

**Example:**
```toml
[fleet]
enabled = true
claim_timeout = "5s"
max_wait_time = "5m"
allow_concurrent = 1
refresh_interval = "1s"
```

**How Fleet Coordination Works:**
1. When a package is needed, peers coordinate via mDNS
2. One peer "claims" responsibility to fetch from WAN
3. Other peers wait and receive the package via P2P once downloaded
4. If the claiming peer fails, another peer takes over

**Notes:**
- Requires mDNS to be enabled (`privacy.enable_mdns = true`)
- Only useful when multiple debswarm instances are on the same LAN
- Significantly reduces bandwidth for organizations with many machines
- Falls back gracefully if coordination fails

---

### [index]

Settings for package index management and APT integration (v1.18+).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `apt_lists_path` | string | `"/var/lib/apt/lists"` | Path to APT's package lists directory. Parsed on startup to populate the package index. |
| `watch_apt_lists` | boolean | `true` | Watch APT lists for changes and re-parse when `apt update` runs. |
| `apt_archives_path` | string | `"/var/cache/apt/archives"` | Path to APT's package cache (downloaded .deb files). (v1.19+) |
| `import_apt_archives` | boolean | `true` | Import packages from APT's cache into debswarm's cache on startup. (v1.19+) |

**Example:**
```toml
[index]
# APT package lists (Packages files with hash index)
apt_lists_path = "/var/lib/apt/lists"
watch_apt_lists = true

# APT package cache (downloaded .deb files) - v1.19+
apt_archives_path = "/var/cache/apt/archives"
import_apt_archives = true
```

**APT Lists Auto-Indexing (v1.18+):**

debswarm parses APT's local package lists (`/var/lib/apt/lists/*_Packages`) to build a hash index:
- Enables P2P downloads even when `apt update` doesn't go through the proxy
- Automatically re-parses when APT updates the lists (via fsnotify)
- Debounces rapid changes during `apt update` (2 second delay)
- Extracts repository identifiers from APT list filenames

This is essential for the proxy to look up package hashes when APT requests a `.deb` file.

**APT Archives Import (v1.19+):**

debswarm can import existing packages from APT's local cache:
- Scans `/var/cache/apt/archives/` for `.deb` files on startup
- Skips the `partial/` subdirectory (incomplete downloads)
- Verifies each package's SHA256 hash against the index
- Only imports packages that can be verified (security)
- Copies verified packages to debswarm's cache
- Runs in background to avoid blocking daemon startup

This makes new debswarm users immediate contributors to the P2P network by sharing packages they already have.

**Notes:**
- APT lists watching requires the daemon to have read access to `/var/lib/apt/lists`
- Archives import requires read access to `/var/cache/apt/archives`
- When running as systemd service, these directories are typically accessible
- Unverified packages (not in index) are skipped for security
- Already-cached packages are skipped (idempotent)

---

## Complete Example Configuration

```toml
# /etc/debswarm/config.toml - Full configuration example

[network]
# P2P and proxy ports
listen_port = 4001
proxy_port = 9977

# Connection limits
max_connections = 100

# Bootstrap peers (libp2p public nodes)
bootstrap_peers = [
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
]

[cache]
# Cache location and limits
path = "/var/cache/debswarm"
max_size = "50GB"
min_free_space = "2GB"

[transfer]
# Bandwidth limits (0 = unlimited)
max_upload_rate = "10MB/s"
max_download_rate = "0"

# Per-peer rate limiting (prevents single peer monopolization)
per_peer_upload_rate = "auto"     # auto = global/expected_peers
per_peer_download_rate = "auto"
expected_peers = 10

# Adaptive rate limiting (adjusts rates based on peer performance)
# adaptive_rate_limiting = true   # enabled by default when per-peer is active
adaptive_min_rate = "100KB/s"
adaptive_max_boost = 1.5

# Concurrency limits
max_concurrent_uploads = 20
max_concurrent_peer_downloads = 10

# Automatic retry for failed downloads
retry_max_attempts = 3
retry_interval = "5m"
retry_max_age = "1h"

[dht]
# DHT timing
provider_ttl = "24h"
announce_interval = "12h"

[privacy]
# Discovery options
enable_mdns = true
announce_packages = true

# Private swarm (uncomment to enable)
# psk_path = "/etc/debswarm/swarm.key"
# peer_allowlist = []
# peer_blocklist = []

[metrics]
# Metrics server (localhost only by default)
port = 9978
bind = "127.0.0.1"

[logging]
# Log settings
level = "info"
file = ""

# Audit logging (v1.8+)
# [logging.audit]
# enabled = true
# path = "/var/log/debswarm/audit.json"
# max_size_mb = 100
# max_backups = 5

# Scheduled sync windows (v1.9+)
# [scheduler]
# enabled = true
# timezone = "America/New_York"
# outside_window_rate = "100KB/s"
# inside_window_rate = "unlimited"
# urgent_always_full_speed = true
#
# [[scheduler.windows]]
# days = ["weekday"]
# start_time = "22:00"
# end_time = "06:00"

# Fleet coordination (v1.9+)
# [fleet]
# enabled = true
# claim_timeout = "5s"
# max_wait_time = "5m"
# allow_concurrent = 1
```

---

## Command-Line Overrides

Some configuration options can be overridden via command-line flags:

| Flag | Config Equivalent | Description |
|------|-------------------|-------------|
| `--config, -c` | - | Config file path |
| `--proxy-port, -p` | `network.proxy_port` | HTTP proxy port |
| `--p2p-port` | `network.listen_port` | P2P listen port |
| `--metrics-port` | `metrics.port` | Metrics server port |
| `--metrics-bind` | `metrics.bind` | Metrics server bind address |
| `--max-upload-rate` | `transfer.max_upload_rate` | Maximum upload rate |
| `--max-download-rate` | `transfer.max_download_rate` | Maximum download rate |
| `--prefer-quic` | - | Prefer QUIC transport (default: true) |
| `--log-level, -l` | `logging.level` | Log verbosity level |
| `--log-file` | `logging.file` | Log file path |
| `--data-dir, -d` | - | Data directory for identity keys |

**Example:**
```bash
debswarm daemon --proxy-port 8080 --max-upload-rate 5MB/s --log-level debug
```

---

## Configuration Validation

debswarm validates configuration at startup and will fail fast with clear error messages if invalid settings are detected:

- Invalid port numbers (must be 1-65535)
- Invalid multiaddr format in bootstrap peers
- Invalid size/rate format strings
- Mutually exclusive options (e.g., both `psk` and `psk_path` set)
- Invalid log levels

**Example error:**
```
config validation failed with 2 errors:
  - network.bootstrap_peers[0]: invalid multiaddr "bad-address": ...
  - transfer.max_upload_rate: invalid rate "fast": ...
```

---

## SIGHUP Reload

The daemon supports reloading configuration on SIGHUP:

```bash
# Reload configuration
sudo systemctl reload debswarm
# or
kill -HUP $(pidof debswarm)
```

**Reloadable settings:**
- Rate limits (`max_upload_rate`, `max_download_rate`)
- Per-peer rate limits (`per_peer_upload_rate`, `per_peer_download_rate`, `expected_peers`)
- Adaptive settings (`adaptive_rate_limiting`, `adaptive_min_rate`, `adaptive_max_boost`)
- Database integrity check is performed on reload

**Settings requiring restart:**
- Ports (`listen_port`, `proxy_port`, `metrics.port`)
- Cache path
- Bootstrap peers
- PSK configuration
