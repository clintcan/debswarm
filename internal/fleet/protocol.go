package fleet

import (
	"bufio"
	"context"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Protocol handles the fleet coordination protocol over libp2p streams
type Protocol struct {
	host        host.Host
	coordinator *Coordinator
	logger      *zap.Logger

	// Stream management
	streams   map[peer.ID]network.Stream
	streamsMu sync.RWMutex
}

// NewProtocol creates a new fleet protocol handler
func NewProtocol(h host.Host, coord *Coordinator, logger *zap.Logger) *Protocol {
	p := &Protocol{
		host:        h,
		coordinator: coord,
		logger:      logger,
		streams:     make(map[peer.ID]network.Stream),
	}

	// Register stream handler
	h.SetStreamHandler(ProtocolID, p.handleStream)

	// Register as message sender for coordinator responses
	coord.SetSender(p)

	return p
}

// Close shuts down the protocol handler
func (p *Protocol) Close() error {
	p.host.RemoveStreamHandler(ProtocolID)

	p.streamsMu.Lock()
	defer p.streamsMu.Unlock()

	for _, s := range p.streams {
		_ = s.Close()
	}
	p.streams = make(map[peer.ID]network.Stream)

	return nil
}

// handleStream handles incoming fleet protocol streams
func (p *Protocol) handleStream(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	p.logger.Debug("Fleet stream opened",
		zap.String("peer", peerID.String()[:min(12, len(peerID.String()))]))

	defer func() { _ = s.Close() }()

	reader := bufio.NewReader(s)

	for {
		var msg Message
		if err := msg.Decode(reader); err != nil {
			if err != io.EOF {
				p.logger.Debug("Failed to decode fleet message",
					zap.Error(err),
					zap.String("peer", peerID.String()[:min(12, len(peerID.String()))]))
			}
			return
		}

		// Pass to coordinator
		p.coordinator.HandleMessage(peerID, msg)
	}
}

// getOrCreateStream gets an existing stream or creates a new one
func (p *Protocol) getOrCreateStream(ctx context.Context, peerID peer.ID) (network.Stream, error) {
	p.streamsMu.RLock()
	s, ok := p.streams[peerID]
	p.streamsMu.RUnlock()

	if ok {
		return s, nil
	}

	// Create new stream
	s, err := p.host.NewStream(ctx, peerID, ProtocolID)
	if err != nil {
		return nil, err
	}

	p.streamsMu.Lock()
	p.streams[peerID] = s
	p.streamsMu.Unlock()

	return s, nil
}

// SendMessage sends a message to a specific peer
func (p *Protocol) SendMessage(ctx context.Context, peerID peer.ID, msg *Message) error {
	s, err := p.getOrCreateStream(ctx, peerID)
	if err != nil {
		return err
	}

	return msg.Encode(s)
}

// BroadcastMessage sends a message to all mDNS peers
func (p *Protocol) BroadcastMessage(ctx context.Context, msg *Message) error {
	peers := p.coordinator.peers.GetMDNSPeers()

	var lastErr error
	for _, peerInfo := range peers {
		if err := p.SendMessage(ctx, peerInfo.ID, msg); err != nil {
			p.logger.Debug("Failed to send fleet message",
				zap.Error(err),
				zap.String("peer", peerInfo.ID.String()[:min(12, len(peerInfo.ID.String()))]))
			lastErr = err
		}
	}

	return lastErr
}

// BroadcastWant broadcasts a WantPackage message to all fleet peers
func (p *Protocol) BroadcastWant(ctx context.Context, hash string, size int64, nonce uint32) error {
	msg := &Message{
		Type:  MsgWantPackage,
		Hash:  hash,
		Size:  size,
		Nonce: nonce,
	}
	return p.BroadcastMessage(ctx, msg)
}

// BroadcastHave broadcasts that we have a package
func (p *Protocol) BroadcastHave(ctx context.Context, hash string, size int64) error {
	msg := &Message{
		Type: MsgHavePackage,
		Hash: hash,
		Size: size,
	}
	return p.BroadcastMessage(ctx, msg)
}

// BroadcastFetching broadcasts that we're fetching a package
func (p *Protocol) BroadcastFetching(ctx context.Context, hash string, size int64, nonce uint32) error {
	msg := &Message{
		Type:  MsgFetching,
		Hash:  hash,
		Size:  size,
		Nonce: nonce,
	}
	return p.BroadcastMessage(ctx, msg)
}

// BroadcastProgress broadcasts download progress
func (p *Protocol) BroadcastProgress(ctx context.Context, hash string, offset, size int64) error {
	msg := &Message{
		Type:   MsgFetchProgress,
		Hash:   hash,
		Size:   size,
		Offset: offset,
	}
	return p.BroadcastMessage(ctx, msg)
}

// BroadcastComplete broadcasts that a download completed
func (p *Protocol) BroadcastComplete(ctx context.Context, hash string, size int64) error {
	msg := &Message{
		Type: MsgFetched,
		Hash: hash,
		Size: size,
	}
	return p.BroadcastMessage(ctx, msg)
}

// BroadcastFailed broadcasts that a download failed
func (p *Protocol) BroadcastFailed(ctx context.Context, hash string) error {
	msg := &Message{
		Type: MsgFetchFailed,
		Hash: hash,
	}
	return p.BroadcastMessage(ctx, msg)
}

// StartProgressBroadcaster starts a background goroutine that periodically
// broadcasts progress for in-flight downloads
func (p *Protocol) StartProgressBroadcaster(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Collect progress data under the lock
			var messages []Message
			p.coordinator.mu.RLock()
			for hash, state := range p.coordinator.inFlight {
				if state.Fetcher == "" { // We're the fetcher
					messages = append(messages, Message{
						Type:   MsgFetchProgress,
						Hash:   hash,
						Size:   state.Size,
						Offset: state.Offset,
					})
				}
			}
			p.coordinator.mu.RUnlock()

			// Broadcast outside the lock to avoid blocking on network I/O
			for i := range messages {
				if err := p.BroadcastMessage(ctx, &messages[i]); err != nil {
					p.logger.Debug("Failed to broadcast progress",
						zap.Error(err),
						zap.String("hash", messages[i].Hash[:min(16, len(messages[i].Hash))]+"..."))
				}
			}
		}
	}
}
