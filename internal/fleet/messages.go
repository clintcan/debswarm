// Package fleet provides LAN fleet coordination for download deduplication.
package fleet

import (
	"encoding/binary"
	"errors"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ProtocolID is the libp2p protocol identifier for fleet coordination
const ProtocolID = "/debswarm/fleet/1.0.0"

// Message types for fleet coordination protocol
const (
	MsgWantPackage   uint8 = 1 // "I need package X"
	MsgHavePackage   uint8 = 2 // "I have package X in cache"
	MsgFetching      uint8 = 3 // "I'm downloading X from WAN"
	MsgFetchProgress uint8 = 4 // "Download progress update"
	MsgFetched       uint8 = 5 // "I finished downloading X"
	MsgFetchFailed   uint8 = 6 // "I failed to download X"
)

// Message represents a fleet coordination message
type Message struct {
	Type   uint8
	Nonce  uint32 // Random nonce for election tiebreaker
	Hash   string // Package SHA256 hash
	Size   int64  // Package size in bytes
	Offset int64  // Current download offset (for progress)
}

// WantPackageMsg represents a request for a package
type WantPackageMsg struct {
	Hash  string // Package SHA256 hash
	Size  int64  // Expected size (0 if unknown)
	Nonce uint32 // Random nonce for election
}

// HavePackageMsg indicates a peer has a package cached
type HavePackageMsg struct {
	Hash string // Package SHA256 hash
	Size int64  // Actual size
}

// FetchingMsg indicates a peer is downloading from WAN
type FetchingMsg struct {
	Hash  string // Package being downloaded
	Size  int64  // Expected size
	Nonce uint32 // The winning nonce
}

// FetchProgressMsg provides download progress updates
type FetchProgressMsg struct {
	Hash   string // Package being downloaded
	Offset int64  // Current offset (bytes downloaded)
	Size   int64  // Total size
}

// FetchedMsg indicates a download completed successfully
type FetchedMsg struct {
	Hash string // Package hash
	Size int64  // Final size
}

// FetchFailedMsg indicates a download failed
type FetchFailedMsg struct {
	Hash   string // Package hash
	Reason string // Failure reason
}

// PeerMessage wraps a message with its source peer
type PeerMessage struct {
	From    peer.ID
	Message Message
}

// Encode writes a message to a writer in binary format
func (m *Message) Encode(w io.Writer) error {
	// Write message type (1 byte)
	if err := binary.Write(w, binary.BigEndian, m.Type); err != nil {
		return err
	}

	// Write nonce (4 bytes)
	if err := binary.Write(w, binary.BigEndian, m.Nonce); err != nil {
		return err
	}

	// Write hash length and hash
	hashBytes := []byte(m.Hash)
	if err := binary.Write(w, binary.BigEndian, uint16(len(hashBytes))); err != nil {
		return err
	}
	if _, err := w.Write(hashBytes); err != nil {
		return err
	}

	// Write size (8 bytes)
	if err := binary.Write(w, binary.BigEndian, m.Size); err != nil {
		return err
	}

	// Write offset (8 bytes)
	if err := binary.Write(w, binary.BigEndian, m.Offset); err != nil {
		return err
	}

	return nil
}

// Decode reads a message from a reader
func (m *Message) Decode(r io.Reader) error {
	// Read message type
	if err := binary.Read(r, binary.BigEndian, &m.Type); err != nil {
		return err
	}

	// Read nonce
	if err := binary.Read(r, binary.BigEndian, &m.Nonce); err != nil {
		return err
	}

	// Read hash length and hash
	var hashLen uint16
	if err := binary.Read(r, binary.BigEndian, &hashLen); err != nil {
		return err
	}
	if hashLen > 1024 {
		return errors.New("hash too long")
	}
	hashBytes := make([]byte, hashLen)
	if _, err := io.ReadFull(r, hashBytes); err != nil {
		return err
	}
	m.Hash = string(hashBytes)

	// Read size
	if err := binary.Read(r, binary.BigEndian, &m.Size); err != nil {
		return err
	}

	// Read offset
	if err := binary.Read(r, binary.BigEndian, &m.Offset); err != nil {
		return err
	}

	return nil
}

// FetchState tracks the state of an in-flight download
type FetchState struct {
	Hash       string       // Package hash
	Size       int64        // Expected size
	Offset     int64        // Current download progress
	Fetcher    peer.ID      // Peer doing the download
	Nonce      uint32       // Winning nonce
	StartTime  time.Time    // When download started
	LastUpdate time.Time    // Last progress update
	Waiters    []chan error // Channels waiting for completion
}

// IsStale returns true if the fetch state is stale (no updates for too long)
func (s *FetchState) IsStale(timeout time.Duration) bool {
	return time.Since(s.LastUpdate) > timeout
}

// Progress returns the download progress as a fraction (0.0 - 1.0)
func (s *FetchState) Progress() float64 {
	if s.Size <= 0 {
		return 0
	}
	return float64(s.Offset) / float64(s.Size)
}
