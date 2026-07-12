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
	StaleTimeout    time.Duration // Reap a peer's in-flight entry after this long with no progress
}

// defaultStaleTimeout bounds how long a peer may be recorded as fetching a
// package without any progress update before the reaper drops the entry. A live
// fetcher broadcasts progress every RefreshInterval (1s by default), so a much
// larger window reliably distinguishes a dead/silent fetcher from a slow one.
const defaultStaleTimeout = 60 * time.Second

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		ClaimTimeout:    5 * time.Second,
		MaxWaitTime:     5 * time.Minute,
		AllowConcurrent: 1,
		RefreshInterval: time.Second,
		StaleTimeout:    defaultStaleTimeout,
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

// FleetSender extends MessageSender with broadcast capability.
// Protocol implements this interface.
type FleetSender interface {
	MessageSender
	BroadcastMessage(ctx context.Context, msg *Message) error
	// BroadcastMessageTo sends msg to the given peers and returns the IDs the
	// message was successfully written to.
	BroadcastMessageTo(ctx context.Context, peers []peer.AddrInfo, msg *Message) ([]peer.ID, error)
}

// pendingWant tracks an active WantPackage query awaiting responses from peers
type pendingWant struct {
	haveChan     chan peer.ID          // fed by handleHavePackage
	fetchingChan chan fetchingResponse // fed by handleFetching
	wantChan     chan fetchingResponse // fed by handleWantPackage (competing wanters, for election)
	dontHaveChan chan dontHaveResponse // fed by handleDontHave (peer NACKs)
	nonce        uint32                // our election nonce, echoed in DontHave replies to competitors
	done         chan struct{}         // closed when WantPackage returns
}

type fetchingResponse struct {
	peer  peer.ID
	nonce uint32
}

// dontHaveResponse is a peer's NACK to our WantPackage. alsoWanting is set when
// the peer is racing us for the same cold package, with its election nonce.
type dontHaveResponse struct {
	peer        peer.ID
	nonce       uint32
	alsoWanting bool
}

// Coordinator manages fleet coordination for download deduplication
type Coordinator struct {
	config *Config
	logger *zap.Logger
	peers  PeerProvider
	cache  CacheChecker
	sender FleetSender

	// In-flight downloads being coordinated
	inFlight map[string]*FetchState
	mu       sync.RWMutex

	// Pending want queries awaiting peer responses
	pendingWants   map[string]*pendingWant
	pendingWantsMu sync.Mutex

	// Message handlers
	msgChan chan PeerMessage

	// nonceFn generates election nonces. Overridable in tests for determinism;
	// defaults to generateNonce (crypto/rand).
	nonceFn func() uint32

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
	// Callers may build a Config literal without StaleTimeout (the daemon does);
	// a zero value would make IsStale always true and reap every entry, so clamp
	// it to a sane default here.
	if cfg.StaleTimeout <= 0 {
		cfg.StaleTimeout = defaultStaleTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &Coordinator{
		config:       cfg,
		logger:       logger,
		peers:        peers,
		cache:        cache,
		inFlight:     make(map[string]*FetchState),
		pendingWants: make(map[string]*pendingWant),
		msgChan:      make(chan PeerMessage, 100),
		ctx:          ctx,
		cancel:       cancel,
	}
	c.nonceFn = c.generateNonce

	// Start message handler
	c.wg.Add(1)
	go c.messageHandler()

	// Start the stale-entry reaper
	c.wg.Add(1)
	go c.reaper()

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
func (c *Coordinator) SetSender(sender FleetSender) {
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
	nonce := c.nonceFn()

	// Broadcast WantPackage to fleet peers
	fleetPeers := c.peers.GetMDNSPeers()
	if len(fleetPeers) == 0 {
		// No fleet peers, just fetch from WAN
		return &WantResult{Action: ActionFetchWAN}, nil
	}

	// Register pending want so response handlers can route messages to us
	pw := &pendingWant{
		haveChan:     make(chan peer.ID, len(fleetPeers)),
		fetchingChan: make(chan fetchingResponse, len(fleetPeers)),
		wantChan:     make(chan fetchingResponse, len(fleetPeers)),
		dontHaveChan: make(chan dontHaveResponse, len(fleetPeers)),
		nonce:        nonce,
		done:         make(chan struct{}),
	}
	c.pendingWantsMu.Lock()
	c.pendingWants[hash] = pw
	c.pendingWantsMu.Unlock()

	defer func() {
		close(pw.done)
		c.pendingWantsMu.Lock()
		delete(c.pendingWants, hash)
		c.pendingWantsMu.Unlock()
	}()

	// Broadcast WantPackage to all fleet peers, tracking which peers actually
	// received it so the wait below can end as soon as every reachable peer has
	// answered DontHave instead of always sitting out the full claim timeout.
	awaiting := make(map[peer.ID]struct{}, len(fleetPeers))
	if c.sender != nil {
		msg := &Message{
			Type:  MsgWantPackage,
			Hash:  hash,
			Size:  size,
			Nonce: nonce,
		}
		sentTo, err := c.sender.BroadcastMessageTo(ctx, fleetPeers, msg)
		if err != nil {
			c.logger.Debug("Failed to broadcast WantPackage to some peers",
				zap.Error(err),
				zap.String("hash", hash[:min(16, len(hash))]+"..."))
		}
		for _, id := range sentTo {
			awaiting[id] = struct{}{}
		}
	}
	if len(awaiting) == 0 {
		// No peer received our want, so no answer can ever arrive — equivalent
		// to having no fleet peers at all.
		return &WantResult{Action: ActionFetchWAN}, nil
	}

	// Track the lowest-nonce peer that is also racing us for this same cold
	// package (learned from their WantPackage broadcasts, which arrive quickly).
	// If by the claim timeout no peer already has it cached (HavePackage) and no
	// peer is already fetching it (Fetching), the lowest nonce in the fleet wins
	// the right to fetch from WAN and everyone else waits directly on that winner.
	// Waiting on the global-minimum winner (rather than an intermediate) avoids
	// chained waits when three or more nodes want the same package at once.
	var electedPeer peer.ID
	var electedNonce uint32
	var haveElected bool

	// decide resolves the claim window — either because the timer expired or
	// because every peer that received our want answered DontHave (nothing more
	// can arrive, so waiting longer is pure latency).
	decide := func() (*WantResult, error) {
		if haveElected {
			// We lost the election: a peer with a lower nonce is the fleet's
			// designated fetcher. Wait for that peer directly instead of
			// fetching from WAN ourselves, so only one node hits the mirror.
			// Its Fetching (announced when it starts) will not overwrite this
			// state, and its Fetched/FetchFailed will release our waiter; the
			// MaxWaitTime backstop covers a winner that never reports.
			waitChan := make(chan error, 1)
			c.mu.Lock()
			if state, ok := c.inFlight[hash]; ok {
				state.Waiters = append(state.Waiters, waitChan)
			} else {
				c.inFlight[hash] = &FetchState{
					Hash:       hash,
					Size:       size,
					Fetcher:    electedPeer,
					Nonce:      electedNonce,
					StartTime:  time.Now(),
					LastUpdate: time.Now(),
					Waiters:    []chan error{waitChan},
				}
			}
			c.mu.Unlock()

			return &WantResult{
				Action:   ActionWaitPeer,
				Provider: electedPeer,
				WaitChan: waitChan,
			}, nil
		}

		// No one claimed it and we hold the lowest nonce — we fetch from WAN
		c.mu.Lock()
		c.inFlight[hash] = &FetchState{
			Hash:       hash,
			Size:       size,
			Fetcher:    peer.ID(""), // self
			Nonce:      nonce,
			StartTime:  time.Now(),
			LastUpdate: time.Now(),
		}
		c.mu.Unlock()

		return &WantResult{Action: ActionFetchWAN}, nil
	}

	// Wait for responses with timeout
	timer := time.NewTimer(c.config.ClaimTimeout)
	defer timer.Stop()

	for {
		select {
		case provider := <-pw.haveChan:
			// Someone has it cached, fetch from them via LAN
			return &WantResult{
				Action:   ActionFetchLAN,
				Provider: provider,
			}, nil

		case competitor := <-pw.wantChan:
			// A peer is racing us for the same cold package. The lowest nonce
			// wins; remember the lowest competitor that beats ours. We only act
			// on this if we reach the timeout without a HavePackage or Fetching.
			if competitor.nonce < nonce && (!haveElected || competitor.nonce < electedNonce) {
				electedPeer = competitor.peer
				electedNonce = competitor.nonce
				haveElected = true
			}
			continue

		case fetching := <-pw.fetchingChan:
			if fetching.nonce < nonce {
				// They have a lower nonce — they win the election, wait for them
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
			// We have a lower nonce — we win, keep collecting (may still get HavePackage)
			continue

		case dh := <-pw.dontHaveChan:
			// A peer answered that it neither has the package nor is fetching
			// it. If it is also racing us for the same cold package, fold its
			// nonce into the election — its own WantPackage broadcast may have
			// been sent before we registered our pending want and been dropped,
			// so this reply is the reliable channel for that information.
			if dh.alsoWanting && dh.nonce < nonce && (!haveElected || dh.nonce < electedNonce) {
				electedPeer = dh.peer
				electedNonce = dh.nonce
				haveElected = true
			}
			delete(awaiting, dh.peer)
			if len(awaiting) > 0 {
				continue
			}
			// Every peer that received our want has now answered: decide
			// immediately instead of waiting out the rest of the claim timeout.
			return decide()

		case <-timer.C:
			// Some peers never answered (older version, or gone): the timer is
			// the backstop that keeps mixed fleets working.
			return decide()

		case <-ctx.Done():
			return &WantResult{Action: ActionFetchWAN}, ctx.Err()
		}
	}
}

// GetMaxWaitTime returns the configured maximum wait time for peer downloads
func (c *Coordinator) GetMaxWaitTime() time.Duration {
	return c.config.MaxWaitTime
}

// NotifyFetching announces that this node is fetching a package from WAN
func (c *Coordinator) NotifyFetching(hash string, size int64) {
	c.mu.Lock()
	nonce := c.nonceFn()
	if state, ok := c.inFlight[hash]; ok {
		// An entry already exists (e.g. WantPackage's timeout branch recorded us
		// as fetcher, and local callers may have queued as waiters since). Update
		// it in place so those waiters are not discarded — overwriting the whole
		// state here would strand them until the MaxWaitTime backstop fires.
		state.Size = size
		state.Fetcher = peer.ID("") // self
		state.Nonce = nonce
		state.StartTime = time.Now()
		state.LastUpdate = time.Now()
	} else {
		c.inFlight[hash] = &FetchState{
			Hash:       hash,
			Size:       size,
			Fetcher:    peer.ID(""), // self
			Nonce:      nonce,
			StartTime:  time.Now(),
			LastUpdate: time.Now(),
		}
	}
	c.mu.Unlock()

	c.logger.Debug("Notifying fleet of WAN fetch",
		zap.String("hash", hash[:min(16, len(hash))]+"..."),
		zap.Int64("size", size))

	if c.sender != nil {
		msg := &Message{
			Type:  MsgFetching,
			Hash:  hash,
			Size:  size,
			Nonce: nonce,
		}
		if err := c.sender.BroadcastMessage(c.ctx, msg); err != nil {
			c.logger.Debug("Failed to broadcast Fetching",
				zap.Error(err),
				zap.String("hash", hash[:min(16, len(hash))]+"..."))
		}
	}
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
	var size int64
	if state, ok := c.inFlight[hash]; ok {
		size = state.Size
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
	c.mu.Unlock()

	if c.sender != nil {
		msg := &Message{
			Type: MsgFetched,
			Hash: hash,
			Size: size,
		}
		if err := c.sender.BroadcastMessage(c.ctx, msg); err != nil {
			c.logger.Debug("Failed to broadcast Fetched",
				zap.Error(err),
				zap.String("hash", hash[:min(16, len(hash))]+"..."))
		}
	}
}

// NotifyFailed signals that a download failed
func (c *Coordinator) NotifyFailed(hash string, fetchErr error) {
	c.mu.Lock()
	if state, ok := c.inFlight[hash]; ok {
		// Notify all waiters of failure
		for _, ch := range state.Waiters {
			select {
			case ch <- fetchErr:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, hash)

		c.logger.Debug("Fleet download failed",
			zap.String("hash", hash[:min(16, len(hash))]+"..."),
			zap.Error(fetchErr))
	}
	c.mu.Unlock()

	if c.sender != nil {
		msg := &Message{
			Type: MsgFetchFailed,
			Hash: hash,
		}
		if err := c.sender.BroadcastMessage(c.ctx, msg); err != nil {
			c.logger.Debug("Failed to broadcast FetchFailed",
				zap.Error(err),
				zap.String("hash", hash[:min(16, len(hash))]+"..."))
		}
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

// reaper periodically drops in-flight entries for peers that were recorded as
// fetching a package but have gone silent (no progress within StaleTimeout) —
// e.g. the fetcher crashed, or it won the election but satisfied the request
// from its own cache/LAN without announcing completion. Without this, such an
// entry lingers: any local caller still waiting on it only recovers via its own
// MaxWaitTime, and a later WantPackage for the same hash would attach to the
// dead fetcher instead of fetching. Only peer-fetcher entries are reaped; our
// own downloads (Fetcher == "") are managed by the download lifecycle.
func (c *Coordinator) reaper() {
	defer c.wg.Done()

	interval := max(c.config.StaleTimeout/2, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.reapStale()
		}
	}
}

// reapStale drops stale peer-fetcher entries and releases their waiters so the
// waiters fall back to their own download instead of waiting the full wait cap.
func (c *Coordinator) reapStale() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for hash, state := range c.inFlight {
		// Never reap our own in-flight downloads (Fetcher == ""): their progress
		// may legitimately be quiet and they are cleaned up by Notify*.
		if state.Fetcher == "" || !state.IsStale(c.config.StaleTimeout) {
			continue
		}

		for _, ch := range state.Waiters {
			select {
			case ch <- ErrPeerFetchFailed:
			default:
			}
			close(ch)
		}
		delete(c.inFlight, hash)

		c.logger.Debug("Reaped stale fleet fetch state",
			zap.String("hash", hash[:min(16, len(hash))]+"..."),
			zap.String("fetcher", state.Fetcher.String()[:min(12, len(state.Fetcher.String()))]),
			zap.Int("waiters", len(state.Waiters)))
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
	case MsgDontHave:
		c.handleDontHave(pm.From, pm.Message)
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
		return
	}

	// We neither have it nor are fetching it. If we have our own in-flight
	// WantPackage election for this hash, this peer is racing us for the same
	// cold package: route their nonce into our election so the lowest nonce
	// wins and only one of us fetches from WAN (see WantPackage's wantChan case).
	c.pendingWantsMu.Lock()
	pw, ok := c.pendingWants[msg.Hash]
	c.pendingWantsMu.Unlock()
	var ourNonce uint32
	if ok {
		ourNonce = pw.nonce
		select {
		case pw.wantChan <- fetchingResponse{peer: from, nonce: msg.Nonce}:
		case <-pw.done:
		}
	}

	// Reply with an explicit NACK so the requester can resolve its claim window
	// as soon as everyone has answered, instead of always waiting out the full
	// claim timeout. Offset=1 flags that we are also racing for this package,
	// with our election nonce in Nonce (see handleDontHave on the other side).
	if c.sender != nil {
		resp := &Message{
			Type:  MsgDontHave,
			Hash:  msg.Hash,
			Nonce: ourNonce,
		}
		if ok {
			resp.Offset = 1
		}
		if err := c.sender.SendMessage(c.ctx, from, resp); err != nil {
			c.logger.Debug("Failed to send DontHave response",
				zap.Error(err),
				zap.String("peer", from.String()[:min(12, len(from.String()))]))
		}
	}
}

// handleDontHave routes a peer's NACK into the pending WantPackage query so it
// can stop waiting once every reached peer has answered.
func (c *Coordinator) handleDontHave(from peer.ID, msg Message) {
	c.pendingWantsMu.Lock()
	pw, ok := c.pendingWants[msg.Hash]
	c.pendingWantsMu.Unlock()
	if !ok {
		return
	}

	select {
	case pw.dontHaveChan <- dontHaveResponse{peer: from, nonce: msg.Nonce, alsoWanting: msg.Offset == 1}:
	case <-pw.done:
	}
}

func (c *Coordinator) handleHavePackage(from peer.ID, msg Message) {
	c.logger.Debug("Peer has package",
		zap.String("peer", from.String()[:min(12, len(from.String()))]),
		zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))

	// Route to pending WantPackage query if one exists
	c.pendingWantsMu.Lock()
	pw, ok := c.pendingWants[msg.Hash]
	c.pendingWantsMu.Unlock()

	if ok {
		select {
		case pw.haveChan <- from:
		case <-pw.done:
		}
	}
}

func (c *Coordinator) handleFetching(from peer.ID, msg Message) {
	c.mu.Lock()
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
	c.mu.Unlock()

	c.logger.Debug("Peer is fetching package",
		zap.String("peer", from.String()[:min(12, len(from.String()))]),
		zap.String("hash", msg.Hash[:min(16, len(msg.Hash))]+"..."))

	// Route to pending WantPackage query if one exists
	c.pendingWantsMu.Lock()
	pw, ok := c.pendingWants[msg.Hash]
	c.pendingWantsMu.Unlock()

	if ok {
		select {
		case pw.fetchingChan <- fetchingResponse{peer: from, nonce: msg.Nonce}:
		case <-pw.done:
		}
	}
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
