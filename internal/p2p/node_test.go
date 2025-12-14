package p2p

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
	"go.uber.org/zap"
)

func newTestLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		ListenPort:     0, // Random port
		BootstrapPeers: nil,
		EnableMDNS:     false,
		DataDir:        t.TempDir(),
		PreferQUIC:     false, // TCP only for faster tests
		Scorer:         peers.NewScorer(),
		Timeouts:       timeouts.NewManager(nil),
		Metrics:        metrics.New(),
	}
}

func TestNew_BasicCreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	// Verify node has a valid peer ID
	peerID := node.PeerID()
	if peerID == "" {
		t.Error("Node should have a peer ID")
	}

	// Peer ID should start with "12D3Koo" for Ed25519 keys
	if !strings.HasPrefix(peerID.String(), "12D3Koo") {
		t.Errorf("Unexpected peer ID format: %s", peerID)
	}
}

func TestNew_WithQUIC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	cfg.PreferQUIC = true
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New with QUIC failed: %v", err)
	}
	defer node.Close()

	// Verify node created successfully
	if node.PeerID() == "" {
		t.Error("Node should have a peer ID")
	}

	// Should have QUIC addresses
	addrs := node.Addrs()
	hasQUIC := false
	for _, addr := range addrs {
		if strings.Contains(addr.String(), "quic") {
			hasQUIC = true
			break
		}
	}
	if !hasQUIC {
		t.Error("Node with PreferQUIC should have QUIC addresses")
	}
}

func TestNew_EphemeralIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	cfg.DataDir = "" // No data dir = ephemeral identity
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New with ephemeral identity failed: %v", err)
	}
	defer node.Close()

	if node.PeerID() == "" {
		t.Error("Ephemeral node should have a peer ID")
	}
}

func TestNew_PersistentIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	cfg := newTestConfig(t)
	cfg.DataDir = dataDir
	logger := newTestLogger()

	// Create first node
	node1, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	id1 := node1.PeerID()
	node1.Close()

	// Create second node with same data dir
	cfg2 := newTestConfig(t)
	cfg2.DataDir = dataDir

	node2, err := New(ctx, cfg2, logger)
	if err != nil {
		t.Fatalf("New with same data dir failed: %v", err)
	}
	defer node2.Close()

	id2 := node2.PeerID()

	// IDs should match (same identity loaded)
	if id1 != id2 {
		t.Errorf("Persistent identity should produce same peer ID: %s vs %s", id1, id2)
	}
}

func TestNode_PeerID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	id := node.PeerID()

	// Should be non-empty
	if id == "" {
		t.Error("PeerID() should return non-empty peer ID")
	}

	// Should be consistent
	if node.PeerID() != id {
		t.Error("PeerID() should return consistent value")
	}
}

func TestNode_Addrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	addrs := node.Addrs()

	// Should have at least one address
	if len(addrs) == 0 {
		t.Error("Node should have at least one listen address")
	}

	// Addresses should contain TCP or QUIC
	for _, addr := range addrs {
		addrStr := addr.String()
		if !strings.Contains(addrStr, "tcp") && !strings.Contains(addrStr, "quic") {
			t.Errorf("Unexpected address format: %s", addrStr)
		}
	}
}

func TestNode_ConnectedPeers_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	// New node should have no connected peers
	count := node.ConnectedPeers()
	if count != 0 {
		t.Errorf("New node should have 0 connected peers, got %d", count)
	}
}

func TestNode_Close(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Close should not error
	if err := node.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Double close should not panic (may or may not error)
	node.Close()
}

func TestNode_SetContentGetter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	// Set a content getter
	called := false
	getter := func(hash string) (io.ReadCloser, int64, error) {
		called = true
		return nil, 0, nil
	}

	node.SetContentGetter(getter)

	// Content getter should be set (we can't easily verify it's called
	// without a full protocol exchange, but at least we verify no panic)
	if called {
		t.Error("Content getter should not be called on set")
	}
}

func TestNode_WaitForBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	cfg.BootstrapPeers = nil // No bootstrap peers
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	// With no bootstrap peers, WaitForBootstrap should return quickly
	done := make(chan struct{})
	go func() {
		node.WaitForBootstrap()
		close(done)
	}()

	select {
	case <-done:
		// Expected
	case <-time.After(10 * time.Second):
		t.Error("WaitForBootstrap should complete quickly with no bootstrap peers")
	}
}

func TestNode_RoutingTableSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	// New node without connections should have empty or small routing table
	size := node.RoutingTableSize()
	if size < 0 {
		t.Error("RoutingTableSize should not be negative")
	}
}

func TestNew_WithPSK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	cfg := newTestConfig(t)
	cfg.PSK = psk
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New with PSK failed: %v", err)
	}
	defer node.Close()

	if node.PeerID() == "" {
		t.Error("Node with PSK should have a peer ID")
	}
}

func TestNew_WithAllowlist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	cfg.PeerAllowlist = []string{"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"}
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New with allowlist failed: %v", err)
	}
	defer node.Close()

	if node.PeerID() == "" {
		t.Error("Node with allowlist should have a peer ID")
	}
}

func TestNew_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cfg := newTestConfig(t)
	logger := newTestLogger()

	// Creating node with cancelled context may fail or succeed depending on timing
	node, err := New(ctx, cfg, logger)
	if err == nil && node != nil {
		node.Close()
	}
	// We're mainly checking this doesn't panic
}

func TestNode_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Cancel context
	cancel()

	// Give time for shutdown
	time.Sleep(100 * time.Millisecond)

	// Close should still work
	node.Close()
}

func TestNode_Scorer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	scorer := node.Scorer()
	if scorer == nil {
		t.Error("Scorer() should return non-nil scorer")
	}
}

func TestNode_Timeouts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	tm := node.Timeouts()
	if tm == nil {
		t.Error("Timeouts() should return non-nil manager")
	}
}

func TestNode_GetPeerStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t)
	logger := newTestLogger()

	node, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer node.Close()

	stats := node.GetPeerStats()
	// Should return empty slice, not nil
	if stats == nil {
		t.Error("GetPeerStats() should return non-nil slice")
	}
}

func TestNode_TwoNodes_BasicSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	// Create first node
	cfg1 := newTestConfig(t)
	node1, err := New(ctx, cfg1, logger)
	if err != nil {
		t.Fatalf("New node1 failed: %v", err)
	}
	defer node1.Close()

	// Create second node
	cfg2 := newTestConfig(t)
	node2, err := New(ctx, cfg2, logger)
	if err != nil {
		t.Fatalf("New node2 failed: %v", err)
	}
	defer node2.Close()

	// Both nodes should have different peer IDs
	if node1.PeerID() == node2.PeerID() {
		t.Error("Two nodes should have different peer IDs")
	}

	// Both nodes should have addresses
	if len(node1.Addrs()) == 0 {
		t.Error("Node1 should have addresses")
	}
	if len(node2.Addrs()) == 0 {
		t.Error("Node2 should have addresses")
	}
}
