# Architecture

## Overview

debswarm is a peer-to-peer package distribution system that operates as an HTTP proxy between APT and package mirrors. It intercepts APT requests, attempts to fulfill them from the P2P network, and falls back to mirrors when necessary.

```
┌─────────────────────────────────────────────────────────────────┐
│                         Local Machine                           │
│                                                                 │
│  ┌─────────┐     ┌──────────────────────────────────────────┐  │
│  │   APT   │────▶│              debswarm                     │  │
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
│                  │  └──────────────────────────────────┘    │  │
│                  └──────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
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

Parallel chunked download engine:

- **Chunk Management**: Splits large files into 4MB chunks
- **Source Tracking**: Monitors performance of each source
- **Racing**: Small files race P2P vs mirror
- **Assembly**: Reassembles chunks and verifies hash

```go
type Downloader struct {
    scorer    *peers.Scorer
    metrics   *metrics.Metrics
    chunkSize int64
    maxConc   int
}
```

### Peer Scorer (`internal/peers/`)

Tracks and scores peer performance:

- **Metrics**: Latency, throughput, success rate
- **Scoring**: Weighted combination of metrics
- **Selection**: Returns best peers with some diversity
- **Blacklisting**: Temporary ban for misbehaving peers

### Timeout Manager (`internal/timeouts/`)

Adaptive timeout system:

- **Per-Operation**: Different base timeouts for different operations
- **Adaptation**: Adjusts based on observed performance
- **Size-Based**: Calculates timeouts based on file size
- **Decay**: Gradually returns to base values

## Data Flow

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
│                    Work Queue                         │
│  [C0] [C1] [C2] [C3] [C4] ... [C19]                  │
└──────────────────────────────────────────────────────┘
         │     │     │     │
         ▼     ▼     ▼     ▼
    ┌────────────────────────────┐
    │       Worker Pool          │
    │  W1   W2   W3   W4   ...   │
    └────────────────────────────┘
         │     │     │     │
         ▼     ▼     ▼     ▼
    ┌─────┐ ┌─────┐ ┌─────┐ ┌─────┐
    │Peer │ │Peer │ │Mirror│ │Peer │
    │  A  │ │  B  │ │      │ │  C  │
    └─────┘ └─────┘ └─────┘ └─────┘
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

### DHT Namespace

```
Provider Key: /debswarm/pkg/{sha256_hash}
```

## Security

### Trust Boundaries

```
┌─────────────────────────────────────────────────────┐
│                 TRUSTED                              │
│  ┌─────────────────────────────────────────────┐    │
│  │  APT + GPG Verification                      │    │
│  │  - Release files (signed by Debian/Ubuntu)   │    │
│  │  - Packages index (signed transitively)      │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
                        │
                        │ SHA256 hashes
                        ▼
┌─────────────────────────────────────────────────────┐
│                 VERIFIED                             │
│  ┌─────────────────────────────────────────────┐    │
│  │  debswarm verification                       │    │
│  │  - All downloads verified against SHA256     │    │
│  │  - Hash from trusted Packages index          │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
                        │
                        │ Download content
                        ▼
┌─────────────────────────────────────────────────────┐
│                 UNTRUSTED                            │
│  ┌─────────────────────────────────────────────┐    │
│  │  P2P Network                                 │    │
│  │  - Peers can send anything                   │    │
│  │  - Content must pass verification            │    │
│  │  - Bad actors get blacklisted                │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
```
