package fleet

import (
	"bytes"
	"context"
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
