package fleet

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// mockPeerProvider implements PeerProvider for testing
type mockPeerProvider struct {
	peers []peer.AddrInfo
}

func (m *mockPeerProvider) GetMDNSPeers() []peer.AddrInfo {
	return m.peers
}

// mockCacheChecker implements CacheChecker for testing
type mockCacheChecker struct {
	hashes map[string]bool
}

func (m *mockCacheChecker) Has(hash string) bool {
	return m.hashes[hash]
}

func TestNewCoordinator(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	if c == nil {
		t.Fatal("expected non-nil coordinator")
	}

	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestCoordinatorNoFleetPeers(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{} // No peers
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	// With no fleet peers, should get ActionFetchWAN
	result, err := c.WantPackage(context.Background(), "abc123", 1000)
	if err != nil {
		t.Errorf("WantPackage() error = %v", err)
	}
	if result.Action != ActionFetchWAN {
		t.Errorf("expected ActionFetchWAN, got %v", result.Action)
	}
}

func TestCoordinatorLocalCache(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: map[string]bool{"abc123": true}}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	// If we have it cached, should return ActionFetchWAN (no coordination needed)
	result, err := c.WantPackage(context.Background(), "abc123", 1000)
	if err != nil {
		t.Errorf("WantPackage() error = %v", err)
	}
	if result.Action != ActionFetchWAN {
		t.Errorf("expected ActionFetchWAN for cached package, got %v", result.Action)
	}
}

func TestNotifyFetchingComplete(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	hash := "abc123def456"

	// Start fetching
	c.NotifyFetching(hash, 1000)

	if c.GetInFlightCount() != 1 {
		t.Errorf("expected 1 in-flight, got %d", c.GetInFlightCount())
	}

	// Update progress
	c.NotifyProgress(hash, 500)

	// Complete
	c.NotifyComplete(hash)

	if c.GetInFlightCount() != 0 {
		t.Errorf("expected 0 in-flight after complete, got %d", c.GetInFlightCount())
	}
}

func TestNotifyFetchingFailed(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	hash := "abc123def456"

	// Start fetching
	c.NotifyFetching(hash, 1000)

	// Fail
	c.NotifyFailed(hash, ErrTimeout)

	if c.GetInFlightCount() != 0 {
		t.Errorf("expected 0 in-flight after failure, got %d", c.GetInFlightCount())
	}
}

func TestFetchStateIsStale(t *testing.T) {
	state := &FetchState{
		LastUpdate: time.Now().Add(-10 * time.Minute),
	}

	if !state.IsStale(5 * time.Minute) {
		t.Error("expected stale with 5m timeout")
	}

	if state.IsStale(15 * time.Minute) {
		t.Error("expected not stale with 15m timeout")
	}
}

func TestFetchStateProgress(t *testing.T) {
	tests := []struct {
		offset int64
		size   int64
		want   float64
	}{
		{0, 100, 0},
		{50, 100, 0.5},
		{100, 100, 1.0},
		{0, 0, 0}, // Zero size
		{50, 0, 0},
	}

	for _, tt := range tests {
		state := &FetchState{Offset: tt.offset, Size: tt.size}
		got := state.Progress()
		if got != tt.want {
			t.Errorf("Progress(%d/%d) = %v, want %v", tt.offset, tt.size, got, tt.want)
		}
	}
}

func TestCoordinatorStatus(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{
		peers: []peer.AddrInfo{{}, {}}, // 2 peers
	}
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	status := c.Status()
	if status.InFlightCount != 0 {
		t.Errorf("expected 0 in-flight, got %d", status.InFlightCount)
	}
	if status.PeerCount != 2 {
		t.Errorf("expected 2 peers, got %d", status.PeerCount)
	}
}

// mockFleetSender implements FleetSender for testing
type mockFleetSender struct {
	mu       sync.Mutex
	messages []struct {
		peerID peer.ID
		msg    *Message
	}
	broadcasts []*Message
}

func (m *mockFleetSender) SendMessage(ctx context.Context, peerID peer.ID, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, struct {
		peerID peer.ID
		msg    *Message
	}{peerID, msg})
	return nil
}

func (m *mockFleetSender) BroadcastMessage(ctx context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcasts = append(m.broadcasts, msg)
	return nil
}

func (m *mockFleetSender) getMessages() []struct {
	peerID peer.ID
	msg    *Message
} {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]struct {
		peerID peer.ID
		msg    *Message
	}, len(m.messages))
	copy(result, m.messages)
	return result
}

func (m *mockFleetSender) getBroadcasts() []*Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*Message, len(m.broadcasts))
	copy(result, m.broadcasts)
	return result
}

func TestHandleWantPackageWithCache(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: map[string]bool{"cached123": true}}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	fromPeer := peer.ID("peer123")
	msg := Message{
		Type: MsgWantPackage,
		Hash: "cached123",
		Size: 1000,
	}

	c.HandleMessage(fromPeer, msg)

	// Wait for message handler to process
	var messages []struct {
		peerID peer.ID
		msg    *Message
	}
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		messages = sender.getMessages()
		if len(messages) > 0 {
			break
		}
	}

	if len(messages) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(messages))
	}

	sent := messages[0]
	if sent.peerID != fromPeer {
		t.Errorf("expected message to %v, got %v", fromPeer, sent.peerID)
	}
	if sent.msg.Type != MsgHavePackage {
		t.Errorf("expected MsgHavePackage, got %d", sent.msg.Type)
	}
	if sent.msg.Hash != "cached123" {
		t.Errorf("expected hash 'cached123', got %s", sent.msg.Hash)
	}
}

func TestHandleWantPackageWhileFetching(t *testing.T) {
	logger := zap.NewNop()
	peers := &mockPeerProvider{}
	cache := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peers, cache, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	// Start fetching a package
	c.NotifyFetching("fetching456", 2000)

	fromPeer := peer.ID("peer456")
	msg := Message{
		Type: MsgWantPackage,
		Hash: "fetching456",
		Size: 2000,
	}

	c.HandleMessage(fromPeer, msg)

	// Wait for message handler to process
	var messages []struct {
		peerID peer.ID
		msg    *Message
	}
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		messages = sender.getMessages()
		if len(messages) > 0 {
			break
		}
	}

	if len(messages) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(messages))
	}

	sent := messages[0]
	if sent.peerID != fromPeer {
		t.Errorf("expected message to %v, got %v", fromPeer, sent.peerID)
	}
	if sent.msg.Type != MsgFetching {
		t.Errorf("expected MsgFetching, got %d", sent.msg.Type)
	}
	if sent.msg.Hash != "fetching456" {
		t.Errorf("expected hash 'fetching456', got %s", sent.msg.Hash)
	}
}

func TestMessageEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "WantPackage",
			msg: Message{
				Type:  MsgWantPackage,
				Nonce: 12345,
				Hash:  "abc123def456",
				Size:  1000,
			},
		},
		{
			name: "HavePackage",
			msg: Message{
				Type: MsgHavePackage,
				Hash: "xyz789",
				Size: 5000,
			},
		},
		{
			name: "FetchProgress",
			msg: Message{
				Type:   MsgFetchProgress,
				Hash:   "progress123",
				Size:   10000,
				Offset: 5000,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tt.msg.Encode(&buf); err != nil {
				t.Fatalf("Encode() error = %v", err)
			}

			var decoded Message
			if err := decoded.Decode(&buf); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}

			if decoded.Type != tt.msg.Type {
				t.Errorf("Type = %v, want %v", decoded.Type, tt.msg.Type)
			}
			if decoded.Nonce != tt.msg.Nonce {
				t.Errorf("Nonce = %v, want %v", decoded.Nonce, tt.msg.Nonce)
			}
			if decoded.Hash != tt.msg.Hash {
				t.Errorf("Hash = %v, want %v", decoded.Hash, tt.msg.Hash)
			}
			if decoded.Size != tt.msg.Size {
				t.Errorf("Size = %v, want %v", decoded.Size, tt.msg.Size)
			}
			if decoded.Offset != tt.msg.Offset {
				t.Errorf("Offset = %v, want %v", decoded.Offset, tt.msg.Offset)
			}
		})
	}
}

func TestWantPackageBroadcasts(t *testing.T) {
	logger := zap.NewNop()
	peerList := &mockPeerProvider{
		peers: []peer.AddrInfo{{ID: peer.ID("peer1")}, {ID: peer.ID("peer2")}},
	}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	cfg := DefaultConfig()
	cfg.ClaimTimeout = 100 * time.Millisecond // Short timeout for test

	c := New(cfg, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	// WantPackage should broadcast MsgWantPackage and timeout to ActionFetchWAN
	result, err := c.WantPackage(context.Background(), "hash123456789012", 1000)
	if err != nil {
		t.Fatalf("WantPackage() error = %v", err)
	}
	if result.Action != ActionFetchWAN {
		t.Errorf("expected ActionFetchWAN, got %v", result.Action)
	}

	broadcasts := sender.getBroadcasts()
	if len(broadcasts) == 0 {
		t.Fatal("expected at least one broadcast")
	}
	if broadcasts[0].Type != MsgWantPackage {
		t.Errorf("expected MsgWantPackage broadcast, got %d", broadcasts[0].Type)
	}
	if broadcasts[0].Hash != "hash123456789012" {
		t.Errorf("expected hash 'hash123456789012', got %s", broadcasts[0].Hash)
	}
}

func TestWantPackageHaveResponse(t *testing.T) {
	logger := zap.NewNop()
	provider := peer.ID("provider-peer")
	peerList := &mockPeerProvider{
		peers: []peer.AddrInfo{{ID: provider}},
	}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	cfg := DefaultConfig()
	cfg.ClaimTimeout = 5 * time.Second // Long enough to receive response

	c := New(cfg, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	hash := "hash_have_response"

	// Start WantPackage in goroutine
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	// Give WantPackage time to register pending want and broadcast
	time.Sleep(50 * time.Millisecond)

	// Inject HavePackage response via HandleMessage
	c.HandleMessage(provider, Message{
		Type: MsgHavePackage,
		Hash: hash,
	})

	select {
	case result := <-resultChan:
		if result.Action != ActionFetchLAN {
			t.Errorf("expected ActionFetchLAN, got %v", result.Action)
		}
		if result.Provider != provider {
			t.Errorf("expected provider %v, got %v", provider, result.Provider)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

func TestWantPackageFetchingLowerNonce(t *testing.T) {
	logger := zap.NewNop()
	fetcherPeer := peer.ID("fetcher-peer")
	peerList := &mockPeerProvider{
		peers: []peer.AddrInfo{{ID: fetcherPeer}},
	}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	cfg := DefaultConfig()
	cfg.ClaimTimeout = 5 * time.Second

	c := New(cfg, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	hash := "hash_fetching_nonce"

	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	time.Sleep(50 * time.Millisecond)

	// Inject Fetching response with nonce=0 (guaranteed lower than any random nonce)
	c.HandleMessage(fetcherPeer, Message{
		Type:  MsgFetching,
		Hash:  hash,
		Nonce: 0,
		Size:  1000,
	})

	select {
	case result := <-resultChan:
		if result.Action != ActionWaitPeer {
			t.Errorf("expected ActionWaitPeer, got %v", result.Action)
		}
		if result.Provider != fetcherPeer {
			t.Errorf("expected provider %v, got %v", fetcherPeer, result.Provider)
		}
		if result.WaitChan == nil {
			t.Error("expected non-nil WaitChan")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

func TestWantPackageTimeout(t *testing.T) {
	logger := zap.NewNop()
	peerList := &mockPeerProvider{
		peers: []peer.AddrInfo{{ID: peer.ID("some-peer")}},
	}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	cfg := DefaultConfig()
	cfg.ClaimTimeout = 50 * time.Millisecond

	c := New(cfg, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	// No responses injected — should timeout to ActionFetchWAN
	result, err := c.WantPackage(context.Background(), "hash_timeout_test", 1000)
	if err != nil {
		t.Fatalf("WantPackage() error = %v", err)
	}
	if result.Action != ActionFetchWAN {
		t.Errorf("expected ActionFetchWAN on timeout, got %v", result.Action)
	}
}

func TestNotifyFetchingBroadcasts(t *testing.T) {
	logger := zap.NewNop()
	peerList := &mockPeerProvider{}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	c.NotifyFetching("hash_notify_fetch", 5000)

	broadcasts := sender.getBroadcasts()
	if len(broadcasts) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(broadcasts))
	}
	if broadcasts[0].Type != MsgFetching {
		t.Errorf("expected MsgFetching broadcast, got %d", broadcasts[0].Type)
	}
	if broadcasts[0].Hash != "hash_notify_fetch" {
		t.Errorf("expected hash 'hash_notify_fetch', got %s", broadcasts[0].Hash)
	}
	if broadcasts[0].Size != 5000 {
		t.Errorf("expected size 5000, got %d", broadcasts[0].Size)
	}
}

func TestNotifyCompleteBroadcasts(t *testing.T) {
	logger := zap.NewNop()
	peerList := &mockPeerProvider{}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	c := New(nil, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	sender := &mockFleetSender{}
	c.SetSender(sender)

	// Must register as in-flight first
	c.NotifyFetching("hash_complete_bc", 3000)
	c.NotifyComplete("hash_complete_bc")

	broadcasts := sender.getBroadcasts()
	// Should have MsgFetching + MsgFetched
	var foundFetched bool
	for _, b := range broadcasts {
		if b.Type == MsgFetched && b.Hash == "hash_complete_bc" {
			foundFetched = true
		}
	}
	if !foundFetched {
		t.Errorf("expected MsgFetched broadcast, got broadcasts: %v", broadcasts)
	}
}

func TestGetMaxWaitTime(t *testing.T) {
	logger := zap.NewNop()
	peerList := &mockPeerProvider{}
	ch := &mockCacheChecker{hashes: make(map[string]bool)}

	cfg := &Config{
		ClaimTimeout:    5 * time.Second,
		MaxWaitTime:     3 * time.Minute,
		AllowConcurrent: 1,
		RefreshInterval: time.Second,
	}

	c := New(cfg, peerList, ch, logger)
	defer func() { _ = c.Close() }()

	if c.GetMaxWaitTime() != 3*time.Minute {
		t.Errorf("expected 3m, got %v", c.GetMaxWaitTime())
	}
}
