// Package audit provides structured audit logging for security-sensitive operations
package audit

import (
	"time"
)

// EventType represents the type of audit event
type EventType string

const (
	// EventDownloadComplete is logged when a package download succeeds
	EventDownloadComplete EventType = "download_complete"
	// EventDownloadFailed is logged when a package download fails
	EventDownloadFailed EventType = "download_failed"
	// EventUploadComplete is logged when a package upload to a peer succeeds
	EventUploadComplete EventType = "upload_complete"
	// EventVerificationFailed is logged when hash verification fails
	EventVerificationFailed EventType = "verification_failed"
	// EventCacheHit is logged when a package is served from cache
	EventCacheHit EventType = "cache_hit"
	// EventPeerBlacklisted is logged when a peer is blacklisted
	EventPeerBlacklisted EventType = "peer_blacklisted"
	// EventMultiSourceVerified is logged when a package is verified by multiple providers
	EventMultiSourceVerified EventType = "multi_source_verified"
	// EventMultiSourceUnverified is logged when no other providers found for a package
	EventMultiSourceUnverified EventType = "multi_source_unverified"
	// EventConnectTunnelStart is logged when an HTTPS CONNECT tunnel is established
	EventConnectTunnelStart EventType = "connect_tunnel_start"
	// EventConnectTunnelEnd is logged when an HTTPS CONNECT tunnel closes
	EventConnectTunnelEnd EventType = "connect_tunnel_end"
	// EventConnectTunnelBlocked is logged when a CONNECT request is blocked
	EventConnectTunnelBlocked EventType = "connect_tunnel_blocked"
)

// Event represents a single audit log entry
type Event struct {
	// Timestamp when the event occurred (RFC3339 format in JSON)
	Timestamp time.Time `json:"timestamp"`

	// EventType identifies what happened
	EventType EventType `json:"event_type"`

	// RequestID is the correlation ID for end-to-end request tracing
	RequestID string `json:"request_id,omitempty"`

	// PackageHash is the SHA256 hash of the package (truncated in logs)
	PackageHash string `json:"package_hash,omitempty"`

	// PackageName is the filename of the package
	PackageName string `json:"package_name,omitempty"`

	// PackageSize is the size in bytes
	PackageSize int64 `json:"package_size,omitempty"`

	// Source indicates where the package came from: "peer", "mirror", "cache", "mixed"
	Source string `json:"source,omitempty"`

	// PeerID is the libp2p peer ID (for uploads or peer-specific events)
	PeerID string `json:"peer_id,omitempty"`

	// DurationMs is the operation duration in milliseconds
	DurationMs int64 `json:"duration_ms,omitempty"`

	// BytesP2P is bytes downloaded from P2P peers
	BytesP2P int64 `json:"bytes_p2p,omitempty"`

	// BytesMirror is bytes downloaded from mirrors
	BytesMirror int64 `json:"bytes_mirror,omitempty"`

	// ChunksTotal is the total number of chunks (for chunked downloads)
	ChunksTotal int `json:"chunks_total,omitempty"`

	// ChunksP2P is chunks downloaded from P2P
	ChunksP2P int `json:"chunks_p2p,omitempty"`

	// Error contains error details for failed events
	Error string `json:"error,omitempty"`

	// Reason provides additional context (e.g., blacklist reason)
	Reason string `json:"reason,omitempty"`

	// ProviderCount is the number of other providers found (for multi-source verification)
	ProviderCount int `json:"provider_count,omitempty"`

	// CONNECT tunnel fields
	// TargetHost is the hostname for CONNECT tunnel requests
	TargetHost string `json:"target_host,omitempty"`
	// TargetPort is the port for CONNECT tunnel requests
	TargetPort string `json:"target_port,omitempty"`
	// TunnelBytes is total bytes transferred through the tunnel
	TunnelBytes int64 `json:"tunnel_bytes,omitempty"`
}

// NewDownloadCompleteEvent creates an event for successful downloads
func NewDownloadCompleteEvent(hash, name string, size int64, source string, durationMs int64, bytesP2P, bytesMirror int64) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventDownloadComplete,
		PackageHash: truncateHash(hash),
		PackageName: name,
		PackageSize: size,
		Source:      source,
		DurationMs:  durationMs,
		BytesP2P:    bytesP2P,
		BytesMirror: bytesMirror,
	}
}

// NewDownloadFailedEvent creates an event for failed downloads
func NewDownloadFailedEvent(hash, name string, err string) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventDownloadFailed,
		PackageHash: truncateHash(hash),
		PackageName: name,
		Error:       err,
	}
}

// NewUploadCompleteEvent creates an event for successful uploads
func NewUploadCompleteEvent(hash string, size int64, peerID string, durationMs int64) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventUploadComplete,
		PackageHash: truncateHash(hash),
		PackageSize: size,
		PeerID:      truncatePeerID(peerID),
		DurationMs:  durationMs,
	}
}

// NewVerificationFailedEvent creates an event for hash verification failures
func NewVerificationFailedEvent(hash, name string, peerID string) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventVerificationFailed,
		PackageHash: truncateHash(hash),
		PackageName: name,
		PeerID:      truncatePeerID(peerID),
		Error:       "hash mismatch",
	}
}

// NewCacheHitEvent creates an event for cache hits
func NewCacheHitEvent(hash, name string, size int64) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventCacheHit,
		PackageHash: truncateHash(hash),
		PackageName: name,
		PackageSize: size,
		Source:      "cache",
	}
}

// NewPeerBlacklistedEvent creates an event for peer blacklisting
func NewPeerBlacklistedEvent(peerID, reason string) Event {
	return Event{
		Timestamp: time.Now(),
		EventType: EventPeerBlacklisted,
		PeerID:    truncatePeerID(peerID),
		Reason:    reason,
	}
}

// truncateHash returns first 16 chars of hash for readability
func truncateHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// truncatePeerID returns first 16 chars of peer ID for readability
func truncatePeerID(peerID string) string {
	if len(peerID) > 16 {
		return peerID[:16]
	}
	return peerID
}

// WithRequestID returns a copy of the event with the request ID set.
// This allows chaining with event constructors for request tracing.
func (e Event) WithRequestID(id string) Event {
	e.RequestID = id
	return e
}

// NewConnectTunnelStartEvent creates an event for CONNECT tunnel establishment
func NewConnectTunnelStartEvent(host, port string) Event {
	return Event{
		Timestamp:  time.Now(),
		EventType:  EventConnectTunnelStart,
		TargetHost: host,
		TargetPort: port,
	}
}

// NewConnectTunnelEndEvent creates an event for CONNECT tunnel completion
func NewConnectTunnelEndEvent(host, port string, tunnelBytes, durationMs int64) Event {
	return Event{
		Timestamp:   time.Now(),
		EventType:   EventConnectTunnelEnd,
		TargetHost:  host,
		TargetPort:  port,
		TunnelBytes: tunnelBytes,
		DurationMs:  durationMs,
	}
}

// NewConnectTunnelBlockedEvent creates an event for blocked CONNECT requests
func NewConnectTunnelBlockedEvent(host, port, reason string) Event {
	return Event{
		Timestamp:  time.Now(),
		EventType:  EventConnectTunnelBlocked,
		TargetHost: host,
		TargetPort: port,
		Reason:     reason,
	}
}
