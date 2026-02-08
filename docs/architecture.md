# Architecture

## Overview

debswarm is a peer-to-peer package distribution system that operates as an HTTP proxy between APT and package mirrors. It intercepts APT requests, attempts to fulfill them from the P2P network, and falls back to mirrors when necessary.

```
┌────────────────────────────────────────────────────────────────┐
│                         Local Machine                          │
│                                                                │
│  ┌─────────┐     ┌──────────────────────────────────────────┐  │
│  │   APT   │────▶│              debswarm                    │  │
│  │         │     │  ┌────────┐  ┌────────┐  ┌────────────┐  │  │
│  └─────────┘     │  │ Proxy  │──│ Cache  │──│ Downloader │  │  │
│                  │  └────────┘  └────────┘  └────────────┘  │  │
│                  │       │           │            │         │  │
│                  │       │      ┌────┴────┐       │         │  │
│                  │       │      │ SQLite  │       │         │  │
│                  │       │      └─────────┘       │         │  │
│                  │       │                        │         │  │
│                  │  ┌────┴────────────────────────┴────┐    │  │
│                  │  │           P2P Node               │    │  │
│                  │  │  ┌─────────┐  ┌──────────────┐   │    │  │
│                  │  │  │ libp2p  │  │ Kademlia DHT │   │    │  │
│                  │  │  └─────────┘  └──────────────┘   │    │  │
│                  │  │  ┌──────────────────────────┐    │    │  │
│                  │  │  │   Fleet Coordinator      │    │    │  │
│                  │  │  └──────────────────────────┘    │    │  │
│                  │  └──────────────────────────────────┘    │  │
│                  └──────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────┘
         │                         │                    │
         │                         │                    │
         ▼                         ▼                    ▼
    ┌─────────┐              ┌──────────┐         ┌──────────┐
    │ Mirror  │              │  Peers   │         │   DHT    │
    │ (HTTP)  │              │  (P2P)   │         │ Network  │
    └─────────┘              └──────────┘         └──────────┘
```

## Components

### Proxy Server (`internal/proxy/`)

The HTTP proxy intercepts all APT HTTP requests:

- **Request Classification**: Determines if request is for a package (.deb), index (Packages), or release file
- **Download Strategy**: Coordinates between cache, P2P, and mirror sources
- **Response Handling**: Streams content back to APT with appropriate headers

```go
type Server struct {
    cache      *cache.Cache
    p2pNode    *p2p.Node
    downloader *downloader.Downloader
    fetcher    *mirror.Fetcher
    metrics    *metrics.Metrics
}
```

### P2P Node (`internal/p2p/`)

Built on libp2p, handles all peer-to-peer networking:

- **Host**: libp2p host with QUIC and TCP transports
- **DHT**: Kademlia DHT for peer and content discovery
- **Protocols**: Custom transfer protocols for package sharing
- **Discovery**: mDNS for local network, DHT for global

```go
type Node struct {
    host    host.Host
    dht     *dht.IpfsDHT
    scorer  *peers.Scorer
    timeouts *timeouts.Manager
}
```

### Cache (`internal/cache/`)

Content-addressed storage with SQLite metadata:

- **Storage**: Files stored by SHA256 hash
- **Metadata**: SQLite database tracks filenames, sizes, access patterns
- **Eviction**: LRU with popularity boost
- **Announcements**: Tracks which packages are announced to DHT

```
~/.cache/debswarm/
├── packages/
│   └── sha256/
│       ├── ab/
│       │   └── abcdef1234...  (package file)
│       └── cd/
│           └── cdef5678...    (package file)
└── debswarm.db               (SQLite metadata)
```

### Downloader (`internal/downloader/`)

Parallel chunked download engine with resume support and streaming assembly:

- **Chunk Management**: Splits large files into 4MB chunks
- **Source Tracking**: Monitors performance of each source
- **Racing**: Small files (<10MB) race P2P vs mirror in memory
- **Streaming Assembly**: Large files assembled directly to disk, not memory
- **Memory Efficient**: Chunked downloads use ~32MB regardless of file size
- **Buffer Pooling**: Reuses 4MB buffers via sync.Pool for zero-allocation chunk I/O
- **Resume Support**: Persists chunks to disk, tracks state in SQLite
- **State Manager**: Tracks download progress for crash recovery

```go
type Downloader struct {
    scorer       *peers.Scorer
    metrics      *metrics.Metrics
    chunkSize    int64
    maxConc      int
    stateManager *StateManager
    cache        PartialCache
    bufferPool   *sync.Pool  // Reusable buffers for chunk I/O
}

// DownloadResult returns either Data (small files) or FilePath (large files)
type DownloadResult struct {
    Data     []byte  // For racing (small files)
    FilePath string  // For chunked (large files) - path to verified temp file
    // ...
}
```

### Peer Scorer (`internal/peers/`)

Tracks and scores peer performance:

- **Metrics**: Latency, throughput, success rate, proximity (v1.8+)
- **Scoring**: Weighted combination of metrics (latency 25%, throughput 25%, reliability 20%, freshness 15%, proximity 15%)
- **LAN Priority**: mDNS-discovered peers get higher proximity scores (v1.8+)
- **Selection**: Returns best peers with some diversity
- **Blacklisting**: Temporary ban for misbehaving peers

### Timeout Manager (`internal/timeouts/`)

Adaptive timeout system:

- **Per-Operation**: Different base timeouts for different operations
- **Adaptation**: Adjusts based on observed performance
- **Size-Based**: Calculates timeouts based on file size
- **Decay**: Gradually returns to base values

### Security Validator (`internal/security/`)

URL validation and SSRF protection:

- **SSRF Blocking**: Prevents requests to localhost, cloud metadata (169.254.x.x), private networks
- **URL Allowlisting**: Validates URLs match Debian/Ubuntu repository patterns
- **Pattern Matching**: Requires `/dists/`, `/pool/`, `/debian/`, or `/ubuntu/` in URLs

```go
func IsAllowedMirrorURL(rawURL string) bool
```

### Fleet Coordinator (`internal/fleet/`)

LAN fleet coordination for download deduplication (v1.25+):

- **Want/Have Protocol**: Peers broadcast `WantPackage` messages; peers with the package reply `HavePackage`
- **Nonce-Based Election**: When no peer has the package, the peer with the lowest random nonce fetches from WAN
- **Wait Notification**: Other peers wait for the elected fetcher and then download via P2P
- **Progress Broadcasting**: Periodic progress updates during active WAN downloads
- **Lifecycle Tracking**: `NotifyFetching`, `NotifyComplete`, `NotifyFailed` keep all peers informed

```go
type Coordinator struct {
    config       FleetConfig
    sender       FleetSender       // Protocol for broadcast/send
    mDNSPeers    func() []peer.ID  // LAN peer discovery
    inFlight     map[string]*inFlightDownload
    pendingWants map[string]*pendingWant
}
```

**Fleet Actions** returned by `WantPackage()`:
- `ActionFetchLAN` — A peer already has the package cached; download from them
- `ActionWaitPeer` — A peer is fetching from WAN; wait for completion then download via P2P
- `ActionFetchWAN` — This node is the designated WAN fetcher

### Benchmark (`internal/benchmark/`)

Performance testing with simulated peers:

- **Simulated Peers**: Configurable latency and throughput for testing
- **Scenarios**: Small files, large files, varying peer counts
- **Metrics**: Throughput (MB/s), duration, chunk distribution
- **Reproducible**: Deterministic testing without real network

### Connectivity Monitor (`internal/connectivity/`)

Network connectivity monitoring for offline-first mode (v1.8+):

- **Mode Detection**: Online, LAN-only, or Offline modes
- **Auto Detection**: Periodic connectivity checks to configured URL
- **Graceful Fallback**: Falls back to mDNS-only peers when internet unavailable
- **Mode Callbacks**: Notifies components when connectivity changes

```go
type Monitor struct {
    GetMode() Mode                    // Current mode: ModeOnline, ModeLANOnly, ModeOffline
    Start(ctx context.Context)        // Start background monitoring
}
```

### Audit Logger (`internal/audit/`)

Structured event logging for compliance and monitoring (v1.8+):

- **Event Types**: Download complete/failed, upload complete, cache hits, verification failures
- **JSON Lines Format**: Compatible with ELK, Splunk, jq
- **File Rotation**: Automatic rotation with configurable size and backup count
- **Logger Interface**: Supports NoopLogger when disabled

```go
type Logger interface {
    Log(event Event)
    Close() error
}
```

## Data Flow

### Seeding

```
debswarm seed import *.deb
  └─▶ For each .deb file:
      ├─▶ Calculate SHA256 hash
      ├─▶ Store in cache (content-addressed)
      ├─▶ Update SQLite metadata
      └─▶ Announce to DHT (if enabled)
```

### Package Download

```
1. APT Request
   └─▶ Proxy receives GET /debian/pool/main/v/vim/vim_9.0.deb

2. Index Lookup
   └─▶ Find expected SHA256 from cached Packages index

3. Cache Check
   └─▶ Check if hash exists in local cache
       └─▶ HIT: Serve from cache, done
       └─▶ MISS: Continue

3b. Fleet Coordination (if enabled)
    └─▶ Broadcast WantPackage to LAN peers
        └─▶ Peer has it: ActionFetchLAN (download from peer)
        └─▶ Peer is fetching: ActionWaitPeer (wait, then download)
        └─▶ No response: ActionFetchWAN (this node fetches)

4. P2P Discovery
   └─▶ Query DHT for providers of this hash
   └─▶ Score and rank found peers

5. Download Strategy
   └─▶ Small file (<10MB): Race P2P vs mirror
   └─▶ Large file (≥10MB): Parallel chunk download

6. Verification
   └─▶ Compute SHA256 of downloaded content
   └─▶ Compare with expected hash
       └─▶ MATCH: Cache and serve
       └─▶ MISMATCH: Blacklist peer, retry

7. Announcement
   └─▶ Announce to DHT that we now have this package
```

### Chunk Download (Large Files)

```
File: 80MB package
Chunk size: 4MB
Chunks: 20

┌──────────────────────────────────────────────────────┐
│                    Work Queue                        │
│  [C0] [C1] [C2] [C3] [C4] ... [C19]                  │
└──────────────────────────────────────────────────────┘
         │     │     │     │
         ▼     ▼     ▼     ▼
    ┌────────────────────────────┐
    │       Worker Pool          │
    │  W1   W2   W3   W4   ...   │
    └────────────────────────────┘
         │     │     │       │
         ▼     ▼     ▼       ▼
    ┌─────┐ ┌─────┐ ┌──────┐ ┌─────┐
    │Peer │ │Peer │ │Mirror│ │Peer │
    │  A  │ │  B  │ │      │ │  C  │
    └─────┘ └─────┘ └──────┘ └─────┘
```

### Download Resume (v0.6.0+)

Chunked downloads can survive interruptions:

```
During Download:
┌─────────────────────────────────────────────────┐
│ SQLite: downloads table                         │
│   id: "abc123...", status: "in_progress"        │
│   chunks: 20, completed: 12                     │
└─────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────┐
│ Disk: ~/.cache/debswarm/packages/partial/abc123 │
│   chunk_0, chunk_1, ... chunk_11 (completed)    │
└─────────────────────────────────────────────────┘

On Resume:
1. Check SQLite for pending downloads
2. Read completed chunks from disk
3. Download only missing chunks (12-19)
4. Assemble and verify hash
5. Clean up partial directory
```

## Network Protocols

### Transfer Protocol

```
Protocol ID: /debswarm/transfer/1.0.0

Request:
  [64 bytes: SHA256 hash as hex string]
  [1 byte: newline]

Response:
  [8 bytes: content length as big-endian uint64]
  [N bytes: content]
```

### Range Transfer Protocol

```
Protocol ID: /debswarm/transfer-range/1.0.0

Request:
  [64 bytes: SHA256 hash as hex string]
  [8 bytes: start offset as big-endian uint64]
  [8 bytes: end offset as big-endian uint64]
  [1 byte: newline]

Response:
  [8 bytes: content length as big-endian uint64]
  [N bytes: content]
```

### Fleet Protocol

```
Protocol ID: /debswarm/fleet/1.0.0

Messages (JSON-encoded):
  MsgWantPackage  - "I need this package (hash, size, nonce)"
  MsgHavePackage  - "I have this package cached"
  MsgFetching     - "I'm downloading this from WAN (nonce for election)"
  MsgFetched      - "WAN download complete, package available"
  MsgFetchFailed  - "WAN download failed"
  MsgProgress     - "Download progress update"
```

### DHT Namespace

```
Provider Key: /debswarm/pkg/{sha256_hash}
```

## Security

### Trust Boundaries

```
┌─────────────────────────────────────────────────────┐
│                 TRUSTED                             │
│  ┌─────────────────────────────────────────────┐    │
│  │  APT + GPG Verification                     │    │
│  │  - Release files (signed by Debian/Ubuntu)  │    │
│  │  - Packages index (signed transitively)     │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
                        │
                        │ SHA256 hashes
                        ▼
┌─────────────────────────────────────────────────────┐
│                 VERIFIED                            │
│  ┌─────────────────────────────────────────────┐    │
│  │  debswarm verification                      │    │
│  │  - All downloads verified against SHA256    │    │
│  │  - Hash from trusted Packages index         │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
                        │
                        │ Download content
                        ▼
┌─────────────────────────────────────────────────────┐
│                 UNTRUSTED                           │
│  ┌─────────────────────────────────────────────┐    │
│  │  P2P Network                                │    │
│  │  - Peers can send anything                  │    │
│  │  - Content must pass verification           │    │
│  │  - Bad actors get blacklisted               │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
```

### Security Hardening (v0.6.x)

Additional security measures beyond content verification:

- **SSRF Protection**: Block requests to localhost, cloud metadata, private networks
- **Response Limits**: Mirror responses capped at 500MB to prevent memory exhaustion
- **HTTP Headers**: Dashboard uses nonce-based CSP (`script-src 'nonce-...'`), API endpoints use `script-src 'none'`, plus X-Frame-Options, X-Content-Type-Options
- **Error Disclosure**: Dashboard hides internal error details from users
- **Identity Protection**: Ed25519 keys stored with 0600 permissions
- **PSK Security**: Only fingerprints logged, never full keys
- **File Permissions**: Directories use 0750, files use 0600 for sensitive data
- **Error Handling**: All Close/Remove operations properly handle errors

## Reusable Internal Libraries

The codebase includes several internal libraries that reduce code duplication and provide consistent patterns across components.

### Retry Library (`internal/retry/`)

Generic retry logic with configurable backoff strategies:

```go
type Config struct {
    MaxAttempts int                           // Required: max attempts (not retries)
    Backoff     func(attempt int) time.Duration // Optional: backoff strategy
}

// Backoff strategies
func Exponential(base time.Duration) func(int) time.Duration  // attempt² * base
func Linear(base time.Duration) func(int) time.Duration       // attempt * base
func Constant(d time.Duration) func(int) time.Duration        // always d

// Generic retry function
func Do[T any](ctx context.Context, cfg Config, fn func() (T, error)) (T, error)
```

Used by: `mirror/fetcher.go`, `downloader/downloader.go`

### Lifecycle Manager (`internal/lifecycle/`)

Manages goroutine lifecycles with context cancellation and wait group tracking:

```go
type Manager struct {
    // Internal: ctx, cancel, wg
}

func New(parent context.Context) *Manager
func (m *Manager) Context() context.Context
func (m *Manager) Go(fn func(ctx context.Context))           // Start tracked goroutine
func (m *Manager) GoTicker(interval time.Duration, fn func()) // Periodic task
func (m *Manager) Stop()                                      // Cancel + wait all
func (m *Manager) StopWithTimeout(d time.Duration) error      // Cancel + wait with deadline
```

Used by: `ratelimit/peer_limiter.go`, `proxy/server.go`

### Hash Utilities (`internal/hashutil/`)

Streaming hash computation during I/O operations:

```go
// Hash while writing
type HashingWriter struct { /* wraps io.Writer */ }
func NewHashingWriter(w io.Writer) *HashingWriter
func (hw *HashingWriter) Write(p []byte) (int, error)
func (hw *HashingWriter) Sum() string  // Hex-encoded SHA256

// Hash while reading
type HashingReader struct { /* wraps io.Reader */ }
func NewHashingReader(r io.Reader) *HashingReader
func (hr *HashingReader) Read(p []byte) (int, error)
func (hr *HashingReader) Sum() string
```

Used by: `cache/cache.go`, `downloader/downloader.go`

### HTTP Client Factory (`internal/httpclient/`)

Centralized HTTP client creation with sensible defaults:

```go
type Config struct {
    Timeout             time.Duration  // Default: 60s
    MaxIdleConnsPerHost int            // Default: 10
    IdleConnTimeout     time.Duration  // Default: 90s
}

func New(cfg *Config) *http.Client     // Full-featured client
func Default() *http.Client            // Pre-configured defaults
func WithTimeout(timeout time.Duration) *http.Client  // Simple timeout-only
```

Used by: `mirror/fetcher.go`, `index/index.go`, `connectivity/monitor.go`
