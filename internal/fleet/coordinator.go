package fleet

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Config holds fleet coordinator configuration
type Config struct {
	ClaimTimeout    time.Duration // How long to wait for a peer to claim
	MaxWaitTime     time.Duration // Max time to wait for peer download
	AllowConcurrent int           // Max concurrent WAN fetchers
	RefreshInterval time.Duration // Progress broadcast interval
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		ClaimTimeout:    5 * time.Second,
		MaxWaitTime:     5 * time.Minute,
		AllowConcurrent: 1,
		RefreshInterval: time.Second,
	}
}

// WantAction indicates what action to take for a wanted package
type WantAction int

const (
	ActionFetchWAN WantAction = iota // This node should fetch from WAN
	ActionWaitPeer                   // Wait for peer to finish
	ActionFetchLAN                   // Fetch from peer that has it
)

// WantResult contains the result of a want query
type WantResult struct {
	Action   WantAction
	Provider peer.ID    // Peer that has it (for ActionFetchLAN) or is fetching (for ActionWaitPeer)
	WaitChan chan error // Channel to wait on for ActionWaitPeer
}

// PeerProvider provides access to mDNS/LAN peers
type PeerProvider interface {
	GetMDNSPeers() []peer.AddrInfo
}

// CacheChecker checks if a package is in the local cache
type CacheChecker interface {
	Has(hash string) bool
}

// MessageSender sends fleet messages to peers
type MessageSender interface {
	SendMessage(ctx context.Context, peerID peer.ID, msg *Message) error
}

// Coordinator manages fleet coordination for download deduplication
type Coordinator struct {
	config *Config
	logger *zap.Logger
	peers  PeerProvider
	cache  CacheChecker
	sender MessageSender

	// In-flight downloads being coordinated
	inFlight map[string]*FetchState
	mu       sync.RWMutex

	// Message handlers
	msgChan chan PeerMessage

	// Shutdown
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new fleet coordinator
func New(cfg *Config, peers PeerProvider, cache CacheChecker, logger *zap.Logger) *Coordinator {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &Coordinator{
		config:   cfg,
		logger:   logger,
		peers:    peers,
		cache:    cache,
		inFlight: make(map[string]*FetchState),
		msgChan:  make(chan PeerMessage, 100),
		ctx:      ctx,
		cancel:   cancel,
	}

	// Start message handler
	c.wg.Add(1)
	go c.messageHandler()

	return c
}

// Close shuts down the coordinator
func (c *Coordinator) Close() error {
	c.cancel()
	c.wg.Wait()
	return nil
}

// SetSender sets the message sender for responding to peers.
// This is called after Protocol is created to avoid circular dependency.
func (c *Coordinator) SetSender(sender MessageSender) {
	c.sender = sender
}

// WantPackage initiates a package request to the fleet.
// Returns what action this node should take.
func (c *Coordinator) WantPackage(ctx context.Context, hash string, size int64) (*WantResult, error) {
	// Check if we have it locally
	if c.cache.Has(hash) {
		return &WantResult{Action: ActionFetchWAN}, nil // We have it, no coordination needed
	}

	// Check if already being fetched by someone
	c.mu.RLock()
	state, exists := c.inFlight[hash]
	c.mu.RUnlock()

	if exists {
		// Someone is already fetching, wait for them
		waitChan := make(chan error, 1)
		c.mu.Lock()
		if state, ok := c.inFlight[hash]; ok {
			state.Waiters = append(state.Waiters, waitChan)
		} else {
			c.mu.Unlock()
			close(waitChan)
			return &WantResult{Action: ActionFetchWAN}, nil
		}
		c.mu.Unlock()

		return &WantResult{
			Action:   ActionWaitPeer,
			Provider: state.Fetcher,
			WaitChan: waitChan,
		}, nil
	}

	// Generate nonce for election
	nonce := c.generateNonce()

	// Broadcast WantPackage to fleet peers
	peers := c.peers.GetMDNSPeers()
	if len(peers) == 0 {
		// No fleet peers, just fetch from WAN
		return &WantResult{Action: ActionFetchWAN}, nil
	}

	// Create channels for responses
	haveChan := make(chan peer.ID, len(peers))
	fetchingChan := make(chan struct {
		peer  peer.ID
		nonce uint32
	}, len(peers))

	// Wait for responses with timeout
	timer := time.NewTimer(c.config.ClaimTimeout)
	defer timer.Stop()

	// In a real implementation, we would:
	// 1. Send MsgWantPackage to all peers
	// 2. Collect MsgHavePackage and MsgFetching responses
	// 3. If someone has it, return ActionFetchLAN
	// 4. If someone else is fetching (lower nonce), return ActionWaitPeer
	// 5. If we have lowest nonce, return ActionFetchWAN

	// For now, simplified implementation:
	// Just register our intent to fetch and proceed
	select {
	case <-timer.C:
		// Timeout, no one else claimed it - we fetch
		c.mu.Lock()
		c.inFlight[hash] = &FetchState{
			Hash:       hash,
			Size:       size,
			Fetcher:    peer.ID(""), // self
			Nonce:      nonce,
			StartTime:  time.Now(),
			LastUpdate: time.Now(),
			Waiters:    nil,
		}
		c.mu.Unlock()

		return &WantResult{Action: ActionFetchWAN}, nil

	case provider := <-haveChan:
		// Someone has it, fetch from them
		return &WantResult{
			Action:   ActionFetchLAN,
			Provider: provider,
		}, nil

	case fetching := <-fetchingChan:
		// Someone else is fetching
		if fetching.nonce < nonce {
			// They win, wait for them
			waitChan := make(chan error, 1)
			c.mu.Lock()
			if state, ok := c.inFlight[hash]; ok {
				state.Waiters = append(state.Waiters, waitChan)
			} else {
				c.inFlight[hash] = &FetchState{
					Hash:       hash,
					Size:       size,
					Fetcher:    fetching.peer,
					Nonce:      fetching.nonce,
					StartTime:  time.Now(),
					LastUpdate: time.Now(),
					Waiters:    []chan error{waitChan},
				}
			}
			c.mu.Unlock()

			return &WantResult{
				Action:   ActionWaitPeer,
				Provider: fetching.peer,
				WaitChan: waitChan,
			}, nil
		}
		// We win, fetch from WAN
		return &WantResult{Action: ActionFetchWAN}, nil
	}
}

// NotifyFetching announces that this node is fetching a package from WAN
func (c *Coordinator) NotifyFetching(hash string, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	nonce := c.generateNonce()
	c.inFlight[hash] = &FetchState{
		Hash:       hash,
		Size:       size,
		Fetcher:    peer.ID(""), // self
		Nonce:      nonce,
		StartTime:  time.Now(),
		LastUpdate: time.Now(),
	}

	c.logger.Debug("Notifying fleet of WAN fetch",
		zap.String("hash", hash[:min(16, len(hash))]+"..."),
		zap.Int64("size", size))
}

// NotifyProgress reports download progress to the fleet
func (c *Coordinator) NotifyProgress(hash string, offset int64) {
	c.mu.Lock()
	if state, ok := c.inFlight[hash]; ok {
		state.Offset = offset
		state.LastUpdate = time.Now()
	}
	c.mu.Unlock()
}

// NotifyComplete signals that a download completed successfully
func (c *Coordinator) NotifyComplete(hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.inFlight[hash]; ok {
		// Notify all waiters of success
		for _, ch := range state.Waiters {
			select {
			case ch <- nil:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, hash)

		c.logger.Debug("Fleet download complete",
			zap.String("hash", hash[:min(16, len(hash))]+"..."),
			zap.Int("waiters", len(state.Waiters)))
	}
}

// NotifyFailed signals that a download failed
func (c *Coordinator) NotifyFailed(hash string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.inFlight[hash]; ok {
		// Notify all waiters of failure
		for _, ch := range state.Waiters {
			select {
			case ch <- err:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, hash)

		c.logger.Debug("Fleet download failed",
			zap.String("hash", hash[:min(16, len(hash))]+"..."),
			zap.Error(err))
	}
}

// GetInFlightCount returns the number of in-flight downloads
func (c *Coordinator) GetInFlightCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.inFlight)
}

// HandleMessage processes an incoming fleet message
func (c *Coordinator) HandleMessage(from peer.ID, msg Message) {
	select {
	case c.msgChan <- PeerMessage{From: from, Message: msg}:
	default:
		c.logger.Warn("Fleet message queue full, dropping message",
			zap.Uint8("type", msg.Type))
	}
}

// messageHandler processes incoming fleet messages
func (c *Coordinator) messageHandler() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		case pm := <-c.msgChan:
			c.handleMessage(pm)
		}
	}
}

func (c *Coordinator) handleMessage(pm PeerMessage) {
	switch pm.Message.Type {
	case MsgWantPackage:
		c.handleWantPackage(pm.From, pm.Message)
	case MsgHavePackage:
		c.handleHavePackage(pm.From, pm.Message)
	case MsgFetching:
		c.handleFetching(pm.From, pm.Message)
	case MsgFetchProgress:
		c.handleFetchProgress(pm.From, pm.Message)
	case MsgFetched:
		c.handleFetched(pm.From, pm.Message)
	case MsgFetchFailed:
		c.handleFetchFailed(pm.From, pm.Message)
	}
}

func (c *Coordinator) handleWantPackage(from peer.ID, msg Message) {
	// If we have the package, respond with HavePackage
	if c.cache.Has(msg.Hash) {
		c.logger.Debug("Responding to WantPackage with HavePackage",
			zap.String("peer", from.String()[:min(12, len(from.String()))]),
			zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))

		if c.sender != nil {
			resp := &Message{
				Type: MsgHavePackage,
				Hash: msg.Hash,
			}
			if err := c.sender.SendMessage(c.ctx, from, resp); err != nil {
				c.logger.Debug("Failed to send HavePackage response",
					zap.Error(err),
					zap.String("peer", from.String()[:min(12, len(from.String()))]))
			}
		}
		return // We have it, no need to check if we're fetching
	}

	// If we're fetching, respond with Fetching
	var isSelfFetching bool
	var nonce uint32
	var size int64

	c.mu.RLock()
	if state, ok := c.inFlight[msg.Hash]; ok && state.Fetcher == "" {
		isSelfFetching = true
		nonce = state.Nonce
		size = state.Size
	}
	c.mu.RUnlock()

	if isSelfFetching {
		c.logger.Debug("Responding to WantPackage with Fetching",
			zap.String("peer", from.String()[:min(12, len(from.String()))]),
			zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))

		if c.sender != nil {
			resp := &Message{
				Type:  MsgFetching,
				Hash:  msg.Hash,
				Nonce: nonce,
				Size:  size,
			}
			if err := c.sender.SendMessage(c.ctx, from, resp); err != nil {
				c.logger.Debug("Failed to send Fetching response",
					zap.Error(err),
					zap.String("peer", from.String()[:min(12, len(from.String()))]))
			}
		}
	}
}

func (c *Coordinator) handleHavePackage(from peer.ID, msg Message) {
	c.logger.Debug("Peer has package",
		zap.String("peer", from.String()[:min(12, len(from.String()))]),
		zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))
}

func (c *Coordinator) handleFetching(from peer.ID, msg Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Record that this peer is fetching
	if _, exists := c.inFlight[msg.Hash]; !exists {
		c.inFlight[msg.Hash] = &FetchState{
			Hash:       msg.Hash,
			Size:       msg.Size,
			Fetcher:    from,
			Nonce:      msg.Nonce,
			StartTime:  time.Now(),
			LastUpdate: time.Now(),
		}
	}

	c.logger.Debug("Peer is fetching package",
		zap.String("peer", from.String()[:min(12, len(from.String()))]),
		zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))
}

func (c *Coordinator) handleFetchProgress(from peer.ID, msg Message) {
	c.mu.Lock()
	if state, ok := c.inFlight[msg.Hash]; ok && state.Fetcher == from {
		state.Offset = msg.Offset
		state.LastUpdate = time.Now()
	}
	c.mu.Unlock()
}

func (c *Coordinator) handleFetched(from peer.ID, msg Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.inFlight[msg.Hash]; ok && state.Fetcher == from {
		// Notify waiters that the package is now available from this peer
		for _, ch := range state.Waiters {
			select {
			case ch <- nil:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, msg.Hash)

		c.logger.Debug("Peer finished fetching package",
			zap.String("peer", from.String()[:min(12, len(from.String()))]),
			zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))
	}
}

func (c *Coordinator) handleFetchFailed(from peer.ID, msg Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.inFlight[msg.Hash]; ok && state.Fetcher == from {
		// Notify waiters that the fetch failed - they should try themselves
		err := ErrPeerFetchFailed
		for _, ch := range state.Waiters {
			select {
			case ch <- err:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, msg.Hash)

		c.logger.Debug("Peer failed to fetch package",
			zap.String("peer", from.String()[:min(12, len(from.String()))]),
			zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))
	}
}

// generateNonce generates a random 32-bit nonce for election
func (c *Coordinator) generateNonce() uint32 {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fallback to time-based if crypto/rand fails (extremely rare)
		// Intentional truncation to 32 bits - only need randomness, not full precision
		return uint32(time.Now().UnixNano() & 0xFFFFFFFF) // #nosec G115 -- intentional truncation for nonce
	}
	return binary.BigEndian.Uint32(buf[:])
}

// Status returns the current coordinator status
type Status struct {
	InFlightCount int
	PeerCount     int
}

// Status returns the current status of the coordinator
func (c *Coordinator) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peerCount := 0
	if c.peers != nil {
		peerCount = len(c.peers.GetMDNSPeers())
	}

	return Status{
		InFlightCount: len(c.inFlight),
		PeerCount:     peerCount,
	}
}
